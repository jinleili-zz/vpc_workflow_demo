# vpc_workflow_demo 切换到 nsp_platform taskqueue Engine 方案评估

> **版本**: v2（整合 review 反馈）
> **变更记录**: 新增 Gap 7-11（Store 查询缺失、事务一致性、自动重试行为变化、enqueueStep 性能、Metadata 查询），修正 nsp-common 改动量估算，补充补偿机制和迁移策略。

## 1. 背景

当前 `vpc_workflow_demo` 仅使用了 `nsp-common/pkg/taskqueue` 的底层消息队列组件（`Broker`、`Consumer`、`CallbackSender`），
而工作流编排、任务状态管理、步骤驱动等逻辑全部由业务侧自行实现（`orchestrator` + `DAO`）。

`nsp-common/pkg/taskqueue` 同时提供了一套完整的 Workflow Engine API（`Engine`、`SubmitWorkflow`、`HandleCallback`），
内置了 PostgreSQL 持久化、步骤顺序驱动、自动重试、状态查询等能力。

本文档评估将 `vpc_workflow_demo` 的 az_nsp → worker 通信链路完全切换到 Engine API 的可行性、收益、风险和实施步骤。

---

## 2. 现状分析

### 2.1 当前已使用的 taskqueue 组件

| 组件 | 来源 | 用途 | 使用位置 |
|------|------|------|----------|
| `taskqueue.Broker` 接口 | nsp-common | 发布任务到 Redis | `orchestrator.go:334`, `vfw/orchestrator.go:187` |
| `asynqbroker.NewBroker` | nsp-common | Broker 实现 | `cmd/az_nsp/main.go:148`, `cmd/worker/main.go:89` |
| `asynqbroker.NewConsumer` | nsp-common | 消费任务 & 回调 | `cmd/az_nsp/main.go:168`, `cmd/worker/main.go:95` |
| `taskqueue.CallbackSender` | nsp-common | Worker 回调 az_nsp | `cmd/worker/main.go:92` |
| `taskqueue.HandlerFunc` | nsp-common | Worker handler 签名 | `tasks/handlers.go` (8个handler) |
| `taskqueue.TaskPayload` / `TaskResult` | nsp-common | Handler 入参/出参 | `tasks/handlers.go` |

### 2.2 当前未使用的 taskqueue 组件

| 组件 | 来源 | 能力 |
|------|------|------|
| `taskqueue.Engine` | nsp-common | 工作流引擎（提交/回调/查询/重试） |
| `taskqueue.SubmitWorkflow` | nsp-common | 声明式工作流提交 |
| `taskqueue.WorkflowDefinition` | nsp-common | 工作流步骤定义 |
| `taskqueue.Store` / `PostgresStore` | nsp-common | `tq_workflows` + `tq_steps` 表管理 |
| `taskqueue.HandleCallback` | nsp-common | 自动驱动步骤状态机 |

### 2.3 业务侧自实现的等价逻辑（即切换后可删除的代码）

**涉及 3 个 orchestrator，2 套 DAO，共约 2,800 行代码：**

| 文件 | 行数 | 自实现的等价逻辑 |
|------|------|-----------------|
| `internal/az/orchestrator/orchestrator.go` | 706 | 任务构建、入队、回调处理、步骤驱动、重试、完成检查 |
| `internal/az/vfw/orchestrator/orchestrator.go` | 348 | 同上（防火墙策略场景） |
| `internal/db/dao/dao.go` (TaskDAO 部分) | ~280 | 任务 CRUD、状态更新、统计查询 |
| `internal/az/vfw/dao/dao.go` (VFWTaskDAO 部分) | ~280 | 同上（VFW 场景） |

> 注：`VPCDAO`、`SubnetDAO`、`FirewallPolicyDAO` 中的**资源管理**逻辑（Create/Get/Delete/UpdateStatus）无论是否切换都需要保留，不属于可删除范围。

---

## 3. 数据模型对比

### 3.1 任务模型映射

| vpc_workflow_demo (`models.Task`) | taskqueue Engine (`StepTask`) | 兼容性 |
|-----------------------------------|-------------------------------|--------|
| `ID` | `ID` | 直接映射 |
| `ResourceType` | 存于 `Workflow.ResourceType` | 上移到 workflow 层 |
| `ResourceID` | 存于 `Workflow.ResourceID` | 上移到 workflow 层 |
| `TaskType` | `TaskType` | 直接映射 |
| `TaskName` | `TaskName` | 直接映射 |
| `TaskOrder` | `StepOrder` | 直接映射 |
| `TaskParams` (string) | `Params` (string) | 直接映射 |
| `Status` | `Status` | 枚举值完全一致 (pending/queued/completed/failed) |
| `Priority` (int) | `Priority` (int) | 值域一致 (1/3/6/9) |
| **`DeviceType`** | **`QueueTag`** | **语义映射：DeviceType → QueueTag** |
| `AsynqTaskID` | `BrokerTaskID` | 改名，直接映射 |
| `Result` | `Result` | 直接映射 |
| `ErrorMessage` | `ErrorMessage` | 直接映射 |
| `RetryCount` / `MaxRetries` | `RetryCount` / `MaxRetries` | 直接映射 |
| **`AZ`** | **无对应字段** | **Gap：需存入 Workflow.Metadata** |
| 时间戳字段 | 时间戳字段 | 直接映射 |

### 3.2 资源模型（无对应，需保留）

Engine 只管理 `tq_workflows` + `tq_steps`，以下表需**继续保留**：

- `vpc_resources` — VPC 资源状态（status, vrf_name, vlan_id 等业务字段）
- `subnet_resources` — 子网资源状态
- `firewall_policies` — 防火墙策略资源状态

### 3.3 数据库表关系（切换后）

```
切换前（单层）：                         切换后（双层）：
┌─────────────────┐                     ┌─────────────────┐
│ vpc_resources    │                     │ vpc_resources    │  ← 业务资源（保留）
│ subnet_resources │                     │ subnet_resources │
│ tasks            │ ← 全自管理          │ fw_policies      │
└─────────────────┘                     ├─────────────────┤
                                        │ tq_workflows     │  ← Engine 管理（新增）
                                        │ tq_steps         │
                                        └─────────────────┘
                                        关联：vpc_resources.id = tq_workflows.resource_id
```

---

## 4. 关键差异与 Gap 分析

### Gap 1：资源状态同步（最核心的 Gap）

**问题**：Engine 的 `HandleCallback` 只更新 `tq_workflows` / `tq_steps` 状态，不会触及业务资源表（`vpc_resources.status`）。

**现状逻辑**：
```
回调到达 → 更新 tasks 表 → 更新 vpc_resources.completed_tasks
                         → 全部完成时更新 vpc_resources.status = running
                         → 失败时更新 vpc_resources.status = failed
```

**解决方案**：给 Engine 新增 **生命周期回调钩子**：

```go
// 需要在 nsp-common/pkg/taskqueue/engine.go 中新增
type WorkflowHooks struct {
    OnStepComplete    func(ctx context.Context, workflowID string, step *StepTask) error
    OnStepFailed      func(ctx context.Context, workflowID string, step *StepTask, errMsg string) error
    OnWorkflowComplete func(ctx context.Context, workflow *Workflow) error
    OnWorkflowFailed   func(ctx context.Context, workflow *Workflow, errMsg string) error
}
```

业务侧只需实现钩子：
```go
hooks := &taskqueue.WorkflowHooks{
    OnStepComplete: func(ctx context.Context, wfID string, step *StepTask) error {
        return vpcDAO.IncrementCompletedTasks(ctx, workflow.ResourceID)
    },
    OnWorkflowComplete: func(ctx context.Context, wf *Workflow) error {
        return vpcDAO.UpdateStatus(ctx, wf.ResourceID, "running", "")
    },
    OnWorkflowFailed: func(ctx context.Context, wf *Workflow, errMsg string) error {
        return vpcDAO.UpdateStatus(ctx, wf.ResourceID, "failed", errMsg)
    },
}
```

**改动范围**：`nsp-common/pkg/taskqueue/engine.go`（Engine 结构体新增 Hooks 字段，HandleCallback 中调用）。

**评估**：**中等难度**。需要修改 nsp-common，但改动集中且明确。

---

### Gap 2：队列路由策略

**问题**：Engine 的 `DefaultQueueRouter` 格式为 `tasks_{queueTag}[_priority]`，而 vpc_workflow_demo 的格式为 `tasks_{region}_{az}_{deviceType}[_priority]`。

**现状**：
```go
// vpc_workflow_demo 当前的队列名
queue.GetPriorityQueueName(region, az, deviceType, priority)
// => "tasks_cn-beijing_cn-beijing-1a_switch_high"
```

**解决方案**：初始化 Engine 时传入自定义 `QueueRouterFunc`：
```go
engine, _ := taskqueue.NewEngine(&taskqueue.Config{
    DSN:           postgresDSN,
    CallbackQueue: callbackQueueName,
    QueueRouter: func(queueTag string, priority taskqueue.Priority) string {
        deviceType := queue.DeviceType(queueTag)
        return queue.GetPriorityQueueName(region, az, deviceType, queue.TaskPriority(priority))
    },
}, broker)
```

`StepDefinition.QueueTag` 填入 `DeviceType` 值（如 `"switch"`, `"firewall"`）。

**评估**：**无需改动 nsp-common**，Engine 已支持自定义 QueueRouter。

---

### Gap 3：回调消费链路改造

**问题**：当前 az_nsp 的 `HandleRaw("task_callback", ...)` 手动解析 payload 并调用 orchestrator，切换后需改为调用 Engine。

**现状**（`cmd/az_nsp/main.go:175-177`）：
```go
callbackConsumer.HandleRaw("task_callback", func(ctx context.Context, t *asynq.Task) error {
    return server.HandleTaskCallback(ctx, t.Payload())
})
```

**切换后**：
```go
callbackConsumer.HandleRaw("task_callback", func(ctx context.Context, t *asynq.Task) error {
    var cb taskqueue.CallbackPayload
    if err := json.Unmarshal(t.Payload(), &cb); err != nil {
        return err
    }
    return engine.HandleCallback(ctx, &cb)
    // Engine 内部自动：更新 step 状态 → 驱动下一步 → 触发 Hooks 回调业务层
})
```

**评估**：**改动量很小**，仅替换回调处理入口。

---

### Gap 4：API 响应格式适配

**问题**：现有 API（如 `GET /vpc/:vpc_name/status`）返回 `models.Task` 列表，切换后 Engine 返回 `taskqueue.StepTask`，字段名有差异。

**关键差异**：

| 现有 API 字段 | StepTask 字段 | 差异 |
|--------------|--------------|------|
| `task_order` | `step_order` | 字段名不同 |
| `device_type` | `queue_tag` | 字段名+语义 |
| `asynq_task_id` | `broker_task_id` | 字段名不同 |
| `az` | 无（在 Workflow.Metadata 中） | 位置不同 |

**解决方案**：两种路径可选——

- **方案 A**：在 API 层做一次 `StepTask → models.Task` 的转换，保持外部 API 不变
- **方案 B**：直接暴露 `WorkflowStatusResponse`（Engine 原生格式），前端适配

**建议**：选 **方案 A**，避免对 API 消费方造成 breaking change。转换代码约 30 行。

---

### Gap 5：VFW orchestrator 的独立回调队列

**问题**：VFW 场景使用独立的回调队列 `callbacks_{region}_{az}_vfw`，而 VPC 场景用 `callbacks_{region}_{az}_vpc`。Engine 的 `Config.CallbackQueue` 是单一值。

**现状**：
```go
// cmd/worker/main.go:110 — VFW 有独立的 cbSender
cbSenderVFW := taskqueue.NewCallbackSenderFromBroker(broker, queue.GetCallbackQueueName(region, az, "vfw"))
```

**解决方案**：两种路径可选——

- **方案 A（推荐）**：az_nsp_vpc 和 az_nsp_vfw 各自创建独立的 Engine 实例，各自监听自己的回调队列。现状就是两个独立进程（`cmd/az_nsp/main.go` 和 `cmd/az_nsp_vfw/main.go`），天然适合。
- **方案 B**：合并到同一个回调队列，通过 task_type 前缀区分。改动较大，不推荐。

**评估**：选方案 A，**无需额外改动**，每个进程独立一个 Engine 实例即可。

---

### Gap 6：ReplayTask（手动重做）

**问题**：当前 `orchestrator.ReplayTask()` 重置任务状态并重新入队。Engine 提供了 `RetryStep()`，但语义略有不同。

**对比**：

| 现有 ReplayTask | Engine RetryStep |
|----------------|-----------------|
| 重置 `retry_count = 0` | 不重置 `retry_count` |
| 仅要求 `status = failed` | 仅要求 `status = failed` |
| 同时重置 `workflow status = running`（无） | **会重置 workflow status = running** |

**解决方案**：Engine 的 `RetryStep` 已经包含了重置 workflow 状态的逻辑（`engine.go:273`），比现有实现更完整。如果需要重置 retry_count，可以在 nsp-common 的 `RetryStep` 中增加一行 `UpdateStepStatus` 前加 reset retry_count。

**评估**：**小改动**，或直接沿用 Engine 行为（不重置 retry_count 也合理）。

---

### Gap 7：Store 缺少按 ResourceID 查询 workflow 的方法（review 补充）

**严重程度：高（阻塞迁移）**

**问题**：当前 `Store` 接口只有 `GetWorkflow(ctx, id)` 按 workflow UUID 查询（`store.go:14`）。但业务 API 入口是**资源维度**：

```
GET /vpc/:vpc_name/status   → 先查 vpc_resources 拿到 vpc_id → 需用 vpc_id 查对应 workflow
POST /task/replay/:task_id  → 需用 step_id 反查 workflow
```

Store 接口中缺少：

```go
GetWorkflowsByResourceID(ctx context.Context, resourceType, resourceID string) ([]*Workflow, error)
```

**注意**：同一个资源可能有多个 workflow（例如 create_vpc 失败后 retry，或先 create 后 delete），因此需要支持查询列表并按时间排序。

**解决方案**：在 nsp-common `Store` 接口和 `PostgresStore` 中新增该方法，预计 ~30 行。同时建议给 `tq_workflows` 添加索引：

```sql
CREATE INDEX IF NOT EXISTS idx_tq_workflows_resource ON tq_workflows(resource_type, resource_id);
```

**评估**：**必须在阶段一完成**，否则状态查询 API 无法实现。

---

### Gap 8：Hooks 与 Engine 的事务一致性（review 补充）

**严重程度：中**

**问题**：`handleStepSuccess`/`handleStepFailure` 中的各个 store 操作是**独立的 SQL 语句**，没有事务包裹（`engine.go:345-401`）。如果 Hooks 也是独立的 DB 调用，Engine 更新 `tq_steps` 和 Hooks 更新 `vpc_resources` 之间没有事务保证。

**极端异常场景**：
1. Engine 标记 `tq_workflows.status = succeeded`
2. Hook `OnWorkflowComplete` 执行到一半进程崩溃
3. 结果：`tq_workflows` 为 succeeded，但 `vpc_resources.status` 仍为 creating

**解决方案**（分阶段）：

- **短期（本次迁移）**：接受最终一致。添加**补偿任务**——定时扫描 `tq_workflows.status = succeeded` 但对应 `vpc_resources.status != running` 的记录并自动修复。实现简单（~30 行），可有效兜底：

```go
// 每 60 秒扫描一次
// SELECT w.id, w.resource_type, w.resource_id
// FROM tq_workflows w
// JOIN vpc_resources v ON v.id = w.resource_id
// WHERE w.status = 'succeeded' AND v.status = 'creating'
// → 自动修复 vpc_resources.status = running
```

- **长期**：考虑在 `handleStepSuccess`/`handleStepFailure` 中引入事务上下文，让 Hooks 可以加入同一个事务。

---

### Gap 9：自动重试行为变化（review 补充）

**严重程度：中（行为差异，需团队确认）**

**问题**：Engine 内置了自动重试逻辑（`engine.go:367-388`）：

```go
func (e *Engine) handleStepFailure(...) {
    if step.RetryCount < step.MaxRetries {
        // 自动重置并重新入队
        ...
        return nil
    }
    // 重试耗尽才标记 workflow failed
}
```

**行为差异对比**：

| | 当前 vpc_workflow_demo | 切换后 Engine |
|---|---|---|
| 任务失败时 | **立即标记 resource 为 failed**，不自动重试 | 自动重试 MaxRetries 次，全部耗尽才标记 failed |
| 重试控制 | 手动触发（ReplayTask API） | 自动 + 手动（RetryStep API） |

这实际上是**能力提升**，但需要团队确认是否期望该行为。如果某些任务不希望自动重试（如涉及外部计费的操作），应将其 `MaxRetries` 设为 `0`。

**建议**：默认接受自动重试（当前 demo 中 MaxRetries 均为 3），但在文档中明确说明该行为变化。

---

### Gap 10：enqueueStep 重复查询 workflow（review 补充）

**严重程度：低（性能优化，不阻塞迁移）**

**问题**：`enqueueStep`（`engine.go:298-306`）每次都查询 workflow 以获取 `ResourceID`：

```go
func (e *Engine) enqueueStep(ctx context.Context, step *StepTask) error {
    wf, err := e.store.GetWorkflow(ctx, step.WorkflowID)  // 每次 enqueue 都查一次
    ...
    payload := map[string]interface{}{
        "resource_id": wf.ResourceID,  // 只为拿这个字段
    }
}
```

而 `handleStepSuccess` 调用 `enqueueStep` 时，上下文中已经有 workflow 信息可以传递。

**建议**：后续优化时将 `enqueueStep` 改为接受 `resourceID` 参数，或在 `handleStepSuccess` 中缓存 workflow 信息。**不阻塞本次迁移**。

---

### Gap 11：Workflow Metadata 的查询能力（review 补充）

**严重程度：低（不阻塞迁移）**

**问题**：proposal 中 `AZ` 字段存入 `Workflow.Metadata`，但 Store 接口没有按 metadata 查询的方法。如果后续需要"查询某个 AZ 下所有 workflow"的能力，需要扩展 Store。

**当前阶段不阻塞**——AZ 信息可以通过 `vpc_resources.az` 关联查询。长期来看，给 `tq_workflows.metadata` 加 GIN 索引并提供查询方法会更完整：

```sql
CREATE INDEX IF NOT EXISTS idx_tq_workflows_metadata ON tq_workflows USING GIN(metadata);
```

---

## 5. 切换后架构总览

```
                          切换前                                    切换后
┌─────────────────────────────────────────────┐  ┌─────────────────────────────────────────────┐
│ az_nsp (VPC)                                │  │ az_nsp (VPC)                                │
│                                             │  │                                             │
│  API Server                                 │  │  API Server                                 │
│    │                                        │  │    │                                        │
│    ▼                                        │  │    ▼                                        │
│  AZOrchestrator                             │  │  AZOrchestrator (精简版)                     │
│    ├─ buildTasks()        ← 手写            │  │    ├─ buildWorkflowDef()   ← 声明式          │
│    ├─ taskDAO.BatchCreate ← 手写 DAO        │  │    ├─ engine.SubmitWorkflow ← Engine 接管    │
│    ├─ enqueueTask()       ← 手写入队        │  │    ├─ vpcDAO (保留)                          │
│    ├─ handleTaskSuccess() ← 手写状态机      │  │    └─ WorkflowHooks (状态同步回调)            │
│    ├─ handleTaskFailure() ← 手写重试        │  │                                             │
│    ├─ vpcDAO              ← 保留            │  │  Callback Consumer                          │
│    └─ taskDAO             ← 可删除          │  │    └─ engine.HandleCallback ← Engine 接管    │
│                                             │  │                                             │
│  Callback Consumer                          │  └──────────────┬──────────────────────────────┘
│    └─ HandleTaskCallback  ← 手写            │                 │ Redis (asynq)
│                                             │  ┌──────────────▼──────────────────────────────┐
└──────────────┬──────────────────────────────┘  │ Worker (不变)                                │
               │ Redis (asynq)                   │  ├─ consumer.Handle("create_vrf_on_switch")  │
┌──────────────▼──────────────────────────────┐  │  ├─ cbSender.Success(ctx, taskID, result)    │
│ Worker (不变)                                │  │  └─ ...                                     │
│  ├─ consumer.Handle("create_vrf_on_switch") │  └─────────────────────────────────────────────┘
│  ├─ cbSender.Success(ctx, taskID, result)   │
│  └─ ...                                     │
└─────────────────────────────────────────────┘
```

---

## 6. 改动范围汇总

### 6.1 nsp-common 侧（需要改动）

| 文件 | 操作 | 说明 |
|------|------|------|
| `pkg/taskqueue/engine.go` | **修改** | 新增 `WorkflowHooks` 结构体和字段；`handleStepSuccess`/`handleStepFailure`/`checkAndCompleteWorkflow` 中调用 hooks |
| `pkg/taskqueue/store.go` | **修改** | 新增 `GetWorkflowsByResourceID` 接口方法 |
| `pkg/taskqueue/pg_store.go` | **修改** | 实现 `GetWorkflowsByResourceID`；新增 `idx_tq_workflows_resource` 索引 |
| `pkg/taskqueue/task.go` | **不变** | 已有的类型定义完全够用 |

预计改动量：**~80 行**（WorkflowHooks ~50 行 + GetWorkflowsByResourceID ~30 行）

### 6.2 vpc_workflow_demo 侧

#### 需要修改的文件

| 文件 | 操作 | 说明 |
|------|------|------|
| `cmd/az_nsp/main.go` | **修改** | 创建 Engine 实例替代裸 Broker；回调处理改为 `engine.HandleCallback` |
| `cmd/az_nsp_vfw/main.go` | **修改** | 同上（VFW 场景） |
| `internal/az/orchestrator/orchestrator.go` | **大幅精简** | 删除 taskDAO/enqueueTask/handleTaskSuccess/handleTaskFailure 等约 400 行；改为调用 `engine.SubmitWorkflow` + 实现 Hooks |
| `internal/az/vfw/orchestrator/orchestrator.go` | **大幅精简** | 同上，约 200 行 |
| `internal/az/api/server.go` | **小改** | `HandleTaskCallback` 改为调用 Engine；状态查询适配 |
| `internal/az/vfw/api/server.go` | **小改** | 同上 |

#### 可删除的文件/代码

| 文件 | 操作 | 说明 |
|------|------|------|
| `internal/db/dao/dao.go` (TaskDAO) | **删除 TaskDAO 部分** | ~280 行，Engine 的 PostgresStore 接管 |
| `internal/az/vfw/dao/dao.go` (VFWTaskDAO) | **删除 VFWTaskDAO 部分** | ~280 行，同上 |

> VPCDAO、SubnetDAO、FirewallPolicyDAO **保留**。

#### 不需要改动的文件

| 文件 | 说明 |
|------|------|
| `cmd/worker/main.go` | Worker 侧完全不变 |
| `tasks/handlers.go` | 所有 handler 完全不变 |
| `internal/queue/queue.go` | 队列命名逻辑保留，供 QueueRouter 使用 |
| `internal/models/` | 资源模型保留；Task 模型仍用于 API 兼容层 |
| `internal/config/` | 配置不变 |

---

## 7. 代码示例：切换后的 orchestrator

### 7.1 CreateVPC（切换前 vs 切换后）

**切换前**（~60 行）：
```go
func (o *AZOrchestrator) CreateVPC(ctx context.Context, req *models.VPCRequest) (...) {
    vpcID := uuid.New().String()
    // 1. 创建 VPC 资源记录
    vpcDAO.Create(ctx, vpcResource)
    // 2. 构建任务列表
    tasks := o.buildVPCTasks(ctx, vpcID, req)
    // 3. 批量写入任务表
    taskDAO.BatchCreate(ctx, tasks)
    // 4. 更新 total_tasks
    vpcDAO.UpdateTotalTasks(ctx, vpcID, len(tasks))
    // 5. 更新 VPC 状态为 creating
    vpcDAO.UpdateStatus(ctx, vpcID, "creating", "")
    // 6. 入队首个任务
    o.enqueueFirstTask(ctx, vpcID)
}
```

**切换后**（~30 行）：
```go
func (o *AZOrchestrator) CreateVPC(ctx context.Context, req *models.VPCRequest) (...) {
    vpcID := uuid.New().String()
    // 1. 创建 VPC 资源记录（保留）
    vpcDAO.Create(ctx, vpcResource)
    // 2. 更新 VPC 状态为 creating（保留）
    vpcDAO.UpdateStatus(ctx, vpcID, "creating", "")
    // 3. 声明工作流（替代 buildTasks + BatchCreate + enqueue）
    def := &taskqueue.WorkflowDefinition{
        Name:         "create_vpc",
        ResourceType: "vpc",
        ResourceID:   vpcID,
        Metadata:     map[string]string{"az": o.az},
        Steps: []taskqueue.StepDefinition{
            {TaskType: "create_vrf_on_switch", TaskName: "创建VRF", QueueTag: "switch",   Params: params},
            {TaskType: "create_vlan_subinterface", TaskName: "创建VLAN子接口", QueueTag: "switch",   Params: params},
            {TaskType: "create_firewall_zone", TaskName: "创建防火墙安全区域", QueueTag: "firewall", Params: params},
        },
    }
    workflowID, err := o.engine.SubmitWorkflow(ctx, def)
    // SubmitWorkflow 内部自动：写 tq_workflows + tq_steps，入队首个 step
}
```

### 7.2 回调处理（切换前 vs 切换后）

**切换前**（orchestrator 内 ~80 行状态机）：
```go
func (o *AZOrchestrator) HandleTaskCallback(ctx, taskID, status, result, errorMsg) {
    taskDAO.UpdateResult(...)
    if status == completed {
        vpcDAO.IncrementCompletedTasks(...)
        nextTask := taskDAO.GetNextPendingTask(...)
        if nextTask != nil { o.enqueueTask(nextTask) }
        else { o.checkAndCompleteResource(...) }
    } else {
        vpcDAO.IncrementFailedTasks(...)
        vpcDAO.UpdateStatus(..., "failed", errorMsg)
    }
}
```

**切换后**（业务层只需实现 Hooks）：
```go
// 回调入口：一行
engine.HandleCallback(ctx, &cb)

// Hooks（在初始化时注册）：
hooks := &taskqueue.WorkflowHooks{
    OnStepComplete: func(ctx context.Context, wfID string, step *StepTask) error {
        wf, _ := engine.Store().GetWorkflow(ctx, wfID)
        if wf.ResourceType == "vpc" {
            return vpcDAO.IncrementCompletedTasks(ctx, wf.ResourceID)
        }
        return subnetDAO.IncrementCompletedTasks(ctx, wf.ResourceID)
    },
    OnWorkflowComplete: func(ctx context.Context, wf *Workflow) error {
        if wf.ResourceType == "vpc" {
            return vpcDAO.UpdateStatus(ctx, wf.ResourceID, "running", "")
        }
        return subnetDAO.UpdateStatus(ctx, wf.ResourceID, "running", "")
    },
    OnWorkflowFailed: func(ctx context.Context, wf *Workflow, errMsg string) error {
        if wf.ResourceType == "vpc" {
            return vpcDAO.UpdateStatus(ctx, wf.ResourceID, "failed", errMsg)
        }
        return subnetDAO.UpdateStatus(ctx, wf.ResourceID, "failed", errMsg)
    },
}
```

---

## 8. 收益分析

### 8.1 代码精简

| 指标 | 切换前 | 切换后 | 变化 |
|------|--------|--------|------|
| orchestrator.go (VPC) | 706 行 | ~300 行 | -57% |
| orchestrator.go (VFW) | 348 行 | ~150 行 | -57% |
| TaskDAO (db/dao) | ~280 行 | 0 行 | -100% |
| VFWTaskDAO (vfw/dao) | ~280 行 | 0 行 | -100% |
| **净减少** | | | **~860 行** |

### 8.2 能力提升

| 能力 | 切换前 | 切换后 |
|------|--------|--------|
| 自动重试 | 需自行实现 | Engine 内置（含 retry_count 管理） |
| 工作流状态查询 | 业务侧分散查询 | `engine.QueryWorkflow()` 统一查询 |
| 步骤驱动 | 手写状态机 | Engine 自动驱动 |
| 竞态保护 | 无 | `TryCompleteWorkflow` 原子操作 |
| 新增资源类型 | 复制一整套 orchestrator+DAO | 只需定义 `WorkflowDefinition` + Hooks |

### 8.3 统一性

所有使用 nsp-common 的服务将使用同一套工作流引擎，降低维护成本，统一可观测性。

---

## 9. 风险与注意事项

| 风险 | 严重程度 | 缓解措施 |
|------|---------|----------|
| nsp-common Engine 需要新增 Hooks 机制 | 中 | Hooks 改动集中在 engine.go，约 50 行，可独立 PR 先行合入 |
| nsp-common Store 需要新增 `GetWorkflowsByResourceID` | 中 | ~30 行改动，阶段一必须完成，否则状态查询 API 无法实现 |
| 数据库迁移：需创建 tq_workflows + tq_steps 表 | 低 | Engine 已提供 `Migrate()` 方法，自动建表 |
| 旧 tasks 表数据迁移 | 中 | 建议不迁移历史数据，新请求走新表，旧数据保留只读查询 |
| API 响应格式变化 | 低 | API 层做 StepTask → models.Task 转换，保持外部兼容 |
| Engine 不支持跨资源类型的 Hooks 路由 | 低 | 可在 Hooks 内通过 `workflow.ResourceType` 分发 |
| tq_workflows 和 vpc_resources 的事务一致性 | 中 | 短期：添加定时补偿任务自动修复不一致状态（~30 行）；长期：引入事务上下文 |
| 自动重试行为变化 | 中 | 确认团队接受自动重试语义；不希望重试的任务设 `MaxRetries = 0` |

---

## 10. 实施步骤

### 阶段一：nsp-common Engine 增强（前置依赖）

1. 在 `taskqueue.Engine` 中新增 `WorkflowHooks` 结构体和字段
2. 在 `handleStepSuccess` / `handleStepFailure` / `checkAndCompleteWorkflow` 中调用对应 Hook
3. 在 `Store` 接口和 `PostgresStore` 中新增 `GetWorkflowsByResourceID` 方法
4. 在 `migrations/001_init.sql` 中新增 `idx_tq_workflows_resource` 索引
5. 新增单元测试验证 Hook 调用时机和 ResourceID 查询
6. 合入 nsp-common 并发布新版本

### 阶段二：vpc_workflow_demo VPC 场景迁移

7. `cmd/az_nsp/main.go`：创建 Engine 实例，运行 `engine.Migrate()`
8. 重构 `AZOrchestrator`：用 `engine.SubmitWorkflow` 替代手动 buildTasks + enqueue
9. 实现 `WorkflowHooks`：资源状态同步（vpc_resources / subnet_resources）
10. 改造回调消费：`HandleRaw` 内调用 `engine.HandleCallback`
11. 改造状态查询 API：`engine.QueryWorkflow` + `GetWorkflowsByResourceID` + 格式转换
12. 改造 ReplayTask：调用 `engine.RetryStep`
13. 添加补偿任务：定时扫描修复 tq_workflows 和 vpc_resources 的不一致状态
14. 删除 `TaskDAO`

### 阶段三：VFW orchestrator 迁移

15. 对 `az_nsp_vfw` 重复阶段二的步骤 7-14
16. 删除 `VFWTaskDAO`

### 阶段四：验证与清理

17. 运行现有 functional test（`tests/functional/functional_test.go`）验证全链路
18. 确认旧 tasks / vfw_tasks 表不再写入后，标记为废弃
19. 更新 API 文档
20. （可选）考虑添加 feature flag 支持新旧链路切换，降低上线风险

---

## 11. 结论

**可行性：可以完全切换。**

核心前提是给 nsp-common Engine 补上两项能力（约 80 行改动）：
1. `WorkflowHooks` 回调机制（~50 行）
2. `GetWorkflowsByResourceID` 查询方法（~30 行）

完成后：

- Worker 侧**零改动**
- az_nsp 侧删除约 **860 行**手写的编排/DAO 代码
- 工作流管理统一收敛到 Engine，新增资源类型只需声明 `WorkflowDefinition` + 实现 Hooks
- API 对外兼容，可无感切换
- 自动重试能力自动获得（现有代码无此能力）
- 事务一致性风险通过补偿任务兜底

---

## 附录 A：Review 反馈采纳记录

| Review 问题 | 严重程度 | 处理方式 |
|------------|---------|---------|
| 问题 A：Store 缺少按 ResourceID 查 workflow 的方法 | 高 | **采纳**，新增为 Gap 7，纳入阶段一 |
| 问题 B：Hooks 与 Engine 的事务一致性 | 中 | **采纳**，新增为 Gap 8，短期补偿任务 + 长期事务上下文 |
| 问题 C：自动重试行为变化 | 中 | **采纳**，新增为 Gap 9，需团队确认语义变化 |
| 问题 D：enqueueStep 重复查询 workflow | 低 | **采纳**，新增为 Gap 10，不阻塞迁移，后续优化 |
| 问题 E：Workflow Metadata 的查询能力 | 低 | **采纳**，新增为 Gap 11，不阻塞迁移，长期补充 |
| nsp-common 改动量修正（50 行 → 80 行） | - | **采纳**，已更新第 6.1 节 |
| 阶段一补充 GetWorkflowsByResourceID | - | **采纳**，已更新第 10 节 |
| 迁移策略建议（feature flag、先 VPC 后 VFW） | - | **采纳**，已更新第 10 节阶段二/四 |
| 补偿机制建议 | - | **采纳**，已纳入阶段二步骤 13 |
