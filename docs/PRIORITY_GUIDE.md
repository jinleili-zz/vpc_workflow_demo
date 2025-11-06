# go-machinery 任务优先级使用指南

## 🎯 概述

go-machinery 支持任务优先级调度，允许您为不同重要性的任务设置执行优先级。优先级范围为 0-9，其中 9 为最高优先级。

## 📋 优先级字段说明

在 `tasks.Signature` 结构中，有一个 `Priority uint8` 字段：

```go
type Signature struct {
    // ... 其他字段
    Priority uint8  // 任务优先级 (0-9, 9为最高)
    // ... 其他字段
}
```

## 🚀 使用示例

### 1. 基本优先级设置

```go
task := &tasks.Signature{
    Name:     "create_vrf_on_switch",
    Priority: 8,  // 高优先级
    Args: []tasks.Arg{
        {Type: "string", Value: "input_data"},
    },
}
```

### 2. 任务链中的优先级

```go
task1 := &tasks.Signature{
    Name:     "task1",
    Priority: 5,  // 中等优先级
    Args: []tasks.Arg{{Type: "string", Value: "data1"}},
}

task2 := &tasks.Signature{
    Name:     "task2",
    Priority: 5,  // 中等优先级
    Args: []tasks.Arg{{Type: "string", Value: "data2"}},
}

chain, _ := tasks.NewChain(task1, task2)
server.SendChain(chain)
```

### 3. 任务组中的优先级

```go
highPriorityTask := &tasks.Signature{
    Name:     "high_priority_task",
    Priority: 9,  // 最高优先级
    Args: []tasks.Arg{{Type: "string", Value: "high"}},
}

lowPriorityTask := &tasks.Signature{
    Name:     "low_priority_task",
    Priority: 1,  // 低优先级
    Args: []tasks.Arg{{Type: "string", Value: "low"}},
}

group := tasks.NewGroup(highPriorityTask, lowPriorityTask)
server.SendGroup(group, 0)
```

## 📊 优先级行为说明

### 优先级范围
- **0**: 最低优先级（默认）
- **1-8**: 中等优先级
- **9**: 最高优先级

### 调度行为
1. **同一队列内**: 高优先级任务优先执行
2. **不同队列间**: 优先级无法跨队列比较
3. **相同优先级**: 按 FIFO 顺序执行
4. **任务链内**: 顺序执行不受优先级影响

## 💡 最佳实践

### 1. 合理分配优先级
```go
const (
    PriorityLow    = 1  // 批量任务、报表生成
    PriorityNormal = 5  // 常规业务任务
    PriorityHigh   = 8  // 用户请求、实时任务
    PriorityUrgent = 9  // 系统维护、紧急修复
)
```

### 2. VPC 场景中的应用

```go
// 紧急客户VPC创建 - 高优先级
urgentTask := &tasks.Signature{
    Name:     "create_vrf_on_switch",
    Priority: 9,
    Args: []tasks.Arg{{Type: "string", Value: urgentVPCData}},
}

// 批量VPC创建 - 低优先级
batchTask := &tasks.Signature{
    Name:     "create_vrf_on_switch",
    Priority: 2,
    Args: []tasks.Arg{{Type: "string", Value: batchVPCData}},
}
```

### 3. 动态优先级调整

```go
func createTaskWithDynamicPriority(importance string) *tasks.Signature {
    var priority uint8
    
    switch importance {
    case "critical":
        priority = 9
    case "high":
        priority = 7
    case "normal":
        priority = 5
    case "low":
        priority = 2
    default:
        priority = 0
    }
    
    return &tasks.Signature{
        Name:     "dynamic_task",
        Priority: priority,
        Args: []tasks.Arg{{Type: "string", Value: "data"}},
    }
}
```

## ⚠️ 注意事项

### 1. 队列隔离
优先级仅在同一队列内有效，不同队列的任务无法通过优先级排序：

```go
// 这两个任务在不同队列，优先级无法比较
task1 := &tasks.Signature{
    Name:       "task1",
    Priority:   9,
    RoutingKey: "queue1",  // 队列1
}

task2 := &tasks.Signature{
    Name:       "task2",
    Priority:   1,
    RoutingKey: "queue2",  // 队列2
}
```

### 2. 资源竞争
高优先级任务会抢占执行资源，可能导致低优先级任务饥饿：

```go
// 避免持续发送高优先级任务
for i := 0; i < 1000; i++ {
    task := &tasks.Signature{
        Name:     "high_priority_task",
        Priority: 9,  // 大量高优先级任务
        // ...
    }
    server.SendTask(task)
}
```

### 3. 任务链顺序
任务链内的顺序执行不受优先级影响：

```go
// 即使设置了优先级，task1仍会在task2之前执行
task1 := &tasks.Signature{
    Name:     "task1",
    Priority: 9,
}

task2 := &tasks.Signature{
    Name:     "task2",
    Priority: 1,
}

chain, _ := tasks.NewChain(task1, task2)  // task1 → task2
```

## 🧪 测试示例

### API 调用示例

创建高优先级 VPC：
```bash
curl -X POST http://localhost:8080/api/v1/vpc/priority \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "urgent-vpc",
    "vrf_name": "VRF-URGENT",
    "vlan_id": 999,
    "firewall_zone": "urgent-zone"
  }'
```

## 📈 监控建议

1. **队列长度监控**: 监控不同优先级任务的积压情况
2. **执行时间统计**: 统计不同优先级任务的平均执行时间
3. **优先级分布**: 分析系统中各优先级任务的分布比例

## 🔄 与其他编排方式结合

### 优先级 + Chain
```go
// 所有链内任务使用相同优先级
chainTask1 := &tasks.Signature{
    Name:     "step1",
    Priority: 7,
}
chainTask2 := &tasks.Signature{
    Name:     "step2",
    Priority: 7,
}
```

### 优先级 + Group
```go
// 组内任务可以有不同的优先级
groupTask1 := &tasks.Signature{
    Name:     "parallel_task1",
    Priority: 8,  // 高优先级
}
groupTask2 := &tasks.Signature{
    Name:     "parallel_task2",
    Priority: 3,  // 中优先级
}
```

### 优先级 + Chord
```go
// 并行任务和回调任务都可以设置优先级
parallelTask := &tasks.Signature{
    Name:     "parallel",
    Priority: 6,
}

callbackTask := &tasks.Signature{
    Name:     "callback",
    Priority: 8,  // 回调任务更高优先级
}

group := tasks.NewGroup(parallelTask)
chord, _ := tasks.NewChord(group, callbackTask)
```
