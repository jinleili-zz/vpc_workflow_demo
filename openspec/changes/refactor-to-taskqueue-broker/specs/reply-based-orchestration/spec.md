## ADDED Requirements

### Requirement: Task handler 使用新版 taskqueue.HandlerFunc 签名

所有 task handler SHALL 使用 `func(ctx context.Context, task *taskqueue.Task) error` 签名。handler 从 `task.Payload` 解析业务参数，从 `task.Reply` 获取回复目标队列。handler 不再接收 `CallbackSender` 作为依赖。

#### Scenario: Handler 处理 VPC 创建任务成功
- **WHEN** worker 收到 type 为 `create_vrf_on_switch` 的任务，且 `task.Reply` 指定了 reply 队列
- **THEN** handler 解析 `task.Payload` 中的 VPC 参数，执行业务逻辑，通过 Broker 将成功结果发布到 `task.Reply.Queue`

#### Scenario: Handler 处理任务失败
- **WHEN** handler 执行过程中发生错误
- **THEN** handler 通过 Broker 将失败结果（包含错误信息）发布到 `task.Reply.Queue`，并返回 error

### Requirement: Orchestrator 通过 Reply 队列接收任务结果

AZ Orchestrator SHALL 启动一个 reply consumer 监听专属的 reply 队列。当 worker 完成任务并将结果发送到 reply 队列时，orchestrator 接收结果并推进 workflow。

#### Scenario: 收到 step 成功回复后推进下一步
- **WHEN** orchestrator 收到某个 step 的成功 reply，且该 workflow 还有后续 step
- **THEN** orchestrator 更新业务表的 completed_tasks 计数，并通过 Broker 发布下一个 step 的任务（附带相同的 reply 队列）

#### Scenario: 收到最后一个 step 的成功回复
- **WHEN** orchestrator 收到 workflow 最后一个 step 的成功 reply
- **THEN** orchestrator 更新业务表的 completed_tasks 计数，并将资源状态更新为 `running`

#### Scenario: 收到 step 失败回复
- **WHEN** orchestrator 收到某个 step 的失败 reply
- **THEN** orchestrator 更新业务表的 failed_tasks 计数，并将资源状态更新为 `failed`，记录错误信息

### Requirement: 任务发布时携带 workflow 上下文

Orchestrator 发布任务时 SHALL 在 `taskqueue.Task.Metadata` 中包含 workflow 上下文信息，包括 `resource_id`、`resource_type`、`step_index`、`total_steps`。

#### Scenario: 发布 VPC 创建的第一个任务
- **WHEN** orchestrator 开始 VPC 创建流程
- **THEN** 发布第一个任务时，Metadata 包含 `resource_type=vpc`、`resource_id=<vpc_id>`、`step_index=0`、`total_steps=3`，且 `task.Reply` 指向 orchestrator 的 reply 队列

#### Scenario: Reply handler 根据 metadata 识别 workflow 上下文
- **WHEN** reply consumer 收到一条 reply 消息
- **THEN** 从消息的 Metadata 中提取 `resource_type`、`resource_id`、`step_index`、`total_steps`，据此决定是否发布下一个 step 或标记 workflow 完成

### Requirement: Worker 端去除 CallbackSender 依赖

Worker 进程 SHALL 不再创建或使用 `taskqueue.CallbackSender`。Worker 仅需 Broker（用于发送 reply）和 Consumer（用于消费任务队列）。

#### Scenario: Worker 启动
- **WHEN** worker 进程启动
- **THEN** 创建 asynqbroker.Broker 和 asynqbroker.Consumer，注册 handler，handler 通过注入的 Broker 发送 reply

### Requirement: AZ NSP 去除 Engine 依赖

AZ NSP 进程 SHALL 不再创建 `taskqueue.Engine` 或 `taskqueue.PostgresStore`。orchestrator 直接使用 Broker 发布任务、Consumer 消费 reply。

#### Scenario: AZ NSP 启动
- **WHEN** AZ NSP 服务启动
- **THEN** 创建 asynqbroker.Broker 和 asynqbroker.Consumer（reply consumer），将 Broker 注入 orchestrator，在后台 goroutine 启动 reply consumer
