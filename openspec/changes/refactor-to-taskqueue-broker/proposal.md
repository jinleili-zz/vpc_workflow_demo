## Why

当前 vpc_workflow_demo 大量依赖 nsp-platform/taskqueue 中已移除的 workflow 机制（`Engine`、`WorkflowDefinition`、`StepTask`、`CallbackSender`、`CallbackPayload`、`PostgresStore` 等），导致项目无法与最新版 taskqueue 库编译。最新版 taskqueue 仅保留了 Broker/Consumer/Inspector 三层抽象，去掉了 workflow 编排。需要重构 demo 以验证精简后的 taskqueue 接口是否满足实际业务编排需求。

## What Changes

- **BREAKING**: 移除对 `taskqueue.Engine`、`taskqueue.WorkflowDefinition`、`taskqueue.StepDefinition`、`taskqueue.StepTask`、`taskqueue.Workflow`、`taskqueue.WorkflowHooks`、`taskqueue.CallbackSender`、`taskqueue.CallbackPayload`、`taskqueue.PostgresStore` 等已删除类型的所有引用
- 将 task handler 签名从 `func(ctx, *taskqueue.TaskPayload) (*taskqueue.TaskResult, error)` 改为 `func(ctx, *taskqueue.Task) error`，与新版 `taskqueue.HandlerFunc` 对齐
- 在 AZ Orchestrator 层自行实现轻量级 workflow 编排逻辑：使用 Broker 的 Reply 机制（`taskqueue.ReplySpec`）接收 worker 回调，替代原有 `CallbackSender` + `Engine.HandleCallback` 模式
- 使用 PostgreSQL 中的业务表（vpc_resources / subnet_resources 等）直接管理任务进度和状态，不再依赖 `tq_workflows` / `tq_steps` 表
- Worker 端简化：去掉 `CallbackSender`，handler 通过 Reply 机制自动将结果回送给 orchestrator
- 保持 Top NSP -> AZ NSP -> Worker 的整体架构不变

## Capabilities

### New Capabilities
- `reply-based-orchestration`: 基于 taskqueue Broker Reply 机制的轻量级 workflow 编排，替代已移除的 Engine/Workflow 机制。orchestrator 发任务时附带 ReplySpec，worker 处理完后通过 Reply 队列回送结果，orchestrator 消费 reply 队列推进流程。

### Modified Capabilities

## Impact

- **代码**: `internal/az/orchestrator/`、`internal/az/vfw/orchestrator/`、`internal/az/api/`、`internal/az/vfw/api/`、`cmd/az_nsp/`、`cmd/az_nsp_vfw/`、`cmd/worker/`、`tasks/` 均需要修改
- **依赖**: `go.mod` 中 `nsp-platform` 版本需更新；移除对 `taskqueue.Engine` 系列类型的编译依赖
- **数据库**: 可移除 `tq_workflows`、`tq_steps` 表；业务表（vpc_resources 等）已有 total_tasks / completed_tasks / failed_tasks 字段，可直接使用
- **API**: 外部 API 接口不变，内部 orchestrator -> worker 通信协议从 callback 模式改为 reply 模式
- **测试**: e2e 和 functional 测试需要适配新的 handler 签名和 orchestrator 逻辑
