# Go-Machinery vs Asynq vs RocketMQ 对比分析

本文档基于 vpc_workflow_demo 项目中 asynq 的实际使用场景，对三种任务队列/消息队列系统进行全面对比。

## 一、概览

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **定位** | 分布式任务队列 | 轻量级任务队列 | 分布式消息中间件 |
| **开发语言** | Go | Go | Java (核心) |
| **项目地址** | github.com/RichardKnop/machinery | github.com/hibiken/asynq | github.com/apache/rocketmq |
| **协议** | MIT | MIT | Apache 2.0 |
| **成熟度** | 成熟稳定 | 活跃发展 | 企业级成熟 |

## 二、语言支持

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **原生语言** | Go | Go | Java |
| **客户端SDK** | Go only | Go only | Java, Go, Python, C++, .NET, Node.js |
| **多语言支持** | ❌ 仅 Go | ❌ 仅 Go | ✅ 官方多语言 SDK |
| **gRPC 支持** | ❌ | ❌ | ✅ |

**项目中的使用 (Asynq):**
```go
// internal/az/orchestrator/orchestrator.go
asynqTask := asynq.NewTask(task.TaskType, payloadData)
info, err := o.asynqClient.Enqueue(asynqTask, asynq.Queue(queueName))
```

## 三、后端存储

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **Broker 支持** | Redis, AMQP, SQS, GCP Pub/Sub | Redis only | 自研分布式存储 |
| **Result Backend** | Redis, Memcache, MongoDB, DynamoDB | Redis only | 内置 |
| **存储分离** | ✅ Broker 和 Backend 可分离 | ❌ 仅 Redis | ✅ NameServer + Broker 分离 |
| **云服务支持** | ✅ AWS SQS, GCP Pub/Sub | ❌ | ✅ 阿里云 RocketMQ |

**项目中的使用 (Asynq + Redis):**
```go
// cmd/worker/main.go
asynqClient := asynq.NewClient(asynq.RedisClientOpt{
    Addr: redisAddr,
    DB:   redisBrokerDB,
})
```

## 四、任务调度

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **即时任务** | ✅ | ✅ | ✅ |
| **延迟任务** | ✅ `ETA` | ✅ `ProcessIn/ProcessAt` | ✅ 定时/延时消息 |
| **定时任务 (Cron)** | ✅ 周期性任务 | ✅ `asynq.Scheduler` | ✅ |
| **任务链 (Chain)** | ✅ | ❌ 需手动实现 | ❌ 需手动实现 |
| **任务组 (Group)** | ✅ 并行执行 | ❌ | ✅ 批量消息 |
| **Chord** | ✅ Group + 回调 | ❌ | ❌ |
| **工作流编排** | ✅ Chain + Group + Chord | ❌ 需手动实现 | ❌ 需手动实现 |

**项目中手动实现的任务链 (Asynq):**
```go
// internal/az/orchestrator/orchestrator.go
func (o *AZOrchestrator) handleTaskSuccess(ctx context.Context, task *models.Task) error {
    // 任务成功后手动触发下一个任务
    nextTask, err := o.taskDAO.GetNextPendingTask(ctx, task.ResourceID)
    if nextTask != nil {
        if err := o.enqueueTask(ctx, nextTask); err != nil {
            return fmt.Errorf("入队下一个任务失败: %v", err)
        }
    }
    return nil
}
```

## 五、任务重试

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **自动重试** | ✅ | ✅ | ✅ |
| **重试次数配置** | ✅ `RetryCount` | ✅ `MaxRetry` | ✅ |
| **重试间隔策略** | Fibonacci 序列 | 指数退避 | 固定/递增 |
| **自定义重试策略** | ✅ `RetryTimeout` | ✅ `RetryDelayFunc` | ✅ |
| **死信队列** | ✅ | ✅ (Archive) | ✅ DLQ |
| **重试回调** | ✅ `OnError` | ✅ `ErrorHandler` | ✅ |

**项目中的重试配置:**
```go
// internal/az/orchestrator/orchestrator.go - 任务定义
{
    ID:         uuid.New().String(),
    TaskType:   "create_vrf_on_switch",
    RetryCount: 0,
    MaxRetries: 3,  // 最大重试3次
}
```

## 六、优先级队列

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **优先级支持** | ❌ 无原生支持 | ✅ 加权优先级队列 | ✅ 优先级消息 |
| **严格优先级** | ❌ | ✅ `StrictPriority` | ✅ |
| **加权优先级** | ❌ | ✅ 多队列加权 | ❌ |
| **动态优先级** | ❌ | ❌ | ❌ |

**项目中的优先级实现 (Asynq):**
```go
// internal/queue/queue.go
const (
    PriorityLow      TaskPriority = 1
    PriorityNormal   TaskPriority = 3
    PriorityHigh     TaskPriority = 6
    PriorityCritical TaskPriority = 9
)

func GetQueueConfig(region, az string, deviceType DeviceType) map[string]int {
    return map[string]int{
        baseQueue + "_critical": int(PriorityCritical),
        baseQueue + "_high":     int(PriorityHigh),
        baseQueue:               int(PriorityNormal),
        baseQueue + "_low":      int(PriorityLow),
    }
}

// cmd/worker/main.go
asynqServer := asynq.NewServer(
    asynq.RedisClientOpt{Addr: redisAddr, DB: redisBrokerDB},
    asynq.Config{
        Queues:         queuesConfig,
        StrictPriority: true,  // 严格按优先级处理
    },
)
```

## 七、多队列支持

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **多队列** | ✅ | ✅ | ✅ Topic/Queue |
| **队列隔离** | ✅ | ✅ | ✅ |
| **动态队列创建** | ✅ | ✅ | ✅ |
| **队列路由** | ❌ 需手动 | ✅ `asynq.Queue()` | ✅ MessageQueue |
| **跨队列消费** | ✅ | ✅ 一个 Worker 多队列 | ✅ Consumer Group |

**项目中的多队列实现:**
```go
// internal/queue/queue.go - 按 Region/AZ/设备类型 生成队列名
func GetQueueName(region, az string, deviceType DeviceType) string {
    return "tasks_" + region + "_" + az + "_" + string(deviceType)
}
// 示例: tasks_cn-beijing_cn-beijing-1a_switch

// 回调队列隔离
func GetCallbackQueueName(region, az string) string {
    return "callbacks_" + region + "_" + az
}
```

## 八、可靠性

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **消息持久化** | ✅ 依赖后端 | ✅ Redis 持久化 | ✅ 磁盘持久化 |
| **At-Least-Once** | ✅ | ✅ | ✅ |
| **At-Most-Once** | ❌ | ❌ | ✅ |
| **Exactly-Once** | ❌ | ❌ | ✅ 事务消息 |
| **幂等性** | ❌ 需业务实现 | ✅ Unique Task | ❌ 需业务实现 |
| **高可用** | 依赖后端 HA | Redis Cluster/Sentinel | Master-Slave 复制 |
| **事务消息** | ❌ | ❌ | ✅ |
| **顺序消息** | ❌ | ❌ | ✅ FIFO Queue |

**Asynq 唯一任务特性:**
```go
// 防止重复任务
asynq.Unique(time.Hour)  // 1小时内不允许重复
```

## 九、性能

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **吞吐量** | 中等 (依赖后端) | 高 (Redis 性能) | 极高 (百万级 TPS) |
| **延迟** | 毫秒级 | 毫秒级 | 毫秒级 |
| **并发控制** | ✅ Worker 数量 | ✅ `Concurrency` | ✅ Consumer 并发 |
| **速率限制** | ❌ | ✅ `RateLimit` | ✅ |
| **背压控制** | ❌ | ✅ 队列大小限制 | ✅ |
| **批量处理** | ❌ | ❌ | ✅ Batch Message |

**项目中的并发控制:**
```go
// cmd/worker/main.go
workerCount := 2
if workerCountEnv := os.Getenv("WORKER_COUNT"); workerCountEnv != "" {
    if count, err := strconv.Atoi(workerCountEnv); err == nil {
        workerCount = count
    }
}

asynqServer := asynq.NewServer(
    redisOpt,
    asynq.Config{
        Concurrency: workerCount,
    },
)
```

## 十、监控与运维

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **Web UI** | ❌ | ✅ Asynqmon | ✅ Console |
| **Metrics 导出** | ❌ | ✅ Prometheus | ✅ Prometheus/OpenTelemetry |
| **任务状态查询** | ✅ | ✅ Inspector | ✅ |
| **任务取消** | ✅ | ✅ | ✅ |
| **暂停/恢复队列** | ❌ | ✅ | ✅ |
| **日志追踪** | ✅ | ✅ | ✅ 消息轨迹 |

## 十一、部署复杂度

| 特性 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **依赖组件** | Redis/AMQP/SQS | Redis | NameServer + Broker |
| **最小部署** | 1 Redis | 1 Redis | 1 NameServer + 1 Broker |
| **生产部署** | Redis Cluster | Redis Cluster/Sentinel | 多 NameServer + 多 Broker |
| **容器化支持** | ✅ | ✅ | ✅ |
| **K8s Operator** | ❌ | ❌ | ✅ 官方 Operator |

## 十二、适用场景

### Go-Machinery 适用场景
- 需要复杂工作流编排 (Chain/Group/Chord)
- 需要多种后端存储支持
- 纯 Go 技术栈
- 中小规模任务处理

### Asynq 适用场景 (当前项目选择)
- 轻量级任务队列需求
- Redis 作为基础设施
- 需要优先级队列
- 需要简单易用的 API
- 高性能低延迟场景

**项目选择 Asynq 的原因:**
```go
// 1. 简单的任务定义和处理
mux.HandleFunc("create_vrf_on_switch", tasks.CreateVRFOnSwitchHandler(...))

// 2. 灵活的队列路由
asynqClient.Enqueue(task, asynq.Queue(queueName))

// 3. 优先级支持
asynq.Config{
    Queues:         queuesConfig,
    StrictPriority: true,
}

// 4. 与 Redis 无缝集成 (项目已使用 Redis 作为数据存储)
```

### RocketMQ 适用场景
- 企业级大规模消息系统
- 需要事务消息支持
- 需要严格消息顺序
- 多语言微服务架构
- 高吞吐量场景 (百万 TPS)
- 需要消息回溯功能

## 十三、总结对比表

| 维度 | Go-Machinery | Asynq | RocketMQ |
|------|-------------|-------|----------|
| **语言支持** | ⭐⭐ Go only | ⭐⭐ Go only | ⭐⭐⭐⭐⭐ 多语言 |
| **后端灵活性** | ⭐⭐⭐⭐⭐ 多后端 | ⭐⭐ Redis only | ⭐⭐⭐ 自研存储 |
| **工作流支持** | ⭐⭐⭐⭐⭐ 原生 Chain/Group | ⭐⭐ 需手动实现 | ⭐⭐ 需手动实现 |
| **优先级队列** | ⭐ 不支持 | ⭐⭐⭐⭐⭐ 完善 | ⭐⭐⭐⭐ 支持 |
| **可靠性** | ⭐⭐⭐ At-Least-Once | ⭐⭐⭐ At-Least-Once | ⭐⭐⭐⭐⭐ 事务消息 |
| **性能** | ⭐⭐⭐ 中等 | ⭐⭐⭐⭐ 高 | ⭐⭐⭐⭐⭐ 极高 |
| **易用性** | ⭐⭐⭐ 中等 | ⭐⭐⭐⭐⭐ 简单 | ⭐⭐⭐ 学习曲线 |
| **运维复杂度** | ⭐⭐⭐ 中等 | ⭐⭐⭐⭐⭐ 简单 | ⭐⭐ 复杂 |
| **社区活跃度** | ⭐⭐⭐ 稳定 | ⭐⭐⭐⭐ 活跃 | ⭐⭐⭐⭐⭐ 非常活跃 |

## 十四、迁移建议

### 从 Asynq 迁移到 Go-Machinery
如果需要原生工作流编排支持，可考虑迁移：
- 优点：原生 Chain/Group/Chord 支持
- 缺点：需重写优先级队列逻辑

### 从 Asynq 迁移到 RocketMQ
如果需要企业级可靠性和多语言支持，可考虑迁移：
- 优点：事务消息、顺序消息、高吞吐
- 缺点：部署复杂度高，需要调整架构

### 保持 Asynq (推荐)
当前项目使用 Asynq 是合理选择：
- Redis 已作为数据存储，无需额外基础设施
- 手动实现的任务链已满足 VPC 工作流需求
- 优先级队列支持设备类型和紧急程度区分
- 回调机制实现了任务状态同步

---

*文档生成时间: 2024年12月*
*基于项目: vpc_workflow_demo*
