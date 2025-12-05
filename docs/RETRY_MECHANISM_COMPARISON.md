# 任务重试机制深度剖析：go-machinery vs asynq

## 文档信息
- **生成日期**: 2025-11-26
- **核心问题**: go-machinery的retry机制能否解决Worker崩溃问题？
- **结论**: **不能！** retry机制只处理Handler错误，无法处理Worker崩溃

---

## 一、你的疑问非常关键！

### 问题本质

**你的理解**：
> go-machinery有任务失败重试机制（RetryCount），那应该能处理Worker崩溃导致的任务失败吧？

**实际情况**：
> ❌ **完全不能！** retry机制和崩溃恢复是**两个完全不同的场景**

---

## 二、核心区别：Handler错误 vs Worker崩溃

### 2.1 两种完全不同的失败场景

| 失败场景 | 时间点 | 任务状态 | 能否触发retry？ |
|---------|--------|---------|---------------|
| **Handler错误** | Handler执行过程中返回error | 任务仍在Worker控制下 | ✅ 能 |
| **Worker崩溃** | 任何时刻进程被kill | 任务失去控制 | ❌ **不能** |

**关键差异图示**:

```
场景1: Handler返回错误（retry能处理）
┌─────────────────────────────────────────────────┐
│  Worker进程运行中                                │
│  1. 从队列取出任务                               │
│  2. Backend更新状态: STARTED                     │
│  3. 调用Handler处理任务                          │
│  4. Handler返回error ← 错误发生在这里            │
│  5. Worker捕获错误                               │
│  6. Backend更新状态: FAILURE                     │
│  7. 检查RetryCount，如果还有重试次数             │
│  8. 重新发送任务到队列（延迟发送）                │
│  ✅ retry机制正常工作                            │
└─────────────────────────────────────────────────┘

场景2: Worker进程崩溃（retry完全无效）
┌─────────────────────────────────────────────────┐
│  Worker进程运行中                                │
│  1. 从队列取出任务（使用BRPOP，任务已从Redis删除）│
│  2. Backend更新状态: STARTED                     │
│  3. 调用Handler处理任务                          │
│  4. Worker进程crash（OOM/panic/kill -9） ← 崩溃  │
│  ❌ 第5步永远不会执行                            │
│  ❌ 第6步永远不会执行                            │
│  ❌ 第7步永远不会执行                            │
│  ❌ 第8步永远不会执行                            │
│  ❌ 任务已从队列删除，无法恢复                    │
│  ❌ Backend中状态永远卡在STARTED                 │
└─────────────────────────────────────────────────┘
```

---

### 2.2 详细代码分析

#### **go-machinery的retry机制（仅处理Handler错误）**

```go
// machinery Worker处理任务的核心逻辑（简化版）
func (worker *Worker) Process(signature *tasks.Signature) error {
    // 1. 更新状态为STARTED
    backend.SetState(&tasks.TaskState{
        State:    tasks.StateStarted,
        TaskUUID: signature.UUID,
    })
    
    // 2. 执行Handler
    results, err := task.Call(signature.Args)
    
    // 3. 如果Handler返回错误
    if err != nil {
        // 4. 更新状态为FAILURE
        backend.SetState(&tasks.TaskState{
            State:    tasks.StateFailure,
            TaskUUID: signature.UUID,
            Error:    err.Error(),
        })
        
        // 5. 检查是否需要重试
        if signature.RetryCount > 0 {
            // 6. 减少重试次数
            signature.RetryCount--
            
            // 7. 计算重试延迟（Fibonacci序列）
            delay := calculateRetryDelay(signature.RetryTimeout)
            
            // 8. 重新发送任务到队列（延迟任务）
            signature.ETA = time.Now().Add(delay)
            broker.Publish(signature)
            
            log.Printf("任务将在 %v 后重试", delay)
            return nil  // 重试已安排
        }
        
        log.Printf("任务失败且重试次数耗尽")
        return err
    }
    
    // 9. 成功，更新状态为SUCCESS
    backend.SetState(&tasks.TaskState{
        State:    tasks.StateSuccess,
        TaskUUID: signature.UUID,
        Results:  results,
    })
    
    return nil
}
```

**关键观察**:
- ✅ 第3步的`if err != nil`能捕获Handler的错误
- ✅ 第5-8步会安排重试
- ❌ 但如果Worker在**第2步之后任何时刻崩溃**，第3-9步都不会执行
- ❌ 任务已经在第1步之前从队列删除（BRPOP）

---

#### **Worker崩溃时的实际情况**

**使用Redis Broker时的消费逻辑**:

```go
// machinery Redis Broker消费任务（简化版）
func (b *RedisBroker) StartConsuming(consumerTag string, taskProcessor TaskProcessor) {
    for {
        // BRPOP是破坏性操作！
        // 任务从队列取出后就立即删除了
        taskBytes := redis.BRPop(queueName, 0)
        
        // ❌ 此时任务已从Redis删除！
        // 如果Worker在这里之后崩溃，任务永久丢失
        
        var signature tasks.Signature
        json.Unmarshal(taskBytes, &signature)
        
        // 交给Worker处理
        go taskProcessor.Process(&signature)
        
        // ❌ 如果上面的goroutine在Process中崩溃
        // 没有任何机制能恢复这个任务
    }
}
```

**问题根源**:
1. Redis List的`BRPOP`是**破坏性读取**（pop = 弹出并删除）
2. 任务从队列删除 → Worker处理 → 更新Backend状态，这三步**不是原子操作**
3. 在步骤2崩溃时，任务已经丢失

---

#### **对比：asynq如何解决这个问题**

```go
// asynq Worker处理任务（简化版）
func (p *processor) exec() {
    for {
        // 1. 从pending取出任务，同时移动到active（原子操作）
        msg := p.broker.Dequeue(queueNames...)
        
        // 2. 使用Lua脚本保证原子性：
        //    - 从pending队列LPOP
        //    - 同时ZADD到active集合（带过期时间）
        
        // 3. 设置租约（30秒）
        lease := &lease{
            msgID:  msg.ID,
            expiry: time.Now().Add(30 * time.Second),
        }
        
        // 4. 启动心跳goroutine
        go p.heartbeat(lease)  // 每8秒延长租约
        
        // 5. 处理任务
        err := handler.ProcessTask(ctx, msg)
        
        // 6. 处理成功，从active删除
        if err == nil {
            p.broker.Done(msg)  // 从Redis彻底删除
        } else {
            // 7. 处理失败，移到retry队列
            p.broker.Retry(msg)
        }
        
        // ❌ 如果Worker在第5步崩溃会怎样？
        // ✅ 任务仍在active集合中
        // ✅ 心跳停止，租约在30秒后到期
        // ✅ Recoverer扫描发现过期，移回pending
        // ✅ 其他Worker重新处理
    }
}

// Recoverer后台扫描
func (r *recoverer) recoverLeaseExpired() {
    cutoff := time.Now().Add(-30 * time.Second)
    
    // 查找所有30秒前过期的任务
    expiredTasks := redis.ZRangeByScore("active", 0, cutoff)
    
    for _, task := range expiredTasks {
        // 原子操作：从active移除，推入pending
        redis.Eval(`
            redis.call('ZREM', 'active', task_id)
            redis.call('LPUSH', 'pending', task_data)
        `)
        
        log.Printf("恢复任务: %s (Worker崩溃)", task.ID)
    }
}
```

**关键差异**:
- ✅ asynq：任务从pending → active，始终在Redis中
- ❌ machinery：任务BRPOP删除，不在Redis中

---

## 三、实际场景对比

### 场景：Worker在处理任务时被kill -9

#### **go-machinery的表现（Redis Broker）**

```
时间轴：
10:00:00 - Worker从Redis队列BRPOP获取task1
         - task1从Redis永久删除 ❌
         - Backend更新task1状态：STARTED

10:00:05 - Worker正在执行Handler
10:00:10 - kill -9 杀掉Worker进程 💥

10:00:11 - Worker已死，无法执行任何代码
         - task1的状态永远卡在STARTED ❌
         - retry机制根本没机会执行 ❌
         - 任务永久丢失 ❌

10:05:00 - 其他Worker正常运行，但task1永远不会被处理
         - Backend中显示task1 = STARTED（5分钟前）
         - 需要人工介入或额外的监控系统 ⚠️
```

**结果**：
- ❌ 任务丢失
- ❌ retry机制无效（因为Worker已死，无法执行retry代码）
- ⚠️ 需要额外实现"僵尸任务监控系统"

---

#### **go-machinery的表现（AMQP Broker）**

```
时间轴：
10:00:00 - Worker从RabbitMQ接收task1（未ACK）
         - task1仍在RabbitMQ中 ✅
         - Backend更新task1状态：STARTED

10:00:05 - Worker正在执行Handler
10:00:10 - kill -9 杀掉Worker进程 💥

10:00:11 - RabbitMQ检测到连接断开
         - task1自动重新入队（未ACK的消息） ✅
         - 其他Worker可以接收task1 ✅

10:00:15 - Worker2接收task1，重新处理
         - Backend更新task1状态：STARTED（第二次）
         - 如果成功，更新为SUCCESS ✅

但是：
         - Backend中有两条STARTED记录（状态不一致）⚠️
         - 如果Worker2也崩溃，继续重新入队
         - 但RabbitMQ的重试是无限的，可能导致死循环 ⚠️
```

**结果**：
- ✅ 任务不丢失（RabbitMQ保证）
- ⚠️ 但需要正确配置AMQP参数（manual ACK、prefetch等）
- ⚠️ Backend状态可能不一致

---

#### **asynq的表现**

```
时间轴：
10:00:00 - Worker从pending获取task1
         - 原子操作：LPOP pending + ZADD active(expiry=10:00:30)
         - task1移到active集合，带30秒租约 ✅

10:00:08 - Heartbeater延长租约：expiry=10:00:38 ✅
10:00:10 - kill -9 杀掉Worker进程 💥

10:00:11 - Worker已死，心跳停止
         - task1仍在active集合中 ✅
         - 租约到期时间：10:00:38

10:00:38 - 租约到期
10:01:00 - Recoverer定期扫描（每分钟）
         - 发现task1过期（10:00:38 < 10:01:00）
         - 原子操作：ZREM active + LPUSH pending ✅
         - task1回到pending队列 ✅

10:01:05 - Worker2从pending获取task1
         - 重新处理，设置新的租约 ✅
```

**结果**：
- ✅ 任务不丢失
- ✅ 自动恢复（无需人工干预）
- ✅ 状态一致（Redis单一数据源）

---

## 四、为什么go-machinery的retry机制无法处理崩溃？

### 4.1 retry机制的触发条件

go-machinery的retry**只在这些情况下触发**：

```go
// machinery的retry触发条件
func (worker *Worker) Process(signature *tasks.Signature) error {
    results, err := task.Call(signature.Args)
    
    // ✅ 情况1：Handler明确返回error
    if err != nil {
        return worker.retryTask(signature, err)
    }
    
    // ✅ 情况2：Handler内部panic（如果有recover）
    defer func() {
        if r := recover(); r != nil {
            worker.retryTask(signature, fmt.Errorf("panic: %v", r))
        }
    }()
    
    // ❌ 情况3：Worker进程崩溃
    // 根本执行不到这里！
}
```

**关键点**：
- retry代码必须在**Worker进程存活**的前提下才能执行
- Worker崩溃 = 进程终止 = 所有代码停止执行
- 因此retry机制**完全失效**

---

### 4.2 retry vs 崩溃恢复的本质区别

| 维度 | retry机制 | 崩溃恢复机制 |
|------|----------|------------|
| **触发条件** | Handler返回error | Worker进程死亡 |
| **执行者** | **当前Worker** | **其他Worker或外部系统** |
| **依赖条件** | Worker进程存活 | Worker进程已死 |
| **实现方式** | 应用层逻辑 | 基础设施层机制 |
| **配置位置** | Signature.RetryCount | Broker特性或框架设计 |

**核心差异**：
- **retry**：Worker自己说"我处理失败了，稍后再试"
- **崩溃恢复**：外部系统说"这个Worker死了，我来接管它的任务"

---

### 4.3 go-machinery缺少的机制

**问题根源**：

```
go-machinery依赖两层：
1. Application层：retry逻辑（Worker代码）
2. Broker层：消息持久化（Redis/AMQP）

崩溃恢复需要第三层：
3. Infrastructure层：外部监控和恢复机制

go-machinery只实现了1和2，没有3！
```

**具体缺失**：

1. **缺少任务租约机制**
   - asynq有租约，Worker定期续约
   - machinery没有，无法判断Worker是否存活

2. **缺少主动恢复机制**
   - asynq有Recoverer后台扫描
   - machinery依赖Broker被动重新入队

3. **缺少状态一致性保证**
   - asynq任务状态在Redis中原子更新
   - machinery Backend异步更新，有不一致窗口

---

## 五、总结：retry ≠ 崩溃恢复

### 5.1 核心结论

**你的理解需要纠正的地方**：

| 你可能的理解 | 实际情况 |
|------------|---------|
| "retry能处理所有失败" | ❌ 只能处理Handler错误 |
| "Worker崩溃会触发retry" | ❌ Worker已死，无法执行retry代码 |
| "配置RetryCount就安全了" | ❌ 对崩溃场景无效 |

**正确的理解**：

```
失败类型1：Handler错误（应用层）
├─ 场景：网络超时、业务逻辑错误
├─ 触发：Handler返回error
├─ 处理：retry机制
└─ go-machinery：✅ 支持

失败类型2：Worker崩溃（基础设施层）
├─ 场景：OOM、panic、kill -9
├─ 触发：进程死亡
├─ 处理：外部恢复机制
└─ go-machinery：
    ├─ Redis Broker：❌ 不支持（任务丢失）
    └─ AMQP Broker：⚠️ 部分支持（需配置）

asynq：✅ 两种都支持
```

---

### 5.2 实际建议

#### **如果使用go-machinery**

**必须采取的额外措施**：

1. **使用AMQP Broker（RabbitMQ）**
   ```go
   cnf := &config.Config{
       Broker: "amqp://user:pass@host:5672/",
       AMQP: &config.AMQPConfig{
           Exchange:      "tasks",
           ExchangeType:  "direct",
           BindingKey:    "tasks",
           PrefetchCount: 3,  // 限制未ACK数量
       },
   }
   ```

2. **实现僵尸任务监控系统**
   ```go
   // 定期扫描STARTED状态的任务
   func monitorZombieTasks() {
       ticker := time.NewTicker(1 * time.Minute)
       for range ticker.C {
           tasks := backend.GetTasksByState(tasks.StateStarted)
           for _, task := range tasks {
               if time.Since(task.StartTime) > 5*time.Minute {
                   log.Printf("僵尸任务: %s，准备重试", task.UUID)
                   
                   // 重新发送到队列
                   signature := reconstructSignature(task)
                   broker.Publish(signature)
               }
           }
       }
   }
   ```

3. **确保任务幂等性**（因为可能重复执行）

4. **监控Backend状态不一致**

---

#### **如果使用asynq**

**无需额外措施**：

```go
// 就这么简单！
srv := asynq.NewServer(
    asynq.RedisClientOpt{Addr: "localhost:6379"},
    asynq.Config{Concurrency: 10},
)

mux := asynq.NewServeMux()
mux.HandleFunc("task:process", handleTask)

srv.Run(mux)  // 崩溃恢复自动启用
```

**asynq自动提供**：
- ✅ 租约机制
- ✅ Recoverer后台扫描
- ✅ 原子状态转换
- ✅ 零配置

---

### 5.3 最终答案

**Q: go-machinery的retry机制能否解决Worker崩溃问题？**

**A: 完全不能！**

**原因**：
1. retry是**应用层**的错误处理，只处理Handler返回的error
2. Worker崩溃是**进程层**的故障，retry代码根本无法执行
3. Redis Broker使用BRPOP破坏性读取，任务崩溃前已删除
4. 需要**基础设施层**的恢复机制（租约+外部扫描）

**对比**：
- **go-machinery**: retry ≠ 崩溃恢复，两者是独立的
- **asynq**: 同时提供retry（Handler错误）和崩溃恢复（Worker死亡）

这就是为什么在可靠性方面，asynq的设计明显优于go-machinery！

---

**文档编写**: Qoder AI  
**参考**: go-machinery文档、asynq源码、Redis文档
