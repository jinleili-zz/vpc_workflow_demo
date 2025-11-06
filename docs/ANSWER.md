# Machinery 任务编排完整解答

## 📋 你的问题
> machinery如何管理任务的顺序？或者处理任务的依赖关系

---

## 🎯 简答

Machinery 提供 **4种方式** 来管理任务顺序和依赖关系：

1. **SendTask** - 独立任务（无序）
2. **Chain** - 任务链（严格顺序）
3. **Group** - 任务组（并行+可等待）
4. **Chord** - 协同任务（并行+汇总）⭐ **推荐**

此外，Machinery 还支持 **任务优先级** 调度，允许为不同重要性的任务设置 0-9 级别的优先级。

---

## 📚 详细说明

### 1️⃣ SendTask - 独立任务
**适用**: 任务完全独立，无依赖

```go
server.SendTask(task1)
server.SendTask(task2)
server.SendTask(task3)
```

**特点**:
- ⚡ 并行执行，速度最快
- ❌ 无法保证顺序
- 📊 适合日志记录、通知等独立任务

**流程**:
```
Task1 ──┐
Task2 ──┤──> 并行执行
Task3 ──┘
```

---

### 2️⃣ Chain - 任务链
**适用**: 任务必须按严格顺序执行

```go
task1 := &tasks.Signature{
    Name: "task1",
    Args: []tasks.Arg{{Type: "string", Value: "input"}},
}
task2 := &tasks.Signature{Name: "task2"} // 接收task1结果
task3 := &tasks.Signature{Name: "task3"} // 接收task2结果

chain, _ := tasks.NewChain(task1, task2, task3)
server.SendChain(chain)
```

**特点**:
- ✅ 严格顺序执行
- ✅ 自动传递结果（前一个任务的返回值传给下一个）
- ⚠️ 需要任务函数签名兼容
- 🐌 串行执行，速度较慢

**流程**:
```
Task1 ──> Task2 ──> Task3
(输入)   (接收结果)  (接收结果)
```

**常见问题**: `reflect: Call with too many input arguments`
- **原因**: 任务函数不接受参数，但Chain会传递上个任务的结果
- **解决**: 修改任务函数签名以接收参数

---

### 3️⃣ Group - 任务组
**适用**: 多个任务并行执行，需要等待所有完成

```go
task1 := &tasks.Signature{Name: "task1"}
task2 := &tasks.Signature{Name: "task2"}
task3 := &tasks.Signature{Name: "task3"}

group := tasks.NewGroup(task1, task2, task3)
asyncResults, _ := server.SendGroup(group, 0) // 0=不限制并发

// 可选: 等待所有任务完成
for _, result := range asyncResults {
    result.Get(time.Second * 10)
}
```

**特点**:
- ⚡ 并行执行，效率高
- ✅ 可以等待所有任务完成
- ✅ 可以获取所有任务的结果
- ❌ 没有后续汇总机制

**流程**:
```
     ┌──> Task1 ──┐
     │            │
Start├──> Task2 ──┤──> 等待全部完成
     │            │
     └──> Task3 ──┘
```

---

### 4️⃣ Chord - 协同任务 ⭐ **推荐**
**适用**: 先并行执行多个任务，全部完成后执行汇总任务

```go
// 第一阶段: 并行任务
task1 := &tasks.Signature{Name: "create_vrf"}
task2 := &tasks.Signature{Name: "create_vlan"}

// 第二阶段: 回调任务（在所有并行任务完成后执行）
callback := &tasks.Signature{Name: "create_firewall"}

group := tasks.NewGroup(task1, task2)
chord, _ := tasks.NewChord(group, callback)
server.SendChord(chord, 0)
```

**特点**:
- ⚡ 并行执行前置任务，效率高
- ✅ 保证回调在所有任务完成后执行
- ✅ 适合"分-汇"模式（MapReduce）
- 🎯 **性能和顺序的完美平衡**

**流程**:
```
     ┌──> Task1 ──┐
     │            │
Start├──> Task2 ──┤──> Callback Task ──> 完成
     │            │
     └──> Task3 ──┘
     
     (并行阶段)    (汇总阶段)
```

---

## 🎯 VPC 项目应用

### 场景分析
我们的VPC创建包含:
1. 创建VRF (交换机)
2. 创建VLAN子接口 (交换机)
3. 创建防火墙安全区域 (防火墙)

### 依赖关系
```
实际需求: 
- VRF 和 VLAN 是交换机配置，可以并行
- 防火墙配置应该在网络配置完成后执行
```

### 当前实现 (SendTask)
```go
// 问题: 三个任务独立发送，无顺序保证
server.SendTask(&task_vrf)       // 可能在任意时间执行
server.SendTask(&task_vlan)      // 可能在任意时间执行
server.SendTask(&task_firewall)  // 可能在VRF/VLAN之前执行 ❌
```

**问题**:
- ❌ 防火墙可能在网络未就绪时配置
- ❌ 没有利用并行优势（虽然实际也是并行的，但没有逻辑控制）

### 推荐实现 (Chord) ⭐
```go
// VRF 和 VLAN 并行执行
task_vrf := &tasks.Signature{Name: "create_vrf_on_switch", Args: ...}
task_vlan := &tasks.Signature{Name: "create_vlan_subinterface", Args: ...}

// 防火墙在网络配置完成后执行
task_firewall := &tasks.Signature{Name: "create_firewall_zone", Args: ...}

// 创建 Chord: (VRF || VLAN) → Firewall
group := tasks.NewGroup(task_vrf, task_vlan)
chord, _ := tasks.NewChord(group, task_firewall)
server.SendChord(chord, 0)
```

**优势**:
- ✅ VRF 和 VLAN 并行创建，节省时间
- ✅ 防火墙在网络就绪后配置，逻辑正确
- ✅ 性能优化: 4秒 vs 2秒（无序）vs 6秒（完全串行）
- ✅ 代码清晰，易于理解和维护

**执行时序**:
```
时间轴:  0s ---- 2s ---- 4s
        VRF    ████
        VLAN   ████
                    FW   ████
                             ↑
                      总耗时: ~4秒
```

---

## 📊 对比总结

| 方式 | 顺序 | 并行 | 传参 | 耗时 | VPC推荐度 |
|-----|------|------|------|------|----------|
| SendTask | ❌ | ✅ | ❌ | 2秒 | ⭐⭐⭐ |
| Chain | ✅ | ❌ | ✅ | 6秒 | ⭐⭐ |
| Group | ❌ | ✅ | ❌ | 2秒 | ⭐⭐⭐⭐ |
| **Chord** | **✅** | **✅** | **✅** | **4秒** | **⭐⭐⭐⭐⭐** |

---

## 🚀 实现指南

### 步骤1: 查看高级示例
已为你创建了完整示例:
- [api/server_advanced.go](../api/server_advanced.go) - 三种模式的API实现
- [examples/task_orchestration.go](../examples/task_orchestration.go) - 详细示例代码

### 步骤2: 升级当前API
可以将 `api/server.go` 的 `createVPC` 方法升级为使用 Chord:

```go
// 替换当前的三个 SendTask 调用为:
group := tasks.NewGroup(task_vrf, task_vlan)
chord, _ := tasks.NewChord(group, task_firewall)
server.SendChord(chord, 0)
```

### 步骤3: 测试验证
```bash
# 测试Chord模式
curl -X POST http://localhost:8080/api/v1/vpc/chord \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-chord",
    "vrf_name": "VRF-TEST",
    "vlan_id": 100,
    "firewall_zone": "zone1"
  }'

# 查看日志，验证执行顺序
tail -f logs/switch_worker.log logs/firewall_worker.log
```

---

## 📖 更多资源

已为你创建完整文档:

1. **[TASK_ORCHESTRATION.md](TASK_ORCHESTRATION.md)**
   - 四种方式的详细说明
   - 代码示例
   - 最佳实践

2. **[ORCHESTRATION_DIAGRAMS.md](ORCHESTRATION_DIAGRAMS.md)**
   - Mermaid流程图
   - 可视化对比
   - 决策树

3. **[QUICK_REFERENCE.md](QUICK_REFERENCE.md)**
   - 速查表
   - 常见问题
   - 一键参考

---

## 💡 总结

### 核心答案
Machinery 通过 **4种编排方式** 管理任务顺序和依赖:
1. **SendTask** - 无序
2. **Chain** - 严格顺序
3. **Group** - 并行可等待
4. **Chord** - 并行+汇总 ⭐

### VPC 场景最佳实践
使用 **Chord** 模式:
```go
// (VRF || VLAN) → Firewall
group := tasks.NewGroup(task_vrf, task_vlan)
chord, _ := tasks.NewChord(group, task_firewall)
server.SendChord(chord, 0)
```

### 核心优势
- ✅ 性能优化: 并行执行网络配置
- ✅ 逻辑正确: 防火墙在网络就绪后配置
- ✅ 易于维护: 代码清晰表达依赖关系
- ✅ 可扩展: 未来增加任务只需加入Group

---

**需要帮助实现吗？我可以帮你升级当前的代码！** 🚀
