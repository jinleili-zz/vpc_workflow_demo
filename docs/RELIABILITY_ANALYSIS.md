# 任务队列可靠性深度分析：asynq vs go-machinery

## 文档信息
- **生成日期**: 2025-11-26
- **分析重点**: 任务可靠性保证机制对比
- **结论**: asynq的可靠性设计确实优于go-machinery

---

## 一、核心问题澄清

### 你的疑问是对的！

**go-machinery也支持at-least-once投递**，但关键区别在于：

| 维度 | go-machinery | asynq |
|------|--------------|-------|
| **可靠性来源** | **依赖于Broker的实现** | **框架自身保证** |
| **实现复杂度** | 需要正确配置Broker | 开箱即用 |
| **崩溃恢复** | Broker机制（被动） | 主动心跳检测+租约机制 |
| **可靠性保证** | ⚠️ 间接保证（配置依赖） | ✅ 明确保证（代码实现） |

**关键差异**：
- **go-machinery**: "我依赖Redis/AMQP/SQS等Broker来保证可靠性"
- **asynq**: "我自己实现了可靠性机制，Redis只是存储层"

---

## 二、可靠性机制深度对比

### 2.1 asynq的可靠性保证机制

#### **机制1: 任务租约（Lease）系统**

asynq实现了一个**主动式**的可靠性保证：

```
Worker获取任务时:
1. 从Redis的pending队列取出任务
2. 设置任务租约（默认30秒）
3. 将任务移动到active集合（包含到期时间）
4. Worker开始处理任务

Worker定期发送心跳:
- 每隔一定时间（默认8秒）延长租约
- 告诉Redis："我还活着，还在处理这个任务"

如果Worker崩溃:
- 心跳停止，租约到期
- 后台goroutine（Recoverer）定期扫描active集合
- 发现租约过期的任务
- 自动将任务移回pending队列，供其他Worker重试
```

**代码层面的保证**:
```go
// asynq内部伪代码
type Task struct {
    ID          string
    LeaseExpiry time.Time  // 租约到期时间
}

// Worker处理循环
func (w *Worker) process(task *Task) {
    // 设置初始租约
    redis.Set(task.ID, time.Now().Add(30*time.Second))
    
    // 启动心跳goroutine
    go func() {
        ticker := time.NewTicker(8 * time.Second)
        for range ticker.C {
            redis.Extend(task.ID, 30*time.Second)  // 延长租约
        }
    }()
    
    // 处理任务
    handler.ProcessTask(task)
    
    // 完成后删除任务（只有成功才删除）
    redis.Del(task.ID)
}

// Recoverer后台扫描（伪代码示意，实际实现见下文）
func (r *Recoverer) Run() {
    for {
        time.Sleep(1 * time.Minute)
        
        // 查找所有active任务
        activeTasks := redis.GetActiveSet()
        now := time.Now()
        
        for _, task := range activeTasks {
            if task.LeaseExpiry.Before(now) {
                // 租约过期，移回pending队列
                redis.MoveFromActiveToPending(task.ID)
                log.Printf("恢复任务: %s (Worker可能已崩溃)", task.ID)
            }
        }
    }
}
```

**Recoverer的实际实现位置**:

在asynq源码中，Recoverer goroutine的实现位于：
- **文件路径**: `github.com/hibiken/asynq/recoverer.go`
- **启动位置**: 在`Server.Run()`方法中自动启动
- **运行方式**: 作为独立的后台goroutine持续运行

**核心实现代码**（基于v0.24.1版本）:

```go
// recoverer.go - 定义
type recoverer struct {
    broker   base.Broker
    interval time.Duration  // 扫描间隔（默认1分钟）
    queues   []string       // 需要监控的队列列表
    done     chan struct{}  // 停止信号
    logger   *log.Logger
}

// 启动recoverer goroutine
func (r *recoverer) start(wg *sync.WaitGroup) {
    wg.Add(1)
    go func() {
        defer wg.Done()
        r.recover()  // 立即执行一次恢复
        timer := time.NewTimer(r.interval)
        for {
            select {
            case <-r.done:
                r.logger.Debug("Recoverer done")
                timer.Stop()
                return
            case <-timer.C:
                r.recover()  // 定期执行恢复逻辑
                timer.Reset(r.interval)
            }
        }
    }()
}

// 恢复租约过期的任务
func (r *recoverer) recoverLeaseExpiredTasks() {
    // 获取30秒前已过期的任务（容忍时钟偏移）
    cutoff := time.Now().Add(-30 * time.Second)
    msgs, err := r.broker.ListLeaseExpired(cutoff, r.queues...)
    if err != nil {
        r.logger.Errorf("Failed to list lease expired tasks: %v", err)
        return
    }
    
    // 对每个过期任务进行恢复处理
    for _, msg := range msgs {
        if msg.Retried < msg.Retry {
            // 还有重试次数，将任务移回pending队列
            err = r.broker.Retry(msg, ...)
            r.logger.Infof("Recovered task: %s (lease expired)", msg.ID)
        } else {
            // 重试次数耗尽，移到archived
            err = r.broker.Archive(msg, ErrLeaseExpired)
            r.logger.Warnf("Archived task: %s (max retries exceeded)", msg.ID)
        }
    }
}

func (r *recoverer) recover() {
    r.recoverLeaseExpiredTasks()
    r.reclaimStaleAggregationSets()  // 同时回收过期的聚合集合
}
```

**3. Server中如何启动Recoverer**:

```go
// server.go 中的Run()方法（简化版）
func (srv *Server) Run(handler Handler) error {
    // 1. 创建所有后台组件
    recoverer := newRecoverer(recovererParams{
        broker:   srv.broker,
        interval: time.Minute,  // 每分钟扫描一次
        queues:   srv.queues,   // 监控所有配置的队列
        logger:   srv.logger,
    })
    
    heartbeater := newHeartbeater(...)
    scheduler := newScheduler(...)
    processor := newProcessor(...)
    
    // 2. 启动所有后台goroutine
    var wg sync.WaitGroup
    recoverer.start(&wg)      // ← 启动Recoverer goroutine
    heartbeater.start(&wg)    // ← 启动心跳goroutine
    scheduler.start(&wg)      // ← 启动调度器goroutine
    processor.start(&wg)      // ← 启动任务处理goroutine
    
    // 3. 阻塞等待退出信号
    <-srv.done
    
    // 4. 优雅关闭所有goroutine
    close(recoverer.done)
    close(heartbeater.done)
    close(scheduler.done)
    close(processor.done)
    
    wg.Wait()  // 等待所有goroutine退出
    return nil
}
```

---

**⚠️ 重要问题：Recoverer运行在哪个进程？**

**答案：Recoverer运行在每个Worker进程内部**

asynq的架构分为两个核心角色：

```
┌───────────────────────────────────────────────────────────┐
│                   asynq 进程架构                           │
├───────────────────────────────────────────────────────────┤
│                                                             │
│  Client进程              Worker进程1          Worker进程2  │
│  ┌────────────┐         ┌──────────────┐    ┌───────────┐ │
│  │ Client     │         │ Server       │    │ Server    │ │
│  │            │ 写Redis │ ├─Recoverer  │    │├─Recoverer│ │
│  │ Enqueue()──┼────────►│ ├─Processor  │    ││ Processor│ │
│  │            │         │ ├─Heartbeat  │    ││ Heartbeat│ │
│  └────────────┘         │ └─Scheduler  │    ││ Scheduler│ │
│                          └──────────────┘    └───────────┘ │
│                                   │                │        │
│                                   └────────────────┘        │
│                                           ▼                 │
│                                      ┌────────┐             │
│                                      │ Redis  │             │
│                                      └────────┘             │
└───────────────────────────────────────────────────────────┘
```

| 角色 | 包含组件 | 职责 | Recoverer？ |
|------|---------|------|------------|
| **Client** | `asynq.Client` | 发送任务到Redis | ❌ 无 |
| **Server (Worker)** | `asynq.Server` + Recoverer + Processor + Heartbeater + Scheduler | 消费并执行任务 + 崩溃恢复 | ✅ 有 |

```go
// ========== Client进程示例 ==========
// 纯粹的任务发送者，不包含任何恢复逻辑
func main() {
    client := asynq.NewClient(asynq.RedisClientOpt{Addr: "localhost:6379"})
    defer client.Close()
    
    task := asynq.NewTask("email:send", payload)
    client.Enqueue(task)  // 仅仅将任务写入Redis，然后退出
    
    // Client进程没有Recoverer！
}

// ========== Server (Worker) 进程示例 ==========
// 长期运行的Worker进程，包含完整的可靠性机制
func main() {
    srv := asynq.NewServer(
        asynq.RedisClientOpt{Addr: "localhost:6379"},
        asynq.Config{Concurrency: 10},
    )
    
    mux := asynq.NewServeMux()
    mux.HandleFunc("email:send", handleEmailTask)
    
    // srv.Run()会启动：
    // ✅ Recoverer goroutine（恢复过期任务）
    // ✅ Processor goroutine（消费并执行任务）
    // ✅ Heartbeater goroutine（发送心跳延长租约）
    // ✅ Scheduler goroutine（调度延迟任务）
    
    if err := srv.Run(mux); err != nil {
        log.Fatal(err)
    }
}
```

---

**4. 多Worker场景的协调机制**:

在生产环境中，通常会运行多个Worker进程来提高吞吐量：

```
Client进程             Worker进程1              Worker进程2              Worker进程3
┌──────────┐          ┌─────────────┐          ┌─────────────┐          ┌─────────────┐
│ Client   │          │ Server      │          │ Server      │          │ Server      │
│          │          │ ├─Recoverer │          │ ├─Recoverer │          │ ├─Recoverer │
│ Enqueue()│──┐       │ ├─Processor │          │ ├─Processor │          │ ├─Processor │
│          │  │       │ ├─Heartbeat │          │ ├─Heartbeat │          │ ├─Heartbeat │
└──────────┘  │       │ └─Scheduler │          │ └─Scheduler │          │ └─Scheduler │
              │       └─────────────┘          └─────────────┘          └─────────────┘
              │              │                         │                         │
              │              └─────────────────────────┼─────────────────────────┘
              ▼                                        ▼
        ┌─────────────────────────────────────────────────┐
        │                   Redis                          │
        │  - pending: [task1, task2, ...]                 │
        │  - active: {task3: expiry_time, ...}            │
        └─────────────────────────────────────────────────┘
```

**多Recoverer协调逻辑**:

```
时间轴示例（Worker1崩溃后的恢复过程）:

12:00:00 - Worker1的Processor正在处理task1
12:00:08 - Worker1的Heartbeater延长task1租约到12:00:38
12:00:16 - Worker1的Heartbeater延长task1租约到12:00:46
12:00:20 - Worker1进程崩溃！心跳停止
12:00:46 - task1租约到期，但还在active集合中

12:01:00 - Recoverer-1已死（Worker1崩溃）
12:01:00 - Recoverer-2扫描：发现task1过期 → 恢复到pending
12:01:00 - Recoverer-3扫描：task1已被Recoverer-2恢复，不存在active中

12:01:05 - Worker2的Processor从pending获取task1，重新处理
```

**并发恢复的安全性保证**:

Q: 多个Recoverer同时扫描，会不会重复恢复同一个任务？

A: **不会！** 因为：

1. **Redis原子操作**：
```lua
-- asynq使用Lua脚本保证原子性
-- 恢复任务的Lua脚本（简化示意）
local task = redis.call('ZREM', 'active', task_id)  -- 从active移除
if task then
    redis.call('LPUSH', 'pending', task)  -- 推入pending
    return 1
else
    return 0  -- 任务已被其他Recoverer恢复
end
```

2. **先到先得机制**：
   - Recoverer-2先执行ZREM，成功移除task1
   - Recoverer-3稍后执行ZREM，返回0（task1已不存在）
   - 因此task1只会被恢复一次

3. **冗余扫描无害**：
   - 多个Recoverer扫描同一个Redis，只是读操作
   - Redis性能极高，扫描开销可忽略
   - 换来的是**去中心化**和**无单点故障**

---

**5. 用户视角：完全无感知**

作为asynq的使用者，**完全无需关心**Recoverer的存在：

```go
// 你只需要这样写
func main() {
    srv := asynq.NewServer(
        asynq.RedisClientOpt{Addr: "localhost:6379"},
        asynq.Config{Concurrency: 10},
    )
    
    mux := asynq.NewServeMux()
    mux.HandleFunc("email:send", handleEmailTask)
    
    // Recoverer会自动在后台运行，无需任何配置！
    srv.Run(mux)
}
```

**Recoverer设计的优势总结**:

| 特性 | 说明 | 优势 |
|------|------|------|
| **去中心化** | 每个Worker都有Recoverer | ✅ 无单点故障 |
| **自动运行** | srv.Run()时自动启动 | ✅ 零配置，开箱即用 |
| **高可用** | 任何Worker存活即可恢复任务 | ✅ 极高可靠性 |
| **原子操作** | Redis Lua脚本保证安全 | ✅ 无重复恢复 |
| **时钟容差** | 使用30秒前的cutoff | ✅ 避免时钟偏移问题 |
| **并发安全** | 多Recoverer同时扫描不冲突 | ✅ 可水平扩展 |

**关键点**:
- ✅ **Recoverer不在Client进程中**（Client只负责发送任务）
- ✅ **Recoverer不是独立的进程**（无需单独部署守护进程）
- ✅ **每个Worker进程都有自己的Recoverer goroutine**
- ✅ 多个Recoverer同时扫描Redis不会冲突（原子操作保证）
- ✅ 任何Worker存活就能恢复任务（无单点故障）
- ⚠️ Recoverer扫描有一定冗余，但Redis性能高，影响可忽略

**用户视角**:

作为asynq的使用者，你**无需手动启动或管理**Recoverer goroutine：

```go
func main() {
    // 创建Server
    srv := asynq.NewServer(
        asynq.RedisClientOpt{Addr: "localhost:6379"},
        asynq.Config{Concurrency: 10},
    )
    
    // 调用Run()时，Recoverer会自动在后台启动
    // 你不需要关心它的存在！
    if err := srv.Run(handler); err != nil {
        log.Fatal(err)
    }
    // Recoverer在srv.Run()内部自动运行
}
```

**关键点**:
- ✅ **主动恢复**: 不依赖Worker主动报告，系统定期巡检
- ✅ **精确检测**: 基于时间戳，能准确判断Worker是否存活
- ✅ **自动重试**: 崩溃的任务自动重新入队，无需人工干预
- ✅ **开箱即用**: Server.Run()时自动启动，无需额外配置
- ✅ **时钟容差**: 使用30秒前的cutoff避免时钟偏移问题

---

#### **机制2: 明确的ACK机制**

asynq只在**任务真正完成**后才从Redis删除：

```go
// Handler正常返回nil
func handleTask(ctx context.Context, t *asynq.Task) error {
    // ... 执行任务逻辑
    return nil  // 只有返回nil，任务才会从Redis删除
}

// Handler返回错误
func handleTask(ctx context.Context, t *asynq.Task) error {
    // ... 执行失败
    return fmt.Errorf("处理失败")  // 任务自动移到retry队列
}

// Handler panic
func handleTask(ctx context.Context, t *asynq.Task) error {
    panic("程序崩溃")  // asynq捕获panic，任务移到retry队列
}
```

**状态转换保证**:
```
pending → active → (成功) → 删除
               ↓
            (失败) → retry → pending (重试)
                         ↓
                    (达到最大重试) → archived
```

---

#### **机制3: Redis Lua脚本保证原子性**

asynq使用Redis Lua脚本确保状态转换的原子性：

```lua
-- 示例：从pending移到active的Lua脚本
local task = redis.call('LPOP', KEYS[1])  -- 从pending队列取出
if task then
    redis.call('ZADD', KEYS[2], ARGV[1], task)  -- 加入active集合（带过期时间）
    return task
end
return nil
```

**优势**:
- ✅ 避免竞态条件（两个Worker同时取同一任务）
- ✅ 保证任务不会丢失（要么在pending，要么在active）

---

### 2.2 go-machinery的可靠性依赖

#### **依赖Broker的ACK机制**

go-machinery将可靠性**委托给Broker**：

**使用Redis Broker时**:
```go
// machinery内部（简化）
func (b *RedisBroker) consume(queue string) {
    for {
        // 从Redis List阻塞取出任务
        task := redis.BRPop(queue, 0)
        
        // 立即确认（从队列中移除）
        // ⚠️ 此时任务已从Redis删除！
        
        // 交给Worker处理
        worker.Process(task)
        
        // 如果Worker崩溃，任务已经丢失！
    }
}
```

**问题所在**:
- ❌ Redis List的`BRPOP`操作是**破坏性读取**（取出后立即删除）
- ❌ 如果Worker在处理过程中崩溃，任务已经从队列删除，无法恢复
- ❌ 依赖Result Backend存储状态，但这是**异步**的，有窗口期

**使用AMQP Broker时（RabbitMQ）**:
```go
// 使用AMQP时的可靠性
func (b *AMQPBroker) consume(queue string) {
    msgs, _ := channel.Consume(queue, "", false, false, false, false, nil)
    
    for msg := range msgs {
        // 处理任务
        worker.Process(msg.Body)
        
        // 手动ACK
        msg.Ack(false)  // ✅ 处理完才ACK
    }
}
```

**相对可靠**:
- ✅ AMQP支持manual ACK，只有Worker明确ACK后才删除消息
- ✅ Worker崩溃时，RabbitMQ会自动将未ACK的消息重新入队
- ⚠️ 但这依赖于**正确配置RabbitMQ**和machinery的AMQP参数

**问题**:
- ❌ **配置复杂**：需要理解AMQP的ACK、prefetch、durability等概念
- ❌ **不一致性**：Redis Broker和AMQP Broker行为不同
- ❌ **文档缺失**：machinery文档对可靠性配置说明不足

---

#### **Result Backend的局限**

machinery依赖Result Backend存储任务状态：

```go
// machinery的状态更新（异步）
func (w *Worker) Process(task *tasks.Signature) error {
    // 1. 更新状态为STARTED
    backend.SetTaskState(&tasks.TaskState{
        State: "STARTED",
        TaskUUID: task.UUID,
    })
    
    // 2. 执行任务
    result, err := task.Execute()
    
    // 3. 如果Worker在这里崩溃...
    //    状态仍然是STARTED，但任务已从队列删除
    
    // 4. 更新状态为SUCCESS/FAILURE
    if err != nil {
        backend.SetTaskState(&tasks.TaskState{State: "FAILURE"})
    } else {
        backend.SetTaskState(&tasks.TaskState{State: "SUCCESS", Result: result})
    }
}
```

**问题**:
- ❌ 状态更新和任务执行**不是原子操作**
- ❌ Worker崩溃后，任务状态可能卡在"STARTED"，但任务已丢失
- ❌ 需要额外的监控系统检测"僵尸任务"

---

### 2.3 实际场景对比

#### **场景1: Worker进程崩溃**

**asynq的表现**:
```
1. Worker从pending取出任务，设置30秒租约
2. Worker开始处理，每8秒发送心跳延长租约
3. Worker进程crash（如OOM、panic）
4. 心跳停止，租约在30秒后到期
5. Recoverer扫描发现租约过期
6. 任务自动移回pending队列
7. 其他Worker重新处理任务

✅ 任务不丢失，自动恢复
```

**go-machinery的表现（Redis Broker）**:
```
1. Worker从Redis List BRPop取出任务（任务立即删除）
2. Backend更新状态为STARTED
3. Worker进程crash
4. 任务已从队列删除，无法恢复
5. Backend中状态卡在STARTED

❌ 任务丢失！
   需要手动监控STARTED状态的任务，检测超时并重试
```

**go-machinery的表现（AMQP Broker + 正确配置）**:
```
1. Worker从RabbitMQ接收消息（未ACK）
2. Worker处理任务
3. Worker进程crash（未发送ACK）
4. RabbitMQ检测到连接断开
5. 消息自动重新入队
6. 其他Worker重新处理

✅ 任务不丢失（但依赖RabbitMQ配置正确）
```

---

#### **场景2: 网络分区**

**asynq的表现**:
```
Worker与Redis网络断开:
1. 心跳goroutine尝试延长租约失败
2. 但Worker继续处理任务
3. 租约到期，任务被其他Worker重新处理
4. 可能导致任务重复执行（at-least-once特性）

⚠️ 任务可能重复，但不会丢失
   (需要任务幂等性设计)
```

**go-machinery的表现（Redis Broker）**:
```
Worker与Redis网络断开:
1. Worker已经从队列取出任务（BRPOP已删除）
2. Worker继续处理，但无法更新状态到Backend
3. 任务完成后无法报告结果

⚠️ 任务可能完成但状态未知
   (需要额外的对账机制)
```

---

## 三、可靠性对比总结

### 3.1 核心差异表

| 可靠性维度 | go-machinery | asynq | 优势方 |
|-----------|--------------|-------|--------|
| **At-least-once保证** | ⚠️ 依赖Broker | ✅ 框架级保证 | **asynq** |
| **Worker崩溃恢复** | ❌ Redis Broker无保证<br>✅ AMQP Broker支持（需配置） | ✅ 租约机制自动恢复 | **asynq** |
| **任务ACK时机** | ⚠️ 取出即删除（Redis）<br>✅ 处理后ACK（AMQP） | ✅ 处理完才删除 | **asynq** |
| **状态一致性** | ❌ Backend异步更新 | ✅ 状态转换原子操作 | **asynq** |
| **配置复杂度** | ❌ 高（Broker相关） | ✅ 低（开箱即用） | **asynq** |
| **监控需求** | ❌ 需要额外监控僵尸任务 | ✅ 内置Inspector检测 | **asynq** |
| **幂等性要求** | ⚠️ 强制要求 | ⚠️ 强制要求 | 平手 |

### 3.2 可靠性评分

**评分维度（满分10分）**:

| 维度 | go-machinery (Redis) | go-machinery (AMQP) | asynq |
|------|---------------------|---------------------|-------|
| **任务不丢失** | 3分 | 8分 | 10分 |
| **崩溃恢复** | 2分 | 7分 | 10分 |
| **配置简易性** | 5分 | 3分 | 10分 |
| **状态一致性** | 4分 | 6分 | 10分 |
| **可观测性** | 3分 | 4分 | 10分 |
| **加权平均** | **3.4分** | **5.6分** | **10分** |

---

## 四、为什么说asynq可靠性更高

### 理由1: 主动式 vs 被动式

- **asynq**: 主动监控任务租约，及时发现并恢复异常
- **machinery**: 依赖Broker的被动机制，Redis Broker甚至没有恢复机制

### 理由2: 原子性保证

- **asynq**: 使用Redis Lua脚本保证状态转换的原子性
- **machinery**: Backend状态更新是异步的，存在不一致窗口

### 理由3: 一致性架构

- **asynq**: 所有操作都针对Redis优化，行为一致
- **machinery**: 不同Broker行为差异大，容易踩坑

### 理由4: 内置可观测性

- **asynq**: Inspector API、Web UI可以实时查看任务状态
- **machinery**: 需要自己实现监控系统

### 理由5: 生产验证

- **asynq**: 被多家公司在生产环境验证（GitHub issue显示广泛使用）
- **machinery**: 更新较少，社区反馈可靠性问题较多

---

## 五、实际生产建议

### 5.1 如果使用go-machinery

**必须采取的措施**:

1. **使用AMQP Broker（RabbitMQ）**，而非Redis
   ```go
   cnf := &config.Config{
       Broker:        "amqp://user:pass@host:5672/",
       ResultBackend: "redis://localhost:6379",
       AMQP: &config.AMQPConfig{
           Exchange:     "tasks",
           ExchangeType: "direct",
           BindingKey:   "tasks",
           PrefetchCount: 3,  // 限制未ACK数量
       },
   }
   ```

2. **监控STARTED状态的任务**
   ```go
   // 定期扫描超时任务
   func monitorZombieTasks() {
       for {
           time.Sleep(1 * time.Minute)
           tasks := backend.GetStartedTasks()
           for _, task := range tasks {
               if time.Since(task.StartTime) > 5*time.Minute {
                   log.Printf("检测到僵尸任务: %s", task.UUID)
                   // 重新入队或告警
               }
           }
       }
   }
   ```

3. **确保任务幂等性**
   ```go
   func processTask(args ...interface{}) error {
       taskID := args[0].(string)
       
       // 检查是否已处理
       if redis.Exists(fmt.Sprintf("processed:%s", taskID)) {
           return nil  // 已处理，跳过
       }
       
       // 执行业务逻辑
       // ...
       
       // 标记已处理
       redis.Set(fmt.Sprintf("processed:%s", taskID), "1", 24*time.Hour)
       return nil
   }
   ```

### 5.2 如果使用asynq

**最佳实践**:

1. **配置合理的租约时间**
   ```go
   srv := asynq.NewServer(
       redisOpt,
       asynq.Config{
           // 根据任务执行时间调整
           // 默认30秒，长任务可增加
           ShutdownTimeout: 30 * time.Second,
       },
   )
   ```

2. **设置合理的重试策略**
   ```go
   client.Enqueue(
       task,
       asynq.MaxRetry(5),
       asynq.Timeout(10*time.Minute),
   )
   ```

3. **利用Inspector监控**
   ```go
   inspector := asynq.NewInspector(redisOpt)
   
   // 检查异常队列
   qinfo, _ := inspector.GetQueueInfo("default")
   if qinfo.Archived > 100 {
       log.Printf("警告: 存档任务过多 (%d)", qinfo.Archived)
   }
   ```

4. **任务幂等性设计**（与machinery相同）
   ```go
   func handleTask(ctx context.Context, t *asynq.Task) error {
       taskID, _ := asynq.GetTaskID(ctx)
       
       // 去重检查
       if redis.Exists("processed:" + taskID) {
           return nil
       }
       
       // 执行业务逻辑
       // ...
       
       redis.Set("processed:"+taskID, "1", 24*time.Hour)
       return nil
   }
   ```

---

## 六、总结

### 你的疑问的答案

**Q: go-machinery不能保证at-least-once吗？**

**A: 能，但有条件限制：**
- ✅ 使用**AMQP Broker（RabbitMQ）**时，可以保证at-least-once（需正确配置）
- ❌ 使用**Redis Broker**时，**无法可靠保证**（任务可能丢失）
- ⚠️ 即使使用AMQP，也需要额外的监控和对账机制

**Q: 为什么说asynq可靠性更高？**

**A: 因为asynq的可靠性是"内建"的，而machinery是"外包"的：**

| 对比点 | go-machinery | asynq |
|--------|--------------|-------|
| **可靠性来源** | 依赖Broker实现 | 框架自身保证 |
| **复杂度** | 高（需理解Broker机制） | 低（开箱即用） |
| **一致性** | 低（Broker行为不一致） | 高（统一Redis后端） |
| **恢复机制** | 被动（Broker决定） | 主动（租约巡检） |
| **可观测性** | 弱（需自建） | 强（内置工具） |
| **生产就绪度** | 需要额外工程 | 直接可用 |

### 最终建议

**对于本NSP项目**:
- 当前使用machinery + Redis Broker，**可靠性存在风险**
- 建议切换到**asynq**，获得更强的可靠性保证和运维体验
- 迁移成本可接受（2-3天），长期收益显著

**如果必须使用machinery**:
- 立即切换到**AMQP Broker（RabbitMQ）**
- 实施任务监控系统
- 确保所有任务幂等性

---

**文档编写**: Qoder AI  
**技术参考**: asynq源码、machinery文档、Redis官方文档