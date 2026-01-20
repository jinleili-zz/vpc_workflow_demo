# AZ-NSP 任务状态机分析

## 1. 资源状态机 (ResourceStatus)

```
                    ┌─────────────────────────────────────────────────────┐
                    │                                                     │
                    ▼                                                     │
┌─────────┐    ┌──────────┐    ┌─────────┐    ┌──────────┐    ┌─────────┐ │
│ pending │───►│ creating │───►│ running │───►│ deleting │───►│ deleted │ │
└─────────┘    └──────────┘    └─────────┘    └──────────┘    └─────────┘ │
                    │                                                     │
                    │         ┌────────┐                                  │
                    └────────►│ failed │──────────────────────────────────┘
                              └────────┘  (任务重做后可恢复)
```

**状态定义** (`internal/models/resource.go:5-14`):

| 状态 | 含义 |
|------|------|
| `pending` | 资源创建请求已接收，尚未处理 |
| `creating` | 任务链正在执行中 |
| `running` | 所有任务成功完成，资源可用 |
| `failed` | 任务执行失败 |
| `deleting` | 正在删除 |
| `deleted` | 已删除 |

---

## 2. 任务状态机 (TaskStatus)

```
┌─────────┐    ┌────────┐    ┌─────────┐    ┌───────────┐
│ pending │───►│ queued │───►│ running │───►│ completed │
└─────────┘    └────────┘    └─────────┘    └───────────┘
     ▲                            │
     │                            ▼
     │                       ┌────────┐
     └───────────────────────│ failed │  (ReplayTask重做)
                             └────────┘
```

**状态定义** (`internal/models/resource.go:49-58`):

| 状态 | 含义 |
|------|------|
| `pending` | 任务待执行 |
| `queued` | 已入队到 asynq，等待 worker 消费 |
| `running` | Worker 正在执行 |
| `completed` | 执行成功 |
| `failed` | 执行失败 |
| `cancelled` | 已取消 |

---

## 3. 状态转换触发点

| 转换 | 触发位置 | 代码位置 |
|------|----------|----------|
| Resource: `pending → creating` | CreateVPC/CreateSubnet | `orchestrator.go:82` |
| Task: `pending → queued` | enqueueTask | `orchestrator.go:327` |
| Task: `queued/running → completed/failed` | HandleTaskCallback | `orchestrator.go:343` |
| Resource: `creating → running` | checkAndCompleteResource (所有任务完成) | `orchestrator.go:420` |
| Resource: `creating → failed` | handleTaskFailure | `orchestrator.go:394` |
| Task: `failed → pending` | ReplayTask | `orchestrator.go:649` |
| Resource: `running → deleting/deleted` | DeleteVPC/DeleteSubnet | `orchestrator.go:532,598` |

---

## 4. VPC 创建完整流程

```
1. CreateVPC 请求
   │
   ├── 创建 VPCResource (status=pending)
   ├── 创建 3 个 Task (均为 pending)
   │   ├── Task1: create_vrf_on_switch (order=1)
   │   ├── Task2: create_vlan_subinterface (order=2)
   │   └── Task3: create_firewall_zone (order=3)
   ├── VPC status → creating
   └── 入队 Task1 (status → queued)
                │
                ▼
2. Worker 消费 Task1 → 执行 → 回调
   │
   ├── Task1 status → completed
   ├── VPC.completed_tasks++
   └── 入队 Task2 (status → queued)
                │
                ▼
3. Worker 消费 Task2 → 执行 → 回调
   │
   ├── Task2 status → completed
   ├── VPC.completed_tasks++
   └── 入队 Task3 (status → queued)
                │
                ▼
4. Worker 消费 Task3 → 执行 → 回调
   │
   ├── Task3 status → completed
   ├── VPC.completed_tasks++
   └── checkAndCompleteResource: VPC status → running
```

---

## 5. 失败处理与重做

```
任务失败:
  Task status → failed
  Resource status → failed
  后续任务不再执行

任务重做 (ReplayTask):
  Task status: failed → pending → queued
  重新入队执行
```

---

## 6. 关键代码路径

### 6.1 资源创建入口
- `internal/az/orchestrator/orchestrator.go:CreateVPC()` - VPC创建
- `internal/az/orchestrator/orchestrator.go:CreateSubnet()` - 子网创建

### 6.2 任务调度
- `internal/az/orchestrator/orchestrator.go:enqueueFirstTask()` - 入队首个任务
- `internal/az/orchestrator/orchestrator.go:enqueueTask()` - 任务入队逻辑

### 6.3 回调处理
- `internal/az/orchestrator/orchestrator.go:HandleTaskCallback()` - 任务回调入口
- `internal/az/orchestrator/orchestrator.go:handleTaskSuccess()` - 成功处理
- `internal/az/orchestrator/orchestrator.go:handleTaskFailure()` - 失败处理

### 6.4 Worker 任务处理
- `tasks/handlers.go:CreateVRFOnSwitchHandler()` - VRF创建
- `tasks/handlers.go:CreateVLANSubInterfaceHandler()` - VLAN子接口创建
- `tasks/handlers.go:CreateFirewallZoneHandler()` - 防火墙区域创建
- `tasks/handlers.go:CreateSubnetOnSwitchHandler()` - 子网创建
- `tasks/handlers.go:ConfigureSubnetRoutingHandler()` - 子网路由配置
