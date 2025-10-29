# Machinery 任务编排完整指南

## 📚 目录
1. [基本概念](#基本概念)
2. [四种编排方式](#四种编排方式)
3. [在VPC项目中的应用](#在vpc项目中的应用)
4. [最佳实践](#最佳实践)

---

## 基本概念

### 任务 (Task/Signature)
一个可执行的工作单元，包含：
- `Name`: 任务名称（需要在Worker中注册）
- `Args`: 输入参数
- `OnSuccess`: 成功后执行的任务
- `OnError`: 失败后执行的任务

```go
task := &tasks.Signature{
    Name: "create_vrf_on_switch",
    Args: []tasks.Arg{
        {Type: "string", Value: "input_data"},
    },
}
```

---

## 四种编排方式

### 1️⃣ 独立任务 (SendTask)

**适用场景**: 任务之间完全独立，没有依赖关系

**执行方式**: 并行，无顺序保证

**代码示例**:
```go
task1 := &tasks.Signature{Name: "task1"}
task2 := &tasks.Signature{Name: "task2"}
task3 := &tasks.Signature{Name: "task3"}

server.SendTask(task1)
server.SendTask(task2)
server.SendTask(task3)
```

**执行流程图**:
```
Task1 ───┐
         ├──> 并行执行
Task2 ───┤
         │
Task3 ───┘
```

**优点**:
- ✅ 简单直接
- ✅ 执行效率高（并行）
- ✅ 互不影响

**缺点**:
- ❌ 无法保证执行顺序
- ❌ 无法确保某个任务在另一个任务之后执行

**使用示例**:
```bash
curl -X POST http://localhost:8080/api/v1/vpc/independent \
  -H "Content-Type: application/json" \
  -d '{"vpc_name": "test", "vrf_name": "VRF-1", "vlan_id": 100, "firewall_zone": "zone1"}'
```

---

### 2️⃣ 任务链 (Chain)

**适用场景**: 任务必须按严格顺序执行，且需要传递结果

**执行方式**: 串行，Task1 → Task2 → Task3

**代码示例**:
```go
task1 := &tasks.Signature{
    Name: "task1",
    Args: []tasks.Arg{{Type: "string", Value: "initial_input"}},
}
task2 := &tasks.Signature{Name: "task2"} // 会接收task1的返回值
task3 := &tasks.Signature{Name: "task3"} // 会接收task2的返回值

chain, _ := tasks.NewChain(task1, task2, task3)
server.SendChain(chain)
```

**执行流程图**:
```
Task1 ──> Task2 ──> Task3
 (输入)    (接收Task1结果)  (接收Task2结果)
```

**优点**:
- ✅ 严格顺序控制
- ✅ 结果自动传递
- ✅ 适合有依赖的流程

**缺点**:
- ❌ 串行执行，耗时较长
- ❌ 需要任务函数签名兼容（接收前一个任务的返回值）
- ❌ 如果参数不匹配会报错: `reflect: Call with too many input arguments`

**任务函数要求**:
```go
// Chain中的任务函数需要接收前一个任务的结果
func Task2(prevResult string) (string, error) {
    // prevResult 是 Task1 的返回值
    return "result2", nil
}
```

---

### 3️⃣ 任务组 (Group)

**适用场景**: 多个任务可以并行执行，需要等待所有任务完成

**执行方式**: 并行

**代码示例**:
```go
task1 := &tasks.Signature{Name: "task1"}
task2 := &tasks.Signature{Name: "task2"}
task3 := &tasks.Signature{Name: "task3"}

group := tasks.NewGroup(task1, task2, task3)
asyncResults, _ := server.SendGroup(group, 0) // 0=不限制并发

// 等待所有任务完成
for _, result := range asyncResults {
    result.Get(time.Second * 10)
}
```

**执行流程图**:
```
     ┌──> Task1 ──┐
     │            │
Start├──> Task2 ──┤──> 所有完成
     │            │
     └──> Task3 ──┘
```

**优点**:
- ✅ 并行执行，效率高
- ✅ 可以等待所有任务完成
- ✅ 可以获取所有任务的结果

**缺点**:
- ❌ 如果需要同步等待，会阻塞
- ❌ 没有后续处理机制

**使用示例**:
```bash
curl -X POST http://localhost:8080/api/v1/vpc/group \
  -H "Content-Type: application/json" \
  -d '{"vpc_name": "test", "vrf_name": "VRF-1", "vlan_id": 100, "firewall_zone": "zone1"}'
```

---

### 4️⃣ Chord（协同任务）⭐ 推荐

**适用场景**: 先并行执行多个任务，全部完成后执行汇总任务

**执行方式**: Group（并行） → Callback（汇总）

**代码示例**:
```go
// 第一阶段：并行任务
task1 := &tasks.Signature{Name: "task1"}
task2 := &tasks.Signature{Name: "task2"}

// 第二阶段：回调任务（在所有任务完成后执行）
callback := &tasks.Signature{Name: "aggregate_task"}

group := tasks.NewGroup(task1, task2)
chord, _ := tasks.NewChord(group, callback)
server.SendChord(chord, 0)
```

**执行流程图**:
```
     ┌──> Task1 ──┐
     │            │
Start├──> Task2 ──┤──> Callback Task
     │            │
     └──> Task3 ──┘
     
     (并行阶段)    (汇总阶段)
```

**优点**:
- ✅ 结合了并行和顺序的优点
- ✅ 适合"分-汇"模式（MapReduce）
- ✅ 回调任务可以处理所有并行任务的结果
- ✅ 执行效率高

**缺点**:
- ❌ 相对复杂
- ❌ 回调任务需要能处理多个结果

**使用示例**:
```bash
curl -X POST http://localhost:8080/api/v1/vpc/chord \
  -H "Content-Type: application/json" \
  -d '{"vpc_name": "test", "vrf_name": "VRF-1", "vlan_id": 100, "firewall_zone": "zone1"}'
```

---

## 在VPC项目中的应用

### 场景分析

我们的VPC创建包含三个任务：
1. **创建VRF** (在交换机上)
2. **创建VLAN子接口** (在交换机上)
3. **创建防火墙安全区域** (在防火墙上)

### 依赖关系分析

```
场景1: 完全独立
VRF、VLAN、Firewall 三者互不依赖
→ 使用: 独立任务 或 Group

场景2: 有顺序要求
必须先创建VRF，然后VLAN，最后Firewall
→ 使用: Chain

场景3: 部分依赖（推荐）
VRF和VLAN必须创建完成后，才能配置Firewall
但VRF和VLAN可以并行
→ 使用: Chord ⭐
```

### 推荐方案: Chord 模式

**理由**:
1. ✅ VRF和VLAN都是交换机配置，可以并行提高效率
2. ✅ 防火墙配置依赖于网络已就绪，应该在VRF和VLAN完成后执行
3. ✅ 即使将来增加更多交换机任务，也能轻松扩展
4. ✅ 符合真实的网络配置顺序

**实现代码**: 参见 [api/server_advanced.go](api/server_advanced.go) 的 `createVPCWithChord` 方法

**执行流程**:
```
          ┌──> 创建VRF ──┐
API请求 ──┤              │──> 创建防火墙区域 ──> 完成
          └──> 创建VLAN ─┘
          
          (并行，2秒)      (单独执行，2秒)
          
总耗时: ~4秒
对比独立任务: ~2秒（但无顺序保证）
对比Chain: ~6秒（完全串行）
```

---

## 编排方式对比表

| 特性 | 独立任务 | Chain | Group | Chord |
|-----|---------|-------|-------|-------|
| **执行方式** | 并行 | 串行 | 并行 | 并行+汇总 |
| **顺序保证** | ❌ | ✅ | ❌ | 部分✅ |
| **结果传递** | ❌ | ✅ | ❌ | ✅ (到回调) |
| **执行效率** | 高 | 低 | 高 | 中高 |
| **复杂度** | 简单 | 简单 | 简单 | 中等 |
| **适用场景** | 完全独立 | 严格顺序 | 并行+等待 | 分-汇模式 |
| **VPC耗时** | ~2秒 | ~6秒 | ~2秒 | ~4秒 |

---

## 最佳实践

### 1. 选择合适的编排方式

```go
// ✅ 好的做法：根据业务需求选择
if 任务完全独立 {
    使用 SendTask 或 Group
} else if 任务有严格顺序 && 需要传递结果 {
    使用 Chain
} else if 部分任务并行 && 有汇总需求 {
    使用 Chord  // ⭐ VPC场景推荐
}
```

### 2. 处理Chain的参数传递

```go
// ❌ 错误：任务函数签名不匹配
func Task1() (string, error) { return "result", nil }
func Task2(input string) (string, error) { ... } // Chain会传入Task1的结果

// ✅ 正确：确保任务函数能接收前一个任务的结果
func Task1(input string) (string, error) { return input, nil }
func Task2(prevResult string) (string, error) { return prevResult, nil }
```

### 3. 设置合理的超时

```go
// ✅ 设置任务超时
task := &tasks.Signature{
    Name: "long_running_task",
    RetryCount: 3,          // 重试3次
    RetryTimeout: 10,       // 每次重试间隔10秒
}
```

### 4. 使用回调处理成功/失败

```go
task := &tasks.Signature{
    Name: "create_vpc",
    OnSuccess: []*tasks.Signature{
        {Name: "send_success_notification"},
    },
    OnError: []*tasks.Signature{
        {Name: "send_error_alert"},
        {Name: "rollback_changes"},
    },
}
```

### 5. 监控任务状态

```go
// 发送任务并获取结果
asyncResult, _ := server.SendTask(task)

// 异步获取结果
go func() {
    result, err := asyncResult.Get(time.Second * 30)
    if err != nil {
        log.Printf("任务失败: %v", err)
    } else {
        log.Printf("任务成功: %v", result)
    }
}()
```

---

## 实战演练

### 测试不同的编排方式

```bash
# 1. 独立任务模式（当前使用）
curl -X POST http://localhost:8080/api/v1/vpc/independent \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-independent",
    "vrf_name": "VRF-1",
    "vlan_id": 100,
    "firewall_zone": "zone1"
  }'

# 2. Chord模式（推荐）
curl -X POST http://localhost:8080/api/v1/vpc/chord \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-chord",
    "vrf_name": "VRF-2",
    "vlan_id": 200,
    "firewall_zone": "zone2"
  }'

# 3. Group模式
curl -X POST http://localhost:8080/api/v1/vpc/group \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-group",
    "vrf_name": "VRF-3",
    "vlan_id": 300,
    "firewall_zone": "zone3"
  }'
```

### 查看执行日志

```bash
# 观察不同模式的执行顺序
tail -f logs/switch_worker.log logs/firewall_worker.log
```

---

## 总结

### VPC场景的最佳选择: Chord ⭐

**推荐使用 Chord 模式的原因**:

1. **性能优化**: VRF和VLAN并行创建，节省时间
2. **逻辑合理**: 防火墙配置确实应该在网络配置完成后执行
3. **易于扩展**: 未来增加更多网络配置任务，只需加入Group
4. **结构清晰**: (网络配置 || ...) → 安全配置

**代码位置**: 
- 示例实现: [api/server_advanced.go](api/server_advanced.go)
- 详细示例: [examples/task_orchestration.go](examples/task_orchestration.go)

**下一步**:
可以考虑将当前的 `api/server.go` 升级为使用 Chord 模式，获得更好的性能和更清晰的任务依赖关系！
