# TaskQueue 使用指南

本文档对应当前仓库中的 `taskqueue` 实现：纯 broker 抽象 + `asynqbroker` 适配。

## 你能用它做什么

- 发布异步任务
- 消费任务
- 透传 trace
- 给任务附带元数据
- 指定 reply 队列
- 查询和管理队列内任务

## 你不能再用它做什么

- 提交 workflow
- 查询 workflow 状态
- 依赖 `TaskPayload` / `TaskResult`
- 使用 `HandleRaw`
- 使用仓库内置 RocketMQ 后端

## 核心模型

```go
type Task struct {
    Type     string
    Payload  []byte
    Queue    string
    Reply    *ReplySpec
    Priority Priority
    Metadata map[string]string
}
```

使用原则：

- `Payload` 直接放业务 JSON
- `Queue` 由业务自己决定
- `Reply` 为 `nil` 时表示不需要回调
- `Priority` 是保留字段，不会自动映射成队列

## 发布任务

```go
broker := asynqbroker.NewBroker(asynq.RedisClientOpt{Addr: "127.0.0.1:6379"})
defer broker.Close()

body, _ := json.Marshal(map[string]any{
    "email": "user@example.com",
    "title": "welcome",
})

info, err := broker.Publish(ctx, &taskqueue.Task{
    Type:    "email:send",
    Payload: body,
    Queue:   "mail:high",
    Reply:   &taskqueue.ReplySpec{Queue: "mail:callback"},
    Metadata: map[string]string{
        "tenant": "acme",
    },
})
```

## 消费任务

```go
consumer := asynqbroker.NewConsumer(asynq.RedisClientOpt{Addr: "127.0.0.1:6379"}, asynqbroker.ConsumerConfig{
    Concurrency: 4,
    Queues: map[string]int{
        "mail:high": 30,
        "mail:low":  10,
    },
})

consumer.Handle("email:send", func(ctx context.Context, task *taskqueue.Task) error {
    var payload struct {
        Email string `json:"email"`
        Title string `json:"title"`
    }
    if err := json.Unmarshal(task.Payload, &payload); err != nil {
        return err
    }

    // task.Reply / task.Metadata / trace 已恢复
    return nil
})
```

## Reply 约定

`ReplySpec` 只描述“回复到哪里”，不描述“回复内容长什么样”。

也就是说：

- reply 队列是平台约定
- reply 消息体仍由业务自己定义

推荐做法：

1. producer 发送任务时设置 `Reply`
2. worker 在 handler 内根据 `task.Reply` 再发布一条普通任务消息
3. producer 或其他服务消费 reply 队列

## Trace 与 Metadata

asynq 适配层会在需要时自动封装内部 envelope：

- 有 trace
- 有 `Reply`
- 有 `Metadata`

handler 拿到的 `task.Payload` 已经是业务原文，不需要自己解 `_v/_tid/_meta/_rto`。

## Priority 的现实语义

当前实现里，优先级真正由两部分决定：

1. 任务被发到了哪个队列
2. 消费端 `ConsumerConfig.Queues` 的权重配置

因此推荐做法是：

- 用 `Priority` 保持业务语义
- 用 `Queue` 决定实际路由

## Inspector

```go
inspector := asynqbroker.NewInspector(asynq.RedisClientOpt{Addr: "127.0.0.1:6379"})
defer inspector.Close()

stats, _ := inspector.GetQueueStats(ctx, "mail:high")

if tr, ok := inspector.(taskqueue.TaskReader); ok {
    detail, _ := tr.GetTaskInfo(ctx, "mail:high", "task-id")
    _ = detail
}
```

`TaskDetail.Payload` 是已解包后的业务 payload。

## 示例

当前仓库里与最新设计一致的示例：

- `examples/taskqueue-broker-demo/`
- `examples/taskqueue-priority-demo/`

其中：

- broker demo 展示 `ReplySpec` 回调队列的用法
- priority demo 展示队列权重和 `*taskqueue.Task` handler 签名
