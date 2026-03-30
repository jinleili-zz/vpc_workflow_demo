## Context

当前 vpc_workflow_demo 使用 nsp-platform/taskqueue 库中的 Engine/Workflow 机制来编排 VPC/Subnet/PCCN 创建流程。该机制包括：
- `taskqueue.Engine`: 提交 workflow、处理 callback、管理 step 状态机
- `taskqueue.PostgresStore`: 将 workflow/step 状态持久化到 `tq_workflows`/`tq_steps` 表
- `taskqueue.CallbackSender`: worker 完成任务后通过 callback 队列通知 Engine
- `taskqueue.WorkflowHooks`: Engine 在 step/workflow 状态变更时回调业务层

nsp-platform/taskqueue 最新版本已移除上述所有 workflow 相关代码，仅保留三个核心抽象：
- `Broker`: 发布任务到队列
- `Consumer`: 消费队列中的任务
- `Inspector`: 查询队列和任务状态

新版 taskqueue 提供了 `ReplySpec` 机制：发布任务时可指定 reply 队列，worker 处理完后可将结果发布到该队列，实现请求-应答模式。

## Goals / Non-Goals

**Goals:**
- 使 vpc_workflow_demo 能基于最新版 nsp-platform/taskqueue（仅 Broker/Consumer/Inspector）编译运行
- 验证精简后的 taskqueue 接口对实际业务编排场景的适用性
- 保持现有 API 接口和外部行为不变
- 使用 Reply 机制替代 Callback 机制，实现任务结果回送
- 在 orchestrator 层自行实现顺序执行的 workflow 逻辑

**Non-Goals:**
- 不引入新的外部 workflow 引擎
- 不修改 Top NSP -> AZ NSP 的 HTTP 通信架构
- 不修改业务数据库 schema（vpc_resources / subnet_resources / pccn_resources 等已有字段足够）
- 不引入 saga 模块（saga 是独立的分布式事务模块，与此次重构无关）

## Decisions

### 1. 用 Reply 队列替代 Callback 队列

**选择**: Worker 通过 `taskqueue.Task.Reply.Queue` 将结果发送到 orchestrator 监听的 reply 队列

**替代方案**: Worker 直接 HTTP 回调 orchestrator
- 缺点：增加网络耦合，worker 需要知道 orchestrator 地址，且需要处理 orchestrator 不可用的情况

**理由**: Reply 机制是 taskqueue 原生支持的模式，无需额外基础设施。worker 无需知道 orchestrator 地址，reply 消息通过 Redis 队列传递，天然具备持久化和重试能力。

### 2. Orchestrator 层自行管理 workflow 状态

**选择**: 在 AZ Orchestrator 中维护一个简单的 workflow 状态机，使用业务表（vpc_resources 等）的 status/completed_tasks/failed_tasks 字段跟踪进度

**替代方案**: 新建一个本地 workflow 包
- 缺点：过度工程化，demo 的 workflow 本质上就是顺序执行 3 个步骤

**理由**: 当前 VPC 创建只有 3 个顺序步骤，子网创建只有 2 个步骤。这种简单的链式执行可直接用 reply consumer 中的逻辑实现：收到一个 step 的 reply 后，发布下一个 step。无需抽象通用 workflow 引擎。

### 3. Handler 签名与新版 taskqueue 对齐

**选择**: Handler 签名改为 `func(ctx context.Context, task *taskqueue.Task) error`

**理由**: 这是新版 taskqueue.HandlerFunc 的标准签名。handler 从 `task.Payload` 解析业务参数，从 `task.Reply` 获取回复队列信息。handler 处理完后通过 Broker 将结果发送到 reply 队列。

### 4. 任务元数据传递方式

**选择**: 使用 `taskqueue.Task.Metadata` 字段携带 workflow 上下文（如 resource_id、resource_type、step_index、total_steps）

**理由**: Metadata 是 taskqueue 原生支持的 map[string]string 字段，通过 trace envelope 在 broker/consumer 间透传。orchestrator 发布任务时写入，reply handler 收到结果后读取，用于定位和更新对应的业务资源。

## Risks / Trade-offs

- **[顺序执行的延迟]** → 每个 step 完成后才发布下一个 step，通过 reply 队列有少量延迟。对于 demo 场景（每个 step 模拟 2s）可接受。
- **[reply 消息丢失]** → 如果 reply consumer 不在线时消息到达，消息不会丢失（Redis/asynq 持久化）。orchestrator 重启后会继续消费。
- **[orchestrator 需要同时是 consumer]** → AZ NSP 既是 HTTP 服务又需要运行一个 reply consumer。增加了 AZ NSP 的职责。通过在 main 中 goroutine 启动 consumer 即可。
- **[状态一致性]** → 业务表状态更新和 reply 处理不在一个事务中，可能出现不一致。保留现有的补偿任务（compensation task）机制处理。
