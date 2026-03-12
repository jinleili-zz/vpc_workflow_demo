# PR #9 Code Review：VPC 状态查询优化实现

> 对应 PR：[#9 - VPC 状态查询优化：DB 直查 + SAGA Async Poll](https://github.com/jinleili-zz/vpc_workflow_demo/pull/9)
> 对应方案文档：[docs/VPC_STATUS_OPTIMIZATION_PLAN.md](./VPC_STATUS_OPTIMIZATION_PLAN.md)

---

## 1. 变更概述

本 PR 实现了 VPC 状态查询优化方案，核心改动：

| 改动 | 文件 | 说明 |
|-----|------|------|
| SAGA Step Sync→Async | `internal/top/orchestrator/orchestrator.go` | 将 `StepTypeSync` 改为 `StepTypeAsync` + Poll，SAGA 真正等待 worker 完成 |
| 后台事务监听 | `internal/top/orchestrator/orchestrator.go` | 新增 `watchSagaTransaction` goroutine，SAGA 完成后回写 `vpc_registry` |
| DB 直查优先 | `internal/top/api/server.go` | `getVPCStatus` 优先从 DB 查询，查不到降级扇出 |
| Model + DAO | `internal/models/firewall.go`, `internal/top/vpc/dao/dao.go` | `VPCRegistry` 增加 `SagaTxID` 字段，新增 `GetVPCsByName` 方法 |
| 数据库迁移 | `internal/db/migrations/003_add_saga_tx_id.sql` | `vpc_registry` 表增加 `saga_tx_id` 列及索引 |
| 长生命周期 ctx | `cmd/top_nsp/main.go` | `NewOrchestrator` 传入 `main()` 的 ctx，避免 HTTP 请求 ctx 取消后 goroutine 失效 |

---

## 2. Review 发现的问题

### 问题一：`watchSagaTransaction` 错误处理静默吞没

**严重程度**：中
**位置**：`internal/top/orchestrator/orchestrator.go` — `watchSagaTransaction` 方法

**问题描述**：

在 `watchSagaTransaction` 中，SAGA 查询失败和 DB 更新失败的错误都被静默忽略：

```go
// 第 989 行：SAGA 查询失败，直接 continue，没有任何日志
status, err := o.sagaEngine.Query(o.ctx, txID)
if err != nil || status == nil {
    continue  // ← 错误被吞没
}

// 第 997 行：UpdateVPCStatus 返回的 error 未处理
o.topDAO.UpdateVPCStatus(o.ctx, vpcName, az.ID, "running")
// ← 如果 DB 更新失败，VPC 状态永远停在 "creating"
```

**影响**：
- 如果 SAGA 引擎连接出现问题（如 PostgreSQL 短暂不可用），错误无感知，无法排查
- 如果 DB 更新失败，`vpc_registry` 表状态不会更新为 `"running"`，用户查询到的状态永远是 `"creating"`
- 生产环境中这类"静默失败"极难定位

**建议修复**：

```go
// SAGA 查询失败应记录日志
status, err := o.sagaEngine.Query(o.ctx, txID)
if err != nil {
    logger.Info("SAGA事务查询失败", "tx_id", txID, "error", err)
    continue
}
if status == nil {
    continue
}

// DB 更新失败应记录日志，并考虑重试
if err := o.topDAO.UpdateVPCStatus(o.ctx, vpcName, az.ID, "running"); err != nil {
    logger.Info("更新VPC状态失败", "vpc_name", vpcName, "az", az.ID, "error", err)
}
```

---

### 问题二：`watchSagaTransaction` 没有 goroutine 回收机制

**严重程度**：中
**位置**：`internal/top/orchestrator/orchestrator.go` — `CreateRegionVPC` 方法第 956 行

**问题描述**：

```go
// 第 956 行
go o.watchSagaTransaction(txID, req.VPCName, azs)
```

每次 `CreateRegionVPC` 都会启动一个后台 goroutine，但 `Orchestrator` 没有跟踪这些 goroutine 的生命周期：

- **没有 `sync.WaitGroup` 或类似机制**：进程优雅退出时，无法等待所有 watcher 完成最后一次 DB 写入
- **没有数量上限**：如果用户在短时间内创建大量 VPC，会产生大量长生命周期 goroutine（每个最长 15 分钟）
- **进程重启后丢失**：watcher 是内存态的，进程重启后不会恢复对未完成事务的监听

**影响**：
- 优雅退出时可能丢失最后一次 `UpdateVPCStatus` 调用，导致 VPC 状态不一致
- 大量并发 VPC 创建可能导致 goroutine 泄漏

**建议修复**：

```go
type Orchestrator struct {
    ctx        context.Context
    // ... 其他字段 ...
    wg         sync.WaitGroup  // 新增：用于跟踪后台 goroutine
}

// CreateRegionVPC 中
o.wg.Add(1)
go func() {
    defer o.wg.Done()
    o.watchSagaTransaction(txID, req.VPCName, azs)
}()

// 新增 Shutdown 方法
func (o *Orchestrator) Shutdown() {
    o.wg.Wait()
}
```

同时在 `cmd/top_nsp/main.go` 的优雅退出流程中调用 `orch.Shutdown()`。

---

### 问题三：超时分支中使用已取消的 context

**严重程度**：中
**位置**：`internal/top/orchestrator/orchestrator.go` — `watchSagaTransaction` 超时处理

**问题描述**：

```go
case <-timeout:
    logger.Info("SAGA监听超时，标记VPC失败", "tx_id", txID, "vpc_name", vpcName)
    for _, az := range azs {
        o.topDAO.UpdateVPCStatus(o.ctx, vpcName, az.ID, "failed")
        //                       ^^^ 如果此时 ctx 已被取消，DB 更新会失败
    }
```

`timeout` 和 `o.ctx.Done()` 是两个独立的 `select` 分支。由于 Go 的 `select` 在多个 case 同时就绪时随机选择，可能出现以下竞态：

1. 15 分钟超时触发
2. 同时 `o.ctx` 也已经被取消（比如进程正在退出）
3. `select` 随机选中了 `timeout` 分支
4. 此时 `o.ctx` 已被取消，`UpdateVPCStatus` 使用已取消的 ctx 执行 DB 操作 → 失败

**影响**：超时分支的 DB 写入操作可能因 context 取消而静默失败。

**建议修复**：

在 timeout 分支中使用独立 context：

```go
case <-timeout:
    logger.Info("SAGA监听超时，标记VPC失败", "tx_id", txID, "vpc_name", vpcName)
    // 使用独立 context 确保 DB 操作能完成
    dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    for _, az := range azs {
        if err := o.topDAO.UpdateVPCStatus(dbCtx, vpcName, az.ID, "failed"); err != nil {
            logger.Info("超时标记VPC失败时DB更新失败", "az", az.ID, "error", err)
        }
    }
    return
```

---

### 问题四：DB 查询降级时缺少日志

**严重程度**：低
**位置**：`internal/top/api/server.go` — `getVPCStatus` 方法

**问题描述**：

```go
// 快路径
if s.orchestrator.HasTopDAO() {
    vpcs, err := s.orchestrator.GetVPCStatusFromDB(ctx, vpcName)
    if err == nil && len(vpcs) > 0 {
        // ... 返回 DB 结果 ...
        return
    }
    // ← 这里 err != nil 时没有日志，直接 fallthrough 到扇出
}

// 慢路径（降级）
```

当 DB 查询出错（例如连接池耗尽、慢查询超时）时，代码静默降级到扇出查询，没有留下任何日志。

**影响**：
- 运维无法知道系统频繁降级的原因
- 如果 DB 持续不可用，每次 `getVPCStatus` 都会走扇出路径，性能退化到优化前的水平，但缺少告警信息

**建议修复**：

```go
if s.orchestrator.HasTopDAO() {
    vpcs, err := s.orchestrator.GetVPCStatusFromDB(ctx, vpcName)
    if err != nil {
        logger.InfoContext(ctx, "DB查询VPC状态失败，降级为扇出查询",
            "vpc_name", vpcName, "error", err)
    }
    if err == nil && len(vpcs) > 0 {
        // ... 返回 DB 结果 ...
        return
    }
}
```

---

### 问题五：Redis 配置可能与集群模式冲突

**严重程度**：低
**位置**：`cmd/top_nsp/main.go`、`deployments/docker/redis-e2e-node-*.conf`

**问题描述**：

PR 新增了 Redis Cluster 配置文件（3 节点集群），但 `cmd/top_nsp/main.go` 中的 Redis 初始化使用的是单实例 `REDIS_ADDR` 环境变量：

```go
// main.go 中
redisAddr := os.Getenv("REDIS_ADDR")
if redisAddr == "" {
    redisAddr = "localhost:6379"
}
```

如果部署环境使用的是 Redis Cluster（3 节点集群），但代码用单实例客户端连接，会出现 `MOVED` 重定向错误，导致 registry（AZ 注册、心跳）和 SAGA 引擎的 Redis 操作失败。

**影响**：
- E2E 测试环境（docker-compose-e2e.yml）使用 Redis Cluster，但连接方式不匹配
- 生产环境部署时可能踩坑

**建议修复**：

需确认 E2E 测试环境中 `REDIS_ADDR` 指向的是 Cluster 的某个节点还是单实例 Redis。如果使用 Cluster，需要改用 `redis.NewClusterClient`，或在 E2E 环境中使用单实例 Redis。建议统一为：

```go
// 判断是否为集群模式
if os.Getenv("REDIS_CLUSTER") == "true" {
    // 使用 ClusterClient
} else {
    // 使用单实例 Client
}
```

---

## 3. 总体评价

### 设计层面

方案设计合理，核心思路正确：
- SAGA Sync→Async + Poll 的改造合理利用了 nsp-common 已有能力，无需修改公共模块
- DB 直查 + 扇出降级的分层策略兼顾了性能和可靠性
- `saga_tx_id` 字段的引入为后续运维排查提供了关联线索

### 代码层面

实现总体完整，但有以下改进空间：
1. **错误处理需加强**：多处错误被静默忽略，影响可观测性
2. **goroutine 生命周期管理**：建议引入 `sync.WaitGroup` 跟踪后台 watcher
3. **Context 使用需更严谨**：超时/退出场景下的 context 竞态需要处理

### 建议优先级

| 优先级 | 问题 | 建议 |
|-------|------|------|
| P1 | 问题一：错误静默吞没 | 添加日志，DB 更新失败时记录 error |
| P1 | 问题二：goroutine 无回收 | 引入 WaitGroup + Shutdown 方法 |
| P2 | 问题三：context 竞态 | 超时分支使用独立 context |
| P2 | 问题四：降级缺少日志 | 添加 DB 查询失败日志 |
| P3 | 问题五：Redis 配置 | 确认 E2E 环境 Redis 模式，统一配置方式 |
