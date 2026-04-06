# TaskQueue 兼容性报告

本文档记录 `taskqueue` 从旧模型切换到当前 broker-only 模型后的主要破坏性变化。

## 已删除

- `Engine`
- `Config`（workflow 入口配置）
- `Store`
- `PostgresStore`
- `WorkflowDefinition`
- `StepDefinition`
- `WorkflowStatusResponse`
- `TaskPayload`
- `TaskResult`
- `CallbackPayload`
- `HandleRaw`
- `rocketmqbroker/`

## 已保留

- `Task`
- `TaskInfo`
- `Priority`
- `Broker`
- `Consumer`
- `Inspector` 四层接口体系

## 已新增

- `ReplySpec`
- `Task.Reply`
- asynq envelope 中的 `_rto`
- asynq envelope 中的 `_meta`

## 接口变化

旧消费接口：

```go
func(ctx context.Context, payload *TaskPayload) (*TaskResult, error)
```

现消费接口：

```go
func(ctx context.Context, task *Task) error
```

迁移方式：

- `payload.Params` -> `task.Payload`
- `payload.TaskType` -> `task.Type`
- 旧 callback 协议 -> `task.Reply`
- handler 返回值 -> 直接返回 `error`

## 行为变化

### 1. broker 不再假设固定业务 payload 结构

旧实现要求消息里带 `task_id/resource_id/task_params`。

当前实现中：

- broker 不解析业务 payload
- handler 自己决定如何反序列化

### 2. 消费端不再暴露原始 asynq task

公共接口已删除 `HandleRaw`，业务层只接触 `*taskqueue.Task`。

### 3. workflow 从 `taskqueue` 中彻底移除

如果业务需要编排，应由其他模块承担，不再通过 `taskqueue` 提供。

### 4. RocketMQ 不再内置

当前仓库里只保留 asynq 实现。

## 兼容性结论

这是一次明确的 breaking change：

- 老的 worker handler 需要改签名
- 老的 workflow 调用代码不能直接继续使用
- 老的 callback 协议代码需要迁移到普通 task + reply 模型

但新的好处也很明确：

- broker 层不再携带 workflow 假设
- `Task` 可以贯穿生产和消费全链路
- trace / reply / metadata 透传逻辑集中在适配层
