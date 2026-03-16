# refactor_nsp_taskqueue_proposal 代码评审

> 基于 `nsp-common/pkg/taskqueue/` 源码（engine.go、store.go、pg_store.go、task.go、broker.go）
> 和 `vpc_workflow_demo` 全部关键代码的逐行比对。

---

## 1. 总体结论

**方案可行，推荐执行。** proposal 中的 6 个 Gap 分析均与源码吻合，改动范围估算合理。

以下是代码层面的补充发现和建议。

---

## 2. Gap 验证结果

| Gap | proposal 判断 | 代码验证 | 结论 |
|-----|-------------|---------|------|
| **Gap 1: WorkflowHooks** | 需新增 ~50 行 | `handleStepSuccess`（engine.go:345）、`handleStepFailure`（engine.go:367）、`checkAndCompleteWorkflow`（engine.go:404）是精确的 Hook 插入点。这三个方法目前只操作 `tq_workflows`/`tq_steps`，不触及业务资源表 | **准确** |
| **Gap 2: 队列路由** | 无需改 nsp-common | `Engine.queueRouter`（engine.go:55）在 `NewEngine` 时已支持自定义 `QueueRouterFunc`（engine.go:80-83），`enqueueStep`（engine.go:318）调用它计算队列名 | **准确** |
| **Gap 3: 回调链路** | 改动很小 | 只需将 `HandleRaw` 内的 `server.HandleTaskCallback()` 替换为 `engine.HandleCallback(ctx, &cb)` | **准确** |
| **Gap 4: API 格式** | 方案 A 保持兼容 | `StepTask` 与 `models.Task` 字段高度对齐，转换层约 30 行 | **准确** |
| **Gap 5: VFW 独立队列** | 各自 Engine 实例 | az_nsp 和 az_nsp_vfw 是独立进程，天然隔离 | **准确** |
| **Gap 6: RetryStep** | 小改动或直接沿用 | `RetryStep`（engine.go:260-287）会重置 workflow 状态并重新入队，比现有实现更完整 | **准确** |

---

## 3. proposal 未覆盖的问题

### 问题 A：Store 缺少按 ResourceID 查 workflow 的方法

**严重程度：高（阻塞迁移）**

当前 `Store` 接口只有 `GetWorkflow(ctx, id)` 按 workflow UUID 查询。但业务 API 的入口是资源维度：

```
GET /vpc/:vpc_name/status   → 先查 vpc_resources 拿到 vpc_id → 需要用 vpc_id 查对应 workflow
POST /task/replay/:task_id  → 需要用 step_id 反查 workflow
```

`Store` 接口中没有：

```go
GetWorkflowByResourceID(ctx context.Context, resourceType, resourceID string) (*Workflow, error)
GetWorkflowsByResourceID(ctx context.Context, resourceType, resourceID string) ([]*Workflow, error)
```

**注意**：同一个资源可能有多个 workflow（例如 create_vpc 失败后 retry，或 delete_vpc），因此需要支持查询列表并按时间排序。

**建议**：在 nsp-common Store 接口和 PostgresStore 中新增以上方法，预计 ~30 行。

---

### 问题 B：Hooks 与 Engine 的事务一致性

**严重程度：中**

`handleStepSuccess`/`handleStepFailure` 中的各个 store 操作是**独立的 SQL 语句**，没有事务包裹：

```go
// engine.go:345-364 — handleStepSuccess
func (e *Engine) handleStepSuccess(...) error {
    e.store.IncrementCompletedSteps(...)  // 独立 SQL
    nextStep := e.store.GetNextPendingStep(...)  // 独立 SQL
    e.enqueueStep(...)  // 独立 SQL
}
```

如果 Hooks 也是独立的 DB 调用，**Engine 更新 `tq_steps` 和 Hooks 更新 `vpc_resources` 之间没有事务保证**。极端场景：

- Engine 标记 `tq_workflows.status = succeeded`
- Hook `OnWorkflowComplete` 执行到一半进程崩溃
- 结果：`tq_workflows` 为 succeeded，但 `vpc_resources.status` 仍为 creating

**建议**：

1. **短期**：接受最终一致。添加补偿任务——定时扫描 `tq_workflows.status = succeeded` 但对应 `vpc_resources.status != running` 的记录并修复
2. **长期**：考虑在 `handleStepSuccess`/`handleStepFailure` 中引入事务上下文，让 Hooks 可以加入同一个事务

---

### 问题 C：自动重试行为变化

**严重程度：中**

Engine 内置的自动重试逻辑（engine.go:368-388）：

```go
func (e *Engine) handleStepFailure(ctx context.Context, step *StepTask, errorMsg string) error {
    if step.RetryCount < step.MaxRetries {
        // 自动重置并重新入队
        e.store.IncrementStepRetryCount(...)
        e.store.UpdateStepStatus(..., StepStatusPending)
        e.enqueueStep(...)
        return nil
    }
    // 重试耗尽才标记 workflow failed
    ...
}
```

**行为差异**：

| | 当前 vpc_workflow_demo | 切换后 Engine |
|---|---|---|
| 任务失败时 | **立即标记 resource 为 failed**，不自动重试 | 自动重试 `MaxRetries` 次，全部耗尽才标记 failed |
| 重试控制 | 手动触发（ReplayTask API） | 自动 + 手动（RetryStep API） |

这实际上是**能力提升**，但需要团队确认是否期望这个行为。如果某些任务不希望自动重试（如涉及外部计费的操作），应将其 `MaxRetries` 设为 `0`。

---

### 问题 D：`enqueueStep` 重复查询 workflow

**严重程度：低（性能优化）**

```go
// engine.go:298-306
func (e *Engine) enqueueStep(ctx context.Context, step *StepTask) error {
    wf, err := e.store.GetWorkflow(ctx, step.WorkflowID)  // 每次 enqueue 都查一次
    ...
    payload := map[string]interface{}{
        "resource_id": wf.ResourceID,  // 只为拿这个字段
    }
}
```

而 `handleStepSuccess` 调用 `enqueueStep` 时，上下文中已经有 workflow 信息可以传递。

**建议**：后续优化时将 `enqueueStep` 改为接受 `resourceID` 参数，或缓存 workflow 信息，避免每次 enqueue 都查 DB。不阻塞迁移。

---

### 问题 E：Workflow Metadata 的查询能力

**严重程度：低**

proposal 中提到 `AZ` 字段存入 `Workflow.Metadata`。但当前 `Store` 接口没有按 metadata 查询的方法。如果后续需要"查询某个 AZ 下所有 workflow"的能力，需要扩展 Store。

当前阶段不阻塞——AZ 信息可以通过 `vpc_resources.az` 关联查询。但长期来看，给 `tq_workflows.metadata` 加 GIN 索引并提供查询方法会更完整。

---

## 4. nsp-common 改动量修正

| 内容 | proposal 估计 | 修正估计 | 说明 |
|------|-------------|---------|------|
| WorkflowHooks 结构体 + 调用 | ~50 行 | ~50 行 | 与 proposal 一致 |
| `GetWorkflowByResourceID` / `GetWorkflowsByResourceID` | 未提及 | ~30 行 | **新增**：Store 接口 + PostgresStore 实现 |
| **合计** | **~50 行** | **~80 行** | |

---

## 5. 实施建议

### 阶段排序调整

proposal 的四阶段划分合理，建议在阶段一中补充 `GetWorkflowsByResourceID`：

**阶段一（nsp-common）：**
1. 新增 `WorkflowHooks` 结构体
2. `Engine` 结构体新增 `Hooks` 字段，`NewEngine`/`NewEngineWithStore` 支持传入
3. `handleStepSuccess`、`handleStepFailure`、`checkAndCompleteWorkflow` 中调用 Hooks
4. **新增** `Store.GetWorkflowsByResourceID` 接口和 PostgresStore 实现
5. 单元测试
6. 合入并发布

**阶段二-四**：与 proposal 一致，无需调整。

### 迁移策略建议

- **不迁移历史数据**（与 proposal 一致）。旧 `tasks` 表保留只读，新请求走 `tq_workflows`/`tq_steps`
- 迁移期间可以通过 feature flag 控制新旧链路切换，降低风险
- 建议先迁移 VPC 场景（覆盖 CreateVPC + CreateSubnet + DeleteVPC + DeleteSubnet），验证通过后再迁移 VFW

### 补偿机制建议

为应对问题 B（事务一致性），建议在 az_nsp 中添加定时补偿任务：

```go
// 每 60 秒扫描一次
// 查找 tq_workflows.status = succeeded 但对应 vpc_resources.status 仍为 creating 的记录
// 自动修复 vpc_resources.status = running
```

实现简单（~30 行），可以有效兜底极端异常场景。

---

## 6. 总结

| 维度 | 评价 |
|------|------|
| 方案可行性 | **可行**，6 个 Gap 分析全部与代码吻合 |
| 收益 | 删除 ~860 行冗余代码，统一工作流引擎，获得自动重试/竞态保护等能力 |
| 风险 | 可控。最大风险是事务一致性，可通过补偿机制缓解 |
| nsp-common 改动 | ~80 行（比 proposal 估计多 ~30 行，因需新增 `GetWorkflowsByResourceID`） |
| 遗漏项 | proposal 未提及 `GetWorkflowsByResourceID`、自动重试行为变化、事务一致性补偿 |
| 建议 | **推荐执行**，按修正后的阶段排序实施 |
