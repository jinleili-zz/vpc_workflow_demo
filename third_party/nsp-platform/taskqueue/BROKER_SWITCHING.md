# TaskQueue 后端切换说明

当前仓库只保留 `asynqbroker`。这份文档描述的是“如何保持公共接口稳定、允许以后切到别的 broker”，而不是现成的多后端操作手册。

## 公共接口

新的后端必须兼容下面这组接口和类型：

- `taskqueue.Task`
- `taskqueue.ReplySpec`
- `taskqueue.Broker`
- `taskqueue.Consumer`
- `taskqueue.HandlerFunc`
- `taskqueue.Inspector` 及其可选扩展层

## 必须保持的公共语义

### 发布侧

- `Publish(ctx, task)` 返回 `TaskInfo`
- 支持 `Queue`
- 支持 `Reply`
- 支持 `Metadata`
- 如果存在 trace，上下游要能恢复

### 消费侧

- handler 接收 `*taskqueue.Task`
- `task.Payload` 必须是业务原始 payload
- `task.Reply` 必须正确恢复
- `task.Metadata` 必须正确恢复
- `ctx` 中要能恢复 trace

### 巡检侧

- 保持 `Inspector` 四层接口结构不变
- `TaskDetail.Payload` 必须是解包后的业务 payload

## 当前 asynq 的做法

`asynqbroker` 使用内部 envelope 透传：

- trace
- reply
- metadata

切换到其他后端时，不要求复用完全相同的字段名，但要求保留相同的行为。

## 不再需要兼容的旧行为

这些旧模型不再是公共契约的一部分：

- workflow engine
- `TaskPayload`
- `TaskResult`
- `CallbackPayload`
- `HandleRaw`
- RocketMQ 仓库内实现

## 推荐的新增后端实现步骤

1. 实现 `Broker.Publish`
2. 实现 `Consumer.Handle/Start/Stop`
3. 实现最小的 `Inspector`
4. 在消费端恢复 `Task`
5. 补齐 trace/reply/metadata 透传
6. 再决定是否实现 `TaskReader/TaskController/QueueController`

## 结论

当前代码库的“可切换性”体现在接口层，而不是体现在仓库内同时维护多个后端实现。

也就是说：

- 现在只有 `asynqbroker`
- 未来可以新增别的后端
- 但新增后端时不能把 workflow 语义重新塞回公共接口
