## 1. 更新依赖和基础类型

- [ ] 1.1 更新 go.mod 中 nsp-platform 依赖到最新版本，确保能编译通过新版 taskqueue 包
- [ ] 1.2 定义 reply 消息的数据结构（ReplyPayload），包含 task_type、resource_id、resource_type、step_index、total_steps、status、result、error 等字段，放在 `internal/orchestration/types.go`

## 2. 重构 Task Handlers

- [ ] 2.1 重构 `tasks/handlers.go`：将所有 handler 签名从 `func(cbSender) -> func(ctx, *TaskPayload) (*TaskResult, error)` 改为接收 Broker 参数，返回 `func(ctx, *taskqueue.Task) error`。handler 内部解析 Payload、执行业务逻辑、通过 Broker 将结果发布到 `task.Reply.Queue`
- [ ] 2.2 重构 `tasks/pccn_handlers.go`：同上，适配 PCCN 相关的 handler

## 3. 实现 Reply-based Orchestration

- [ ] 3.1 在 `internal/orchestration/` 中实现 workflow 定义和 step 链管理：定义 WorkflowDef（steps 列表）和 step 推进逻辑
- [ ] 3.2 实现 reply consumer handler：消费 reply 队列消息，根据 metadata 中的 step_index/total_steps 决定发布下一个 step 或标记 workflow 完成/失败，更新业务表状态
- [ ] 3.3 实现 orchestrator 的 `SubmitWorkflow` 方法：创建业务资源记录后，发布第一个 step 任务到对应设备队列，附带 ReplySpec 和 Metadata

## 4. 重构 AZ Orchestrator

- [ ] 4.1 重构 `internal/az/orchestrator/orchestrator.go`：移除 `taskqueue.Engine` 依赖，改为注入 `taskqueue.Broker`；重写 CreateVPC/CreateSubnet/CreatePCCN 使用新的 SubmitWorkflow 逻辑
- [ ] 4.2 移除 `BuildWorkflowHooks` 方法和 `HandleTaskCallback` 方法
- [ ] 4.3 重写 `GetVPCStatus`/`GetSubnetStatus`/`GetPCCNStatus`：不再查询 Engine Store，改为直接从业务表读取进度信息
- [ ] 4.4 重写 `GetTaskByID`/`ReplayTask`：适配新的任务管理方式（可使用 Inspector 查询 asynq 任务状态）
- [ ] 4.5 调整补偿任务（compensation task）逻辑：不再依赖 workflow store，直接根据业务表状态和时间判断是否需要补偿

## 5. 重构 VFW Orchestrator

- [ ] 5.1 重构 `internal/az/vfw/orchestrator/orchestrator.go`：与 AZ Orchestrator 同步改动，移除 Engine 依赖，使用 Broker + Reply 模式

## 6. 重构 AZ NSP API Server

- [ ] 6.1 重构 `internal/az/api/server.go`：移除 Engine 参数，改为接收 Broker；移除 HandleTaskCallback 和 GetCallbackQueueName
- [ ] 6.2 重构 `internal/az/vfw/api/server.go`：同上

## 7. 重构入口文件

- [ ] 7.1 重构 `cmd/az_nsp/main.go`：移除 Engine/PostgresStore 创建逻辑；创建 Broker 和 reply Consumer，在后台 goroutine 启动 reply consumer
- [ ] 7.2 重构 `cmd/az_nsp_vfw/main.go`：同上
- [ ] 7.3 重构 `cmd/worker/main.go`：移除 CallbackSender 创建；将 Broker 注入 handler 用于发送 reply

## 8. 清理和验证

- [ ] 8.1 移除不再使用的代码和导入（taskqueue.Engine、PostgresStore、WorkflowDefinition、StepDefinition、CallbackSender 等所有引用）
- [ ] 8.2 确保项目能成功编译（`go build ./...`）
- [ ] 8.3 更新 e2e 测试和 functional 测试适配新的接口
- [ ] 8.4 运行测试验证功能正确性
