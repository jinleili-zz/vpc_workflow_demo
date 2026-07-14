# NSP 服务优雅退出分析

## 1. 文档目的

本文分析当前工程中 Top NSP、AZ NSP 和 Worker 在收到 `SIGINT`、`SIGTERM` 等退出信号后，是否能够：

1. 停止接收新的请求或任务；
2. 等待当前正在处理的工作完成；
3. 在超时后安全取消、重新投递或恢复未完成工作；
4. 正确关闭数据库、Redis、日志和后台 goroutine 等资源。

本文只描述当前实现，不代表目标设计已经落地。

## 2. 审查范围

审查基于 `vpc_workflow_demo` 提交：

```text
5c599be4148e
```

当前有效进程入口包括：

- `cmd/top_nsp/main.go`：Top NSP VPC
- `cmd/top_nsp_vfw/main.go`：Top NSP VFW
- `cmd/az_nsp/main.go`：AZ NSP VPC
- `cmd/az_nsp_vfw/main.go`：AZ NSP VFW
- `cmd/worker/main.go`：设备任务 Worker

同时检查了以下相关实现：

- `internal/top/api/server.go`
- `internal/top/vfw/api/server.go`
- `internal/top/orchestrator/orchestrator.go`
- `internal/az/api/server.go`
- `internal/az/vfw/api/server.go`
- `internal/az/orchestrator/orchestrator.go`
- `internal/az/vfw/orchestrator/orchestrator.go`
- `internal/orchestration/workflow.go`
- `tasks/handlers.go`
- `tasks/pccn_handlers.go`
- `nsp-platform/taskqueue/asynqbroker/consumer.go`
- asynq v0.26.0 的 Server Shutdown 实现

## 3. 总体结论

当前工程没有形成统一、完整的进程生命周期管理机制。

总体判断如下：

- **Top NSP 没有实现有效的优雅退出。**
- **AZ NSP 只对 reply consumer 做了有限度的优雅退出，HTTP 请求和后台任务没有完整处理。**
- **Worker 基本具备“有期限的优雅退出”，但不能保证所有任务都在当前进程内完成。**

| 进程 | 接收退出信号 | 停止接收新工作 | 等待在途工作 | 结论 |
| --- | --- | --- | --- | --- |
| top-nsp-vpc | 是 | 否 | 只取消并等待部分后台 goroutine；HTTP 不等待 | 未实现，且进程可能无法自行退出 |
| top-nsp-vfw | 否 | 否 | 否 | 完全没有实现 |
| az-nsp-vpc | 是 | reply 队列是；HTTP 否 | reply 最多约 8 秒；HTTP 不保证 | 部分实现 |
| az-nsp-vfw | 是 | reply 队列是；HTTP 否 | reply 最多约 8 秒；HTTP 不保证 | 部分实现 |
| worker | 是 | 是 | 在途任务最多约 8 秒，超时重新入队 | 基本实现为有期限 drain |

## 4. 各进程分析

### 4.1 Top NSP VPC

信号处理位于 `cmd/top_nsp/main.go`：

```go
go func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Platform().Info("收到关闭信号，正在优雅关闭...")
	cancel()
	orch.Shutdown()
}()
```

收到信号后，进程会取消根 context，并等待 Orchestrator 内通过 WaitGroup 登记的部分后台 goroutine。

但是 HTTP 服务通过 Gin 的阻塞式 `Run()` 启动：

```go
func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}
```

当前实现没有持有 `http.Server`，也没有调用 `http.Server.Shutdown()` 或关闭 listener。

这会产生以下问题：

1. `signal.Notify` 已经接管 SIGTERM，操作系统不再执行默认终止行为；
2. 主 goroutine 仍然阻塞在 `server.Run()`；
3. 信号处理 goroutine 完成后，进程仍可能继续运行；
4. HTTP 服务仍然可以接收新请求；
5. `components.Shutdown()`、数据库关闭、配置加载器关闭等 defer 不会执行；
6. 在 Docker 等环境中，进程最终可能只能由编排器强制发送 SIGKILL。

此外，Orchestrator 在根 context 取消后，会将部分未完成 VPC/PCCN 状态标记为 `interrupted`，然后结束本地状态观察 goroutine。这属于“中断当前业务”，不是“等待业务流程完成”。

因此，Top NSP VPC 当前不仅没有实现 HTTP drain，还存在收到 SIGTERM 后无法自行退出的风险。

### 4.2 Top NSP VFW

`cmd/top_nsp_vfw/main.go` 没有注册 SIGINT 或 SIGTERM，也没有 HTTP Shutdown 逻辑：

```go
if err := server.Run(addr); err != nil {
	logger.Platform().Error("服务启动失败", "error", err)
	os.Exit(1)
}
```

收到 SIGTERM 时，进程使用操作系统默认行为直接终止。Go 进程被信号直接终止时，不能依赖 defer 执行，因此以下操作都没有保证：

- 当前 HTTP 请求完成；
- 跨 AZ 并发调用完成；
- 数据库连接正常关闭；
- 基础组件执行 Shutdown；
- 日志缓冲完成 flush。

另外，部分 Top VFW handler 使用 `context.Background()`，没有使用 `c.Request.Context()`。这意味着即使以后增加 HTTP Shutdown，请求取消也无法自然传递到这些数据库和下游 HTTP 操作。

因此，Top NSP VFW 完全没有实现优雅退出。

### 4.3 AZ NSP VPC

`cmd/az_nsp/main.go` 会监听 SIGINT 和 SIGTERM。收到信号后的主要逻辑为：

```go
cancel()

shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
defer shutdownCancel()

replyConsumer.Stop()

<-shutdownCtx.Done()
```

其中：

- `cancel()` 用于通知心跳和补偿任务退出；
- `replyConsumer.Stop()` 会停止 reply 队列消费，并尝试等待正在处理的 reply；
- main 最多等待到创建 context 后约 10 秒再返回。

但是该实现存在以下缺口：

1. API Server 没有调用 `http.Server.Shutdown()`；
2. 退出等待期间仍然可以接收新 HTTP 请求；
3. 当前 HTTP 请求没有被显式跟踪或等待；
4. 心跳和补偿 goroutine 只有 context cancel，没有 WaitGroup；
5. 心跳使用没有请求 context 和明确超时的 `http.Post`，已发出的调用不能被 cancel 立即打断；
6. 10 秒 context 没有传递给 `replyConsumer.Stop()`，不是真正控制 Stop 的 deadline；
7. TLS 模式虽然创建了 `http.Server`，但它只是 `Run()` 内的局部变量，主流程无法调用其 Shutdown。

因此，AZ NSP VPC 只是对 reply consumer 做了有限度 drain。HTTP 请求可能在这 10 秒内碰巧完成，但没有保证，而且服务在退出期间仍可接收新请求。

### 4.4 AZ NSP VFW

`cmd/az_nsp_vfw/main.go` 的退出流程为：

```go
cancel()
replyConsumer.Stop()
```

它可以：

- 通知心跳和补偿任务停止；
- 停止 reply consumer 拉取新任务；
- 给正在执行的 reply handler 一个有限的完成窗口。

但它仍然没有：

- 关闭 HTTP listener；
- 等待当前 HTTP handler；
- 等待心跳和补偿 goroutine 明确退出；
- 使用一个统一的 shutdown deadline；
- 区分正常 `http.ErrServerClosed` 和真正的启动失败。

所以 AZ NSP VFW 也只是部分实现优雅退出。

### 4.5 Worker

`cmd/worker/main.go` 会接收 SIGINT 和 SIGTERM，并调用：

```go
consumer.Stop()
```

`nsp-platform/taskqueue/asynqbroker.Consumer.Stop()` 最终同步调用 asynq Server 的 `Shutdown()`：

```go
func (c *Consumer) Stop() error {
	c.stopOnce.Do(func() {
		close(c.stopCh)
		c.server.Shutdown()
	})
	return nil
}
```

asynq v0.26.0 的 Shutdown 语义为：

1. 停止从队列拉取新任务；
2. 等待当前 active worker 完成；
3. 默认最多等待 8 秒；
4. 超时后中止本次执行，并把任务重新放回 Redis。

当前 demo Worker handler 通常只使用 `time.Sleep` 模拟 1～2 秒的设备操作。在 Redis 健康且没有额外阻塞时，这些任务通常可以在 8 秒内完成并发布 reply。

但这不是严格保证：

- `ShutdownTimeout` 没有通过 `ConsumerConfig` 暴露，当前只能使用 asynq 默认值；
- handler 中的 `time.Sleep` 不感知 context 取消；
- 如果未来真实设备调用超过 8 秒，asynq 可能已经重新投递任务，而原 handler 仍短暂运行；
- 如果设备侧副作用已经发生，但 reply 尚未发布，重新执行可能造成重复操作；
- `cmd/worker/main.go` 中额外的 10 秒等待没有传给 Consumer，不会扩大 asynq 的 8 秒 drain 窗口。

因此，Worker 当前实现的是“在 8 秒内尽量完成，否则重新投递”，而不是“无条件等待当前任务完成”。这属于业界常见的有期限优雅退出模型，但必须依赖任务幂等性保证安全。

## 5. 业界对优雅退出的一般定义

优雅退出通常不等于无限等待整个业务流程完成，而是一个有明确边界的进程生命周期协议。

一个相对完整的优雅退出流程通常包括：

1. **接收退出信号**：进程处理 SIGTERM/SIGINT，并进入 terminating 状态。
2. **停止流量接入**：将 readiness 置为失败，从负载均衡摘除，并关闭 HTTP listener 或停止从任务队列拉取新任务。
3. **等待在途工作**：在约定的 grace period 内等待已经接收的 HTTP 请求和任务执行完成。
4. **处理长任务**：无法在期限内完成的工作应保存进度、释放租约、重新入队或由其他实例恢复。
5. **关闭后台任务**：取消并等待心跳、扫描器、补偿任务和其他 goroutine 退出。
6. **关闭基础资源**：最后关闭数据库、Redis、消息队列、日志、指标上报等资源。
7. **超时强制退出**：超过总期限后允许强制取消，避免进程永久卡死。

对于本工程中的 VPC、PCCN、VFW 等长业务流程，优雅退出不应该要求单个实例等待整个流程结束。更合理的边界是：

- 当前 HTTP 请求在返回成功前已经可靠持久化 Saga 或任务；
- 停止接收新的请求和队列任务；
- 当前短请求和短任务在期限内完成；
- 未完成的持久化工作由其他实例从 PostgreSQL 或 Redis 恢复；
- Worker 设备操作保持幂等，能够安全应对至少一次投递和重复执行。

## 6. 主要风险

### 6.1 Top NSP VPC 可能无法响应 SIGTERM 正常退出

这是当前最高优先级问题。进程捕获了 SIGTERM，却没有关闭 HTTP Server，可能一直阻塞到编排器发送 SIGKILL。

### 6.2 HTTP 请求可能被截断

Top VFW、AZ VPC、AZ VFW 都没有 HTTP drain。数据库写入、任务发布或跨服务调用可能在中间状态被进程退出打断。

### 6.3 退出期间仍然接收新工作

AZ 服务在等待 Consumer 停止或固定计时期间，HTTP listener 仍然开放，可能不断产生新的工作，违背 drain 的前提。

### 6.4 Worker 超时重新投递可能造成重复副作用

如果设备操作超过 8 秒，任务可能被重新入队。若设备侧已经执行成功但 reply 未成功发布，后续 Worker 会再次执行同一任务，因此设备操作必须具备幂等键或可重复执行语义。

### 6.5 后台 goroutine 缺少统一等待机制

AZ 心跳和补偿任务只接收 context cancel，没有统一 WaitGroup。进程无法确认它们是否已经完成清理。

### 6.6 应用退出期限没有与部署配置对齐

Docker Compose 文件没有显式配置 `stop_grace_period`，应用内部也没有一个统一、可配置的总退出期限，容易出现应用尚未 drain 就被容器强制终止的情况。

## 7. 改进建议

### 7.1 P0：建立统一的进程生命周期

所有服务建议统一使用以下模式：

1. 使用 `signal.NotifyContext` 创建进程生命周期 context；
2. 所有 HTTP 服务持有显式的 `http.Server`；
3. 收到信号后先停止接收新 HTTP 请求和队列任务；
4. 使用同一个可配置 shutdown deadline 等待 HTTP 和 Consumer drain；
5. 再取消并等待后台 goroutine；
6. 最后停止 Saga Engine，并关闭数据库、Redis 和日志；
7. 对第二次退出信号提供立即强制退出能力。

### 7.2 P0：修复 Top NSP

- Top NSP VPC 必须在信号处理中关闭 HTTP Server，确保主 goroutine可以退出；
- Top NSP VFW 必须增加 SIGINT/SIGTERM 处理；
- Top VFW handler 应使用 `c.Request.Context()`，避免下游调用脱离请求生命周期。

### 7.3 P1：完善 AZ NSP drain

- API Server 增加 `Shutdown(context.Context)`；
- 退出时首先停止 HTTP 接入和 reply consumer 新任务接入；
- 为心跳、补偿任务增加 WaitGroup；
- 心跳 HTTP 请求使用带 context 和 timeout 的共享 Client；
- 删除固定等待到 context 超时的逻辑，改为等待真实组件退出。

### 7.4 P1：完善 Consumer 配置

- 在 `nsp-platform/taskqueue/asynqbroker.ConsumerConfig` 中暴露 `ShutdownTimeout`；
- 应用 shutdown deadline 应大于或等于 Consumer drain deadline，并留出资源关闭余量；
- Worker 和 reply consumer 应记录 drain 开始、完成、超时和重新入队数量。

### 7.5 P1：保证任务幂等和可恢复

- 每个设备操作携带稳定的业务幂等键；
- 重复执行时能够查询设备侧状态并返回已完成；
- 避免仅依赖“任务只消费一次”的假设；
- 对本地后台状态观察任务建立持久化恢复机制，避免实例重启后永久停留在 `interrupted` 或中间状态。

### 7.6 P1：补充退出测试

建议增加以下集成测试：

1. 慢 HTTP handler 执行期间发送 SIGTERM，验证当前请求完成；
2. 进入 terminating 状态后，验证新请求不再被接收；
3. Worker 执行短任务时发送 SIGTERM，验证任务完成并发布 reply；
4. Worker 执行超过 drain deadline 的任务，验证任务被重新入队；
5. 重复执行同一设备任务，验证不会产生重复资源；
6. 后台心跳、补偿和 Saga goroutine 在期限内退出；
7. 超过总 shutdown deadline 后，进程可以确定地结束。

## 8. 建议的退出顺序

```text
收到 SIGTERM
    ↓
标记为 not-ready / 从负载均衡摘除
    ↓
停止 HTTP listener 和队列新任务拉取
    ↓
等待在途 HTTP 请求和 Consumer handler
    ↓
持久化、重新投递或释放未完成长任务
    ↓
取消并等待心跳、补偿、状态扫描等后台任务
    ↓
停止 Saga Engine
    ↓
关闭 PostgreSQL、Redis、日志和其他资源
    ↓
正常退出
```

## 9. 验证说明

本次分析执行了以下构建验证：

```bash
GOCACHE=/tmp/go-build-vpc-workflow-demo go test ./cmd/...
GOCACHE=/tmp/go-build-vpc-workflow-demo go test ./tasks ./internal/orchestration
```

五个进程入口及相关任务编排包均编译通过。

当前仓库没有覆盖进程级 SIGTERM、HTTP drain 或慢任务 shutdown 的自动化测试，因此本文关于退出行为的结论主要来自入口、HTTP Server、后台 goroutine、Consumer 和 asynq Shutdown 调用链的静态分析。
