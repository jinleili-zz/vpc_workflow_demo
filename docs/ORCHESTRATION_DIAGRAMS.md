# Machinery 任务编排流程图

## 1. 独立任务 (SendTask)

```mermaid
graph LR
    API[API请求] --> T1[Task1: VRF]
    API --> T2[Task2: VLAN]
    API --> T3[Task3: Firewall]
    
    T1 --> W1[Worker处理]
    T2 --> W2[Worker处理]
    T3 --> W3[Worker处理]
    
    W1 --> D1[完成]
    W2 --> D2[完成]
    W3 --> D3[完成]
    
    style API fill:#e1f5ff
    style T1 fill:#fff4e1
    style T2 fill:#fff4e1
    style T3 fill:#fff4e1
    style D1 fill:#e8f5e9
    style D2 fill:#e8f5e9
    style D3 fill:#e8f5e9
```

**特点**: 
- 🔀 并行执行
- ⚡ 最快（~2秒）
- ❌ 无顺序保证

---

## 2. 任务链 (Chain)

```mermaid
graph LR
    API[API请求] --> T1[Task1: VRF]
    T1 -->|结果传递| T2[Task2: VLAN]
    T2 -->|结果传递| T3[Task3: Firewall]
    T3 --> Done[完成]
    
    style API fill:#e1f5ff
    style T1 fill:#fff4e1
    style T2 fill:#fff4e1
    style T3 fill:#fff4e1
    style Done fill:#e8f5e9
```

**特点**:
- 🔗 严格顺序
- 📊 结果传递
- 🐌 最慢（~6秒）

---

## 3. 任务组 (Group)

```mermaid
graph TB
    API[API请求] --> Group{Group}
    
    Group --> T1[Task1: VRF]
    Group --> T2[Task2: VLAN]
    Group --> T3[Task3: Firewall]
    
    T1 --> Wait[等待所有完成]
    T2 --> Wait
    T3 --> Wait
    
    Wait --> Done[全部完成]
    
    style API fill:#e1f5ff
    style Group fill:#f3e5f5
    style T1 fill:#fff4e1
    style T2 fill:#fff4e1
    style T3 fill:#fff4e1
    style Wait fill:#ffe0b2
    style Done fill:#e8f5e9
```

**特点**:
- 🔀 并行执行
- ⏱️ 可等待全部完成
- ⚡ 快速（~2秒）

---

## 4. Chord (推荐) ⭐

```mermaid
graph TB
    API[API请求] --> Chord{Chord}
    
    Chord --> Group[Group: 并行阶段]
    
    Group --> T1[Task1: VRF]
    Group --> T2[Task2: VLAN]
    
    T1 --> Sync[同步点]
    T2 --> Sync
    
    Sync --> Callback[Callback: Firewall]
    Callback --> Done[完成]
    
    style API fill:#e1f5ff
    style Chord fill:#f3e5f5
    style Group fill:#e1f5fe
    style T1 fill:#fff4e1
    style T2 fill:#fff4e1
    style Sync fill:#ffe0b2
    style Callback fill:#ffccbc
    style Done fill:#e8f5e9
```

**特点**:
- 🚀 并行 + 顺序
- ✅ 有序依赖
- ⚡ 中等速度（~4秒）

---

## VPC 创建工作流对比

### 时间线对比

#### 独立任务 (SendTask)
```
时间轴:  0s -------- 2s
        VRF     ████
        VLAN    ████
        FW      ████
                     ↑
              总耗时: ~2秒
              风险: 防火墙可能在VRF前创建
```

#### 任务链 (Chain)
```
时间轴:  0s -- 2s -- 4s -- 6s
        VRF   ████
        VLAN        ████
        FW                ████
                              ↑
                       总耗时: ~6秒
                       保证: 严格顺序
```

#### Group
```
时间轴:  0s -------- 2s
        VRF     ████
        VLAN    ████
        FW      ████
                     ↑
              总耗时: ~2秒
              可以等待所有完成
```

#### Chord (推荐) ⭐
```
时间轴:  0s -- 2s ---- 4s
        VRF   ████
        VLAN  ████
                   FW  ████
                            ↑
                     总耗时: ~4秒
                     保证: FW在VRF/VLAN之后
```

---

## 复杂编排示例

### 示例1: VRF -> (VLAN || Firewall)

```mermaid
graph TB
    Start[开始] --> VRF[创建VRF]
    VRF --> Parallel{并行执行}
    Parallel --> VLAN[创建VLAN]
    Parallel --> FW[配置防火墙]
    VLAN --> End[完成]
    FW --> End
    
    style Start fill:#e1f5ff
    style VRF fill:#fff4e1
    style Parallel fill:#f3e5f5
    style VLAN fill:#fff4e1
    style FW fill:#ffccbc
    style End fill:#e8f5e9
```

**实现**:
```go
vrfTask := &tasks.Signature{
    Name: "create_vrf",
    OnSuccess: []*tasks.Signature{
        {Name: "create_vlan"},
        {Name: "create_firewall"},
    },
}
```

---

### 示例2: (VRF || VLAN) -> Firewall -> 通知

```mermaid
graph TB
    Start[开始] --> Group[并行创建]
    Group --> VRF[创建VRF]
    Group --> VLAN[创建VLAN]
    VRF --> Sync[同步]
    VLAN --> Sync
    Sync --> FW[配置防火墙]
    FW --> Notify[发送通知]
    Notify --> End[完成]
    
    style Start fill:#e1f5ff
    style Group fill:#f3e5f5
    style VRF fill:#fff4e1
    style VLAN fill:#fff4e1
    style Sync fill:#ffe0b2
    style FW fill:#ffccbc
    style Notify fill:#e3f2fd
    style End fill:#e8f5e9
```

**实现**:
```go
// 第一阶段
group := tasks.NewGroup(
    &tasks.Signature{Name: "create_vrf"},
    &tasks.Signature{Name: "create_vlan"},
)

// 第二阶段: 防火墙
fwTask := &tasks.Signature{
    Name: "create_firewall",
    OnSuccess: []*tasks.Signature{
        {Name: "send_notification"},
    },
}

// 创建Chord
chord, _ := tasks.NewChord(group, fwTask)
```

---

## 决策树: 如何选择编排方式

```mermaid
graph TD
    Start{需要顺序吗?}
    Start -->|否| Independent{需要等待完成?}
    Start -->|是| OrderType{什么类型?}
    
    Independent -->|否| SendTask[使用 SendTask]
    Independent -->|是| UseGroup[使用 Group]
    
    OrderType -->|全部串行| UseChain[使用 Chain]
    OrderType -->|部分并行| UseChord[使用 Chord ⭐]
    
    SendTask --> Note1[最简单<br/>最快]
    UseGroup --> Note2[并行<br/>可等待]
    UseChain --> Note3[严格顺序<br/>结果传递]
    UseChord --> Note4[推荐!<br/>性能+顺序]
    
    style Start fill:#e1f5ff
    style Independent fill:#e1f5ff
    style OrderType fill:#e1f5ff
    style SendTask fill:#fff4e1
    style UseGroup fill:#fff4e1
    style UseChain fill:#fff4e1
    style UseChord fill:#c8e6c9
    style Note4 fill:#a5d6a7
```

---

## 总结

### 性能对比

| 模式 | 耗时 | 顺序保证 | 推荐度 |
|-----|------|---------|--------|
| SendTask | ⭐⭐⭐ (2秒) | ❌ | ⭐⭐⭐ |
| Chain | ⭐ (6秒) | ✅ | ⭐⭐ |
| Group | ⭐⭐⭐ (2秒) | ❌ | ⭐⭐⭐⭐ |
| **Chord** | **⭐⭐ (4秒)** | **✅** | **⭐⭐⭐⭐⭐** |

### VPC场景推荐: Chord ⭐⭐⭐⭐⭐

**理由**:
1. ✅ VRF和VLAN并行，节省50%时间
2. ✅ 防火墙在网络配置后执行，符合逻辑
3. ✅ 性能和顺序的完美平衡
4. ✅ 代码清晰易懂

**实现**: 参考 [api/server_advanced.go](../api/server_advanced.go)
