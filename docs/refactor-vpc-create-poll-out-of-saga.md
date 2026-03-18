# VPC 创建重构方案：将 Poll 从 Saga 中拆出

## 1. 背景

### 当前架构（有问题）

```
CreateRegionVPC:
  Saga Submit (StepTypeAsync, 包含 Poll)
    └── Step per AZ:
          Action:  POST /api/v1/vpc           → AZ 接收任务
          Poll:    GET  /api/v1/vpc/{name}/status → 等 Worker 完成
          Compensate: DELETE /api/v1/vpc/{name}
  └── watchSagaTransaction (轮询 Saga 引擎状态)
        └── 更新 vpc_registry
```

**问题**：

1. **两层轮询冗余** — Saga 引擎轮询 AZ 状态，`watchSagaTransaction` 再轮询 Saga 引擎
2. **语义错误** — Worker 下发失败会触发 Saga 补偿（DELETE 已成功的 AZ），但 VPC 场景不需要因 Worker 失败而回滚
3. Saga 的 Poll 等待 Worker 完成才算 Step 成功，导致 Saga 事务时间长（最长 10 分钟 × AZ 数量，串行）

### 目标架构

```
CreateRegionVPC:
  Saga Submit (StepTypeSync, 无 Poll)
    └── Step per AZ:
          Action:  POST /api/v1/vpc     → AZ 返回 200 即为成功
          Compensate: DELETE /api/v1/vpc/{name}
  └── Saga 完成后:
        如果 Saga 成功 → 启动 watchAZVPCStatus (直接轮询各 AZ 接口)
        如果 Saga 失败 → 标记 VPC failed（Saga 自动补偿已成功的 AZ）
```

**改进**：

1. Saga 只管 API 调用层一致性（POST 失败则回滚）—— 快速完成
2. Worker 状态由业务层直接 Poll AZ 接口收集 —— 职责清晰
3. 消除两层轮询，只有一层语义

## 2. 涉及文件

| 文件 | 改动 |
|------|------|
| `internal/top/orchestrator/orchestrator.go` | 核心重构 |
| `internal/client/az_client.go` | 封装 `GetVPCStatus` 方法 |

不需要改动的文件：
- `internal/top/api/server.go` — 查询接口已有 DB 快路径 + AZ fallback，无需改动
- `internal/top/vpc/dao/dao.go` — DAO 方法不变
- `internal/models/` — 模型不变

## 3. 具体改动

### 3.1 orchestrator.go — `CreateRegionVPC` 改 Saga Step 类型

**文件**: `internal/top/orchestrator/orchestrator.go`
**方法**: `CreateRegionVPC` (当前第 59-157 行)

**改动点**: 第 91-107 行的 `builder.AddStep`，将 `StepTypeAsync` 改为 `StepTypeSync`，移除所有 Poll 字段。

改动前：
```go
builder.AddStep(saga.Step{
    Name:             fmt.Sprintf("创建VPC-%s", az.ID),
    Type:             saga.StepTypeAsync,           // ← 异步+轮询
    ActionMethod:     "POST",
    ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
    ActionPayload:    payloadMap,
    CompensateMethod: "DELETE",
    CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
    PollMethod:       "GET",                        // ← 移除
    PollURL:          fmt.Sprintf("..."),            // ← 移除
    PollIntervalSec:  5,                             // ← 移除
    PollMaxTimes:     120,                           // ← 移除
    PollSuccessPath:  "$.status",                    // ← 移除
    PollSuccessValue: "running",                     // ← 移除
    PollFailurePath:  "$.status",                    // ← 移除
    PollFailureValue: "failed",                      // ← 移除
})
```

改动后：
```go
builder.AddStep(saga.Step{
    Name:             fmt.Sprintf("创建VPC-%s", az.ID),
    Type:             saga.StepTypeSync,             // ← 同步：POST 返回 200 即成功
    ActionMethod:     "POST",
    ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
    ActionPayload:    payloadMap,
    CompensateMethod: "DELETE",
    CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
})
```

**语义变化**：Saga 引擎对 Sync Step 只关心 HTTP 响应码。POST 返回 2xx = Step 成功，非 2xx = Step 失败触发补偿。

### 3.2 orchestrator.go — 同步调整 Saga 超时

**位置**: 第 81 行

改动前：
```go
WithTimeout(900) // 15 分钟超时，给 worker 足够时间
```

改动后：
```go
WithTimeout(60) // 1 分钟超时，Sync 步骤只等 API 调用
```

### 3.3 orchestrator.go — 重写 `watchSagaTransaction`

**原方法**: 第 252-327 行，轮询 Saga 引擎状态。
**新方法**: 分两阶段 — 先等 Saga 完成，再直接 Poll 各 AZ 的 Worker 状态。

改动前（简化）：
```go
func (o *Orchestrator) watchSagaTransaction(txID, vpcName string, azs []*models.AZ) {
    // 每 5 秒轮询 sagaEngine.Query(txID)
    // Saga succeeded → 所有 AZ 标记 running
    // Saga failed → 标记 failed
}
```

改动后：
```go
// watchSagaAndPollAZs 分两阶段监听 VPC 创建：
//   阶段 1: 等待 Saga 事务完成（API 调用层）
//   阶段 2: 直接 Poll 各 AZ 接口收集 Worker 最终状态
func (o *Orchestrator) watchSagaAndPollAZs(txID, vpcName string, azs []*models.AZ) {
    if o.topDAO == nil || o.sagaEngine == nil {
        return
    }

    // ========== 阶段 1: 等待 Saga 完成 ==========
    sagaStatus := o.waitForSagaCompletion(txID, vpcName, azs)
    if sagaStatus != saga.TxStatusSucceeded {
        // Saga 失败 = API 调用失败，引擎已自动补偿，直接标记
        o.markVPCFromSagaFailure(txID, vpcName, azs)
        return
    }

    // ========== 阶段 2: Saga 成功后，Poll 各 AZ 的 Worker 状态 ==========
    o.pollAZWorkerStatuses(vpcName, azs)
}

// waitForSagaCompletion 轮询 Saga 引擎直到事务完结
// 返回最终的 TxStatus（succeeded / failed）
func (o *Orchestrator) waitForSagaCompletion(txID, vpcName string, azs []*models.AZ) saga.TxStatus {
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    timeout := time.After(2 * time.Minute) // Sync 步骤不需要很长

    for {
        select {
        case <-o.ctx.Done():
            // [修复] 服务关闭时使用独立 context 标记状态，避免 DB 写入失败
            dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            azDetails := make(map[string]models.AZDetail)
            for _, az := range azs {
                azDetails[az.ID] = models.AZDetail{Status: "interrupted", Error: "service shutdown"}
            }
            o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, "interrupted", azDetails)
            cancel()
            return saga.TxStatusFailed
        case <-timeout:
            logger.Info("Saga等待超时", "tx_id", txID)
            dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            azDetails := make(map[string]models.AZDetail)
            for _, az := range azs {
                azDetails[az.ID] = models.AZDetail{Status: "failed", Error: "saga timeout"}
            }
            o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, "failed", azDetails)
            cancel()
            return saga.TxStatusFailed
        case <-ticker.C:
            status, err := o.sagaEngine.Query(o.ctx, txID)
            if err != nil || status == nil {
                continue
            }
            switch saga.TxStatus(status.Status) {
            case saga.TxStatusSucceeded:
                return saga.TxStatusSucceeded
            case saga.TxStatusFailed:
                return saga.TxStatusFailed
            }
            // pending / running / compensating → 继续等
        }
    }
}

// markVPCFromSagaFailure Saga 失败时，根据各 Step 状态标记 per-AZ 详情
// 注意：此方法可能在 o.ctx 已取消时被调用（waitForSagaCompletion 的 ctx.Done 分支），
//       因此使用独立的 context.Background() 执行 DB 操作和 Saga 查询。
func (o *Orchestrator) markVPCFromSagaFailure(txID, vpcName string, azs []*models.AZ) {
    dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    status, err := o.sagaEngine.Query(dbCtx, txID)
    if err != nil || status == nil {
        return
    }
    azDetails := make(map[string]models.AZDetail)
    for i, step := range status.Steps {
        if i < len(azs) {
            d := models.AZDetail{}
            if saga.StepStatus(step.Status) == saga.StepStatusSucceeded {
                d.Status = "compensated" // API 成功但被补偿了
            } else {
                d.Status = "failed"
                d.Error = step.LastError
            }
            azDetails[azs[i].ID] = d
        }
    }
    // 注意：Steps 与 azs 的索引对应关系依赖于构建 Saga 时一个 AZ 一个 Step 按序添加，
    // 后续如果在 Steps 中插入非 AZ 相关步骤，需要改用 Step Name 中的 AZ ID 做显式映射。
    o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, "failed", azDetails)
    logger.Info("VPC Saga失败，状态已更新", "tx_id", txID, "vpc_name", vpcName)
}

// pollAZWorkerStatuses Saga 成功后，直接 Poll 各 AZ 接口收集 Worker 最终状态
// TODO: 当前对各 AZ 的查询是串行的，AZ 数量较多时可改为并发 fan-out
func (o *Orchestrator) pollAZWorkerStatuses(vpcName string, azs []*models.AZ) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    timeout := time.After(15 * time.Minute) // Worker 执行可能较慢

    // 跟踪每个 AZ 是否已到达终态
    settled := make(map[string]bool)

    for {
        select {
        case <-o.ctx.Done():
            // [修复] 服务关闭时使用独立 context 标记状态
            dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            vpc, err := o.topDAO.GetVPCByName(dbCtx, vpcName)
            if err == nil && vpc != nil {
                for _, az := range azs {
                    if !settled[az.ID] {
                        vpc.AZDetails[az.ID] = models.AZDetail{Status: "interrupted", Error: "service shutdown"}
                    }
                }
                o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, o.computeOverallStatus(vpc.AZDetails), vpc.AZDetails)
            }
            cancel()
            return
        case <-timeout:
            logger.Info("Worker状态轮询超时", "vpc_name", vpcName)
            dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            azDetails := make(map[string]models.AZDetail)
            for _, az := range azs {
                if !settled[az.ID] {
                    azDetails[az.ID] = models.AZDetail{Status: "failed", Error: "worker poll timeout"}
                }
            }
            // 仅更新未到达终态的 AZ（已到终态的保留原值）
            if len(azDetails) > 0 {
                // 先读取当前状态，合并后写入
                // TODO: Read-Modify-Write 存在竞态窗口，后续可改用 SQL jsonb_set 做原子 merge
                vpc, err := o.topDAO.GetVPCByName(dbCtx, vpcName)
                if err == nil && vpc != nil {
                    for azID, detail := range azDetails {
                        vpc.AZDetails[azID] = detail
                    }
                    o.topDAO.UpdateVPCOverallStatus(dbCtx, vpcName, o.computeOverallStatus(vpc.AZDetails), vpc.AZDetails)
                }
            }
            cancel()
            return
        case <-ticker.C:
            allSettled := true
            azDetails := make(map[string]models.AZDetail)

            for _, az := range azs {
                if settled[az.ID] {
                    continue
                }

                vpcStatus, err := o.azClient.GetVPCStatus(o.ctx, az.NSPAddr, vpcName)
                if err != nil {
                    logger.Info("查询AZ Worker状态失败", "az", az.ID, "error", err)
                    allSettled = false
                    continue
                }

                switch vpcStatus.Status {
                case models.ResourceStatusRunning:
                    azDetails[az.ID] = models.AZDetail{Status: "running"}
                    settled[az.ID] = true
                case models.ResourceStatusFailed:
                    azDetails[az.ID] = models.AZDetail{Status: "failed", Error: vpcStatus.ErrorMessage}
                    settled[az.ID] = true
                default:
                    // creating / pending → 还在执行，继续等
                    allSettled = false
                }
            }

            // 有变化就增量更新 DB
            if len(azDetails) > 0 {
                // TODO: Read-Modify-Write 存在竞态窗口，后续可改用 SQL jsonb_set 做原子 merge
                vpc, err := o.topDAO.GetVPCByName(o.ctx, vpcName)
                if err == nil && vpc != nil {
                    for azID, detail := range azDetails {
                        vpc.AZDetails[azID] = detail
                    }
                    overall := o.computeOverallStatus(vpc.AZDetails)
                    o.topDAO.UpdateVPCOverallStatus(o.ctx, vpcName, overall, vpc.AZDetails)
                }
            }

            if allSettled {
                logger.Info("所有AZ Worker已完成", "vpc_name", vpcName)
                return
            }
        }
    }
}

// computeOverallStatus 根据各 AZ 状态计算整体状态
func (o *Orchestrator) computeOverallStatus(azDetails map[string]models.AZDetail) string {
    hasFailed := false
    hasCreating := false
    hasInterrupted := false
    allRunning := true
    for _, d := range azDetails {
        switch d.Status {
        case "running":
            // ok
        case "failed":
            hasFailed = true
            allRunning = false
        case "creating":
            hasCreating = true
            allRunning = false
        case "interrupted":
            hasInterrupted = true
            allRunning = false
        default:
            // "compensated", "deleted" 等非常规状态
            allRunning = false
        }
    }
    if allRunning {
        return "running"
    }
    if hasFailed && !allRunning {
        // 部分 AZ 成功 + 部分失败 → partial_running
        // 全部失败 → failed
        hasRunning := false
        for _, d := range azDetails {
            if d.Status == "running" {
                hasRunning = true
                break
            }
        }
        if hasRunning {
            return "partial_running"
        }
        return "failed"
    }
    if hasInterrupted {
        return "interrupted"
    }
    if hasCreating {
        return "creating"
    }
    return "failed"
}
```

### 3.4 orchestrator.go — 更新 goroutine 调用

**位置**: 第 146-150 行

改动前：
```go
go func() {
    defer o.wg.Done()
    o.watchSagaTransaction(txID, req.VPCName, azs)
}()
```

改动后：
```go
go func() {
    defer o.wg.Done()
    o.watchSagaAndPollAZs(txID, req.VPCName, azs)
}()
```

### 3.5 az_client.go — 将已有的 AZ 状态查询接口封装为 `GetVPCStatus` 方法

**文件**: `internal/client/az_client.go`

> **说明**: AZ 端的 `GET /api/v1/vpc/{name}/status` 接口已经存在，当前有两处在调用它：
> 1. Saga 引擎通过 Step 的 `PollURL` 直接 HTTP 调用（orchestrator.go 第 100 行）
> 2. `getVPCStatus` 降级路径通过 `tracedHTTP.Get()` 直接调用（server.go 第 270 行）
>
> 但 `az_client.go` 中尚未封装此方法。这里不是新建 AZ 端接口，而是将已有接口统一封装进 `AZNSPClient`，使 `pollAZWorkerStatuses` 可以通过 `o.azClient.GetVPCStatus()` 调用，保持风格一致。

在现有方法后新增：

```go
// GetVPCStatus 查询指定 AZ 的 VPC Worker 状态
func (c *AZNSPClient) GetVPCStatus(ctx context.Context, azAddr string, vpcName string) (*models.VPCStatusResponse, error) {
	url := fmt.Sprintf("%s/api/v1/vpc/%s/status", azAddr, vpcName)

	var resp *http.Response
	var err error
	if c.tracedClient != nil {
		resp, err = c.tracedClient.Get(ctx, url)
	} else {
		var httpReq *http.Request
		httpReq, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
		}
		resp, err = c.httpClient.Do(httpReq)
	}

	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("VPC %s not found in AZ", vpcName)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("查询失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var vpcStatus models.VPCStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&vpcStatus); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &vpcStatus, nil
}
```

### 3.6 删除旧方法

删除原 `watchSagaTransaction` 方法（第 252-327 行），已被 `watchSagaAndPollAZs` 替代。

## 4. 改动总结

| 改动 | 文件 | 说明 |
|------|------|------|
| Step 类型 Async → Sync | orchestrator.go | Saga 只管 API 调用成功/失败 |
| Saga 超时 900s → 60s | orchestrator.go | Sync 步骤无需长等待 |
| 删除 `watchSagaTransaction` | orchestrator.go | 被新方法替代 |
| 新增 `watchSagaAndPollAZs` | orchestrator.go | 两阶段：等Saga + Poll AZ |
| 新增 `waitForSagaCompletion` | orchestrator.go | 阶段1：等 Saga 完结 |
| 新增 `markVPCFromSagaFailure` | orchestrator.go | Saga 失败时标记各 AZ |
| 新增 `pollAZWorkerStatuses` | orchestrator.go | 阶段2：直接 Poll AZ 接口 |
| 新增 `computeOverallStatus` | orchestrator.go | 汇总各 AZ 状态（含 partial_running） |
| 新增 `GetVPCStatus` | az_client.go | 封装 AZ 状态查询 HTTP 调用 |

## 5. 架构对比

### Before

```
             Saga 引擎 (内部)
             ┌─────────────────────────────┐
Submit ────► │ POST AZ-1 → Poll AZ-1 状态   │
             │ POST AZ-2 → Poll AZ-2 状态   │  ← 第一层轮询 (Saga内)
             └──────────────┬──────────────┘
                            │
         watchSagaTransaction                   ← 第二层轮询 (业务层)
         (每5s查询 sagaEngine.Query)
                            │
                            ▼
                     vpc_registry
```

### After

```
             Saga 引擎 (内部)
             ┌───────────────────┐
Submit ────► │ POST AZ-1 → 200 ✓ │  ← 同步，秒级完成
             │ POST AZ-2 → 200 ✓ │
             └────────┬──────────┘
                      │
       waitForSagaCompletion (短暂等待)
                      │
              Saga succeeded?
              ├── No  → markVPCFromSagaFailure → 结束
              │
              └── Yes → pollAZWorkerStatuses
                        ├── GET AZ-1 /vpc/{name}/status ─┐
                        └── GET AZ-2 /vpc/{name}/status ─┤  ← 唯一一层轮询
                                                         ▼
                                                  vpc_registry
```

## 6. 查询 VPC 状态接口（无需改动）

`getVPCStatus` handler (server.go 第 211-333 行) 已有正确实现：
- **快路径**: 读 `vpc_registry` 表，直接返回 DB 中的状态（由 `pollAZWorkerStatuses` 持续更新）
- **降级路径**: 如果 DB 不可用，fan-out 查询各 AZ 接口

重构后 `pollAZWorkerStatuses` 会持续将 AZ Worker 状态写入 DB，查询接口从 DB 读即可获得最新状态，逻辑完全一致。

## 7. Partial Failure 处理策略

重构后 Saga 不再因 Worker 失败触发全局回滚，因此系统可能出现"部分 AZ 成功、部分 AZ 失败"的状态。需要明确处理策略。

### 7.1 状态定义

| 整体状态 | 含义 | 触发条件 |
|----------|------|----------|
| `creating` | 创建中 | 有 AZ 仍在执行 Worker |
| `running` | 全部成功 | 所有 AZ 状态为 running |
| `partial_running` | 部分成功 | 至少一个 AZ running + 至少一个 AZ failed |
| `failed` | 全部失败 | 所有 AZ 均 failed |
| `interrupted` | 服务中断 | 服务关闭时有 AZ 尚未到达终态 |

### 7.2 各状态的后续处理

**`partial_running`**：
- `pollAZWorkerStatuses` 完成后记录详细的 per-AZ 状态到 `vpc_registry.az_details`
- 失败的 AZ 可通过已有的 AZ 级 `/task/replay/:task_id` 接口重试 Worker
- 运维可通过查询接口看到具体哪些 AZ 失败及失败原因，决定是重试还是手动清理
- 不自动清理已成功的 AZ（运维决策）

**`interrupted`**：
- 服务重启后，运维可通过查询接口发现处于 `interrupted` 状态的 VPC
- 手动触发重新查询各 AZ 状态，或等待下一次创建覆盖

**`failed`（全部失败）**：
- 记录失败原因，可批量重试或清理

### 7.3 后续演进（不在本次范围）

- 补偿任务：后台定期扫描 `partial_running` / `interrupted` 状态的 VPC，自动重试失败的 AZ
- 告警集成：`partial_running` 状态触发运维告警

## 8. 已知限制与后续优化

| 优先级 | 问题 | 当前处理 | 后续优化方向 |
|--------|------|----------|-------------|
| P1 | Read-Modify-Write 竞态 | 标注 TODO，当前每个 VPC 只有一个 goroutine 在 poll，实际风险低 | DAO 层用 SQL `jsonb_set` 做原子 merge |
| P2 | AZ 串行轮询 | 标注 TODO，当前 AZ 数量 2-3 个可接受 | 改为并发 fan-out 查询 |
| P2 | Step 与 AZ 索引对应 | 依赖构建顺序，标注注释提醒 | Step Name/Payload 中携带 AZ ID 做显式映射 |
