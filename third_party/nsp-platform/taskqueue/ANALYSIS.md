# TaskQueue 现状分析

## 当前职责

`taskqueue` 现在只负责消息投递抽象，不再负责 workflow 编排。

职责边界如下：

- `taskqueue/`
  - 公共类型
  - 公共接口
  - Inspector 数据模型
- `taskqueue/asynqbroker/`
  - asynq 发布
  - asynq 消费
  - trace/reply/metadata envelope
  - Inspector 实现

## 当前目录职责

- `task.go`: `Task` / `ReplySpec` / `TaskInfo` / `Priority`
- `broker.go`: `Broker` 接口
- `consumer.go`: `Consumer` 接口
- `handler.go`: `HandlerFunc`
- `inspector.go`: 四层 Inspector 抽象
- `asynqbroker/broker.go`: 发布实现
- `asynqbroker/consumer.go`: 消费实现
- `asynqbroker/trace_envelope.go`: trace/reply/metadata 封装
- `asynqbroker/inspector.go`: 巡检和控制实现

## 关键行为

### 发布链路

1. 业务构造 `Task`
2. `Broker.Publish` 接收 `Task`
3. `asynqbroker` 按需封装 envelope
4. 任务进入目标队列

### 消费链路

1. `Consumer.Handle` 按 `Task.Type` 注册 handler
2. `asynqbroker` 消费到消息
3. 解包 envelope
4. 恢复 trace / reply / metadata
5. 构造完整 `*taskqueue.Task`
6. 调用 handler

### 巡检链路

1. `Inspector` 查询队列和 worker
2. `TaskReader` 查询任务详情
3. `TaskController` 执行批量或单任务操作
4. `QueueController` 管理队列

## 当前设计的几个约束

- 只保留 asynq 后端
- 不再提供 workflow 状态机
- 不再保留旧 callback 数据模型
- 不再暴露原始 asynq task 给业务层

## 为什么这样设计

旧实现的问题是 broker 抽象被 workflow 业务格式绑死，表现为：

- handler 依赖 `TaskPayload`
- consumer 要硬编码反序列化固定 JSON 结构
- broker 无法被普通异步任务场景直接复用

现在的设计把这些假设都移除了，最终只保留：

- 通用 `Task`
- 通用 `HandlerFunc`
- 通用 `Broker/Consumer/Inspector`

## 仍需调用方自己处理的事

- 业务 payload 的结构定义
- reply 消息体格式
- `Priority` 到队列名的映射策略
- 失败重试的业务语义

这正是 `taskqueue` 作为 broker SDK 的预期边界。
