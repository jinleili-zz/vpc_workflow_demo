# Machinery 任务编排速查表

## 🚀 四种编排方式对比

| 方式 | 代码 | 执行 | 时长 | 场景 |
|-----|------|------|------|------|
| **SendTask** | `server.SendTask(task)` | 并行 | 2s | 完全独立 |
| **Chain** | `server.SendChain(chain)` | 串行 | 6s | 严格顺序+传参 |
| **Group** | `server.SendGroup(group, 0)` | 并行 | 2s | 并行+等待 |
| **Chord** ⭐ | `server.SendChord(chord, 0)` | 并行+串行 | 4s | 分-汇模式 |

---

## 📝 代码速查

### 1. SendTask - 独立任务
```go
task1 := &tasks.Signature{Name: "task1"}
task2 := &tasks.Signature{Name: "task2"}

server.SendTask(task1)
server.SendTask(task2)
```
✅ 简单  ❌ 无序

---

### 2. Chain - 任务链
```go
task1 := &tasks.Signature{
    Name: "task1",
    Args: []tasks.Arg{{Type: "string", Value: "input"}},
}
task2 := &tasks.Signature{Name: "task2"} // 接收task1结果

chain, _ := tasks.NewChain(task1, task2)
server.SendChain(chain)
```
✅ 有序  ✅ 传参  ❌ 慢

⚠️ **注意**: 任务函数需要接收前一个任务的返回值！

---

### 3. Group - 任务组
```go
task1 := &tasks.Signature{Name: "task1"}
task2 := &tasks.Signature{Name: "task2"}

group := tasks.NewGroup(task1, task2)
results, _ := server.SendGroup(group, 0)

// 可选: 等待完成
for _, r := range results {
    r.Get(time.Second * 10)
}
```
✅ 并行  ✅ 可等待  ❌ 无序

---

### 4. Chord - 协同任务 ⭐
```go
// 并行任务
task1 := &tasks.Signature{Name: "task1"}
task2 := &tasks.Signature{Name: "task2"}

// 回调任务
callback := &tasks.Signature{Name: "callback"}

group := tasks.NewGroup(task1, task2)
chord, _ := tasks.NewChord(group, callback)
server.SendChord(chord, 0)
```
✅ 并行  ✅ 有序  ✅ 高效

**流程**: (task1 || task2) → callback

---

## 🎯 VPC 场景示例

### 当前方式 (SendTask)
```go
// 问题: 防火墙可能在VRF前创建
server.SendTask(&task_vrf)
server.SendTask(&task_vlan)
server.SendTask(&task_firewall)
```

### 推荐方式 (Chord) ⭐
```go
// 优势: VRF||VLAN并行, Firewall等待完成
group := tasks.NewGroup(task_vrf, task_vlan)
chord, _ := tasks.NewChord(group, task_firewall)
server.SendChord(chord, 0)
```

---

## 🔍 选择指南

```
是否需要顺序?
├─ 否
│  ├─ 需要等待? 
│  │  ├─ 是 → Group
│  │  └─ 否 → SendTask
│  └─ 
└─ 是
   ├─ 全部串行? 
   │  ├─ 是 → Chain
   │  └─ 否 → Chord ⭐
   └─
```

---

## ⚠️ 常见问题

### Q1: Chain 报错 "too many input arguments"
**原因**: 任务函数签名不匹配

**解决**:
```go
// ❌ 错误
func Task1() (string, error) { return "result", nil }
func Task2(input string) (string, error) { ... }

// ✅ 正确 - 确保能接收前一个任务的结果
func Task1(input string) (string, error) { return input, nil }
func Task2(prevResult string) (string, error) { return prevResult, nil }
```

### Q2: 如何获取任务结果?
```go
asyncResult, _ := server.SendTask(task)

// 同步等待
result, err := asyncResult.Get(time.Second * 30)

// 异步获取
go func() {
    result, err := asyncResult.Get(time.Second * 30)
    log.Printf("结果: %v, 错误: %v", result, err)
}()
```

### Q3: 如何设置回调?
```go
task := &tasks.Signature{
    Name: "main_task",
    OnSuccess: []*tasks.Signature{
        {Name: "success_callback"},
    },
    OnError: []*tasks.Signature{
        {Name: "error_handler"},
    },
}
```

---

## 📊 性能对比 (VPC场景)

| 方式 | VRF | VLAN | Firewall | 总计 |
|-----|-----|------|----------|------|
| SendTask | 0-2s | 0-2s | 0-2s | **~2s** |
| Chain | 0-2s | 2-4s | 4-6s | **~6s** |
| Group | 0-2s | 0-2s | 0-2s | **~2s** |
| **Chord** | **0-2s** | **0-2s** | **2-4s** | **~4s** |

---

## 🎓 最佳实践

1. **默认使用 Chord**: 性能和顺序的最佳平衡
2. **避免过长的 Chain**: 超过3个任务考虑拆分
3. **设置合理超时**: 防止任务永久挂起
4. **使用回调**: 处理成功/失败情况
5. **监控任务**: 记录日志和指标

---

## 🔗 相关文档

- [完整指南](TASK_ORCHESTRATION.md)
- [流程图](ORCHESTRATION_DIAGRAMS.md)
- [示例代码](../examples/task_orchestration.go)
- [高级API](../api/server_advanced.go)

---

## 💡 一句话总结

> **VPC场景用 Chord: `(VRF || VLAN) → Firewall` 性能好、有顺序、易维护！** ⭐
