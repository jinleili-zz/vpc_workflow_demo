# VPC 状态查询优化方案：从扇出查询改为 DB 直查

## 1. 背景与问题

### 1.1 当前架构的三层异步链路

```
用户 ──▶ top-nsp ──SAGA(HTTP)──▶ az-nsp ──入库即返回200──▶ top-nsp 认为 step 成功
                                    │
                                    ▼ (异步消息队列)
                                 worker ──实际设备配置──▶ 回调 az-nsp 更新状态
```

- **top-nsp**: Region 级编排，通过 SAGA 引擎协调多 AZ 的 VPC 创建
- **az-nsp**: AZ 级服务，校验请求、入库后立即返回，任务通过消息队列异步交给 worker
- **worker**: 实际执行设备配置（交换机、防火墙等），完成后回调 az-nsp 更新状态

### 1.2 存在的问题

**问题一：SAGA 用 `StepTypeSync`，`az-nsp` 返回 200 就算 step 成功**

当前 `orchestrator.go:80-88`：
```go
builder.AddStep(saga.Step{
    Type: saga.StepTypeSync,  // ← az-nsp 返回200即成功，不等 worker 完成
    ...
})
```

这意味着 **SAGA "succeeded" ≠ VPC 真正创建完成**。worker 可能还在配置设备，甚至可能失败，但 SAGA 已经认为事务成功了。

**问题二：`getVPCStatus` 每次扇出 HTTP 请求到所有 AZ**

当前 `server.go:209-304` 的 `getVPCStatus`：
- 从 registry 获取所有 AZ 列表
- 对每个 AZ 并行发起 `GET /api/v1/vpc/{name}/status`
- 聚合结果返回

问题：网络延迟高、AZ 不可达时状态为 unknown、无法离线查询。

**问题三：`vpc_registry` 表状态永远停在 `"creating"`**

`CreateRegionVPC` 在第 104-122 行预注册 VPC 时写入 `status="creating"`，但 SAGA 完成后没有任何机制回写状态。

## 2. 方案总览

**核心思路**：
1. 将 SAGA Step 从 `StepTypeSync` 改为 `StepTypeAsync` + Poll，让 SAGA 真正等待 worker 完成
2. 启动后台 goroutine 监听 SAGA 事务状态，完成后更新 `vpc_registry` 表
3. `getVPCStatus` 优先从 DB 查询，保留扇出作为降级

**不需要修改 nsp-common/saga 模块**。saga 包已完整实现 Async + Poll 机制（`StepTypeAsync`、`PollURL`、`Poller`、`MatchPollResult` 等全部就绪）。

### 改动概览

| 改动位置 | 文件 | 改动内容 |
|---------|------|---------|
| model | `internal/models/firewall.go` | `VPCRegistry` 结构增加 `SagaTxID` 字段 |
| migration | `deployments/docker/init-postgres.sh` 或新 migration | `vpc_registry` 表增加 `saga_tx_id` 列 |
| DAO | `internal/top/vpc/dao/dao.go` | 新增 `GetVPCsByName` 方法；`RegisterVPC` 支持 `saga_tx_id` |
| orchestrator | `internal/top/orchestrator/orchestrator.go` | SAGA Step Sync→Async；新增 `watchSagaTransaction`；预注册时写入 `saga_tx_id` |
| API | `internal/top/api/server.go` | `getVPCStatus` 改为 DB 直查，保留扇出降级 |

## 3. 详细修改

### 3.1 Model：`internal/models/firewall.go`

**修改 `VPCRegistry` 结构体**（第 118-130 行）：

```go
// 当前代码
type VPCRegistry struct {
	ID           string    `json:"id"`
	VPCName      string    `json:"vpc_name"`
	Region       string    `json:"region"`
	AZ           string    `json:"az"`
	AZVpcID      string    `json:"az_vpc_id"`
	VRFName      string    `json:"vrf_name"`
	VLANId       int       `json:"vlan_id"`
	FirewallZone string    `json:"firewall_zone"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// 改为（在 Status 之后增加 SagaTxID）
type VPCRegistry struct {
	ID           string    `json:"id"`
	VPCName      string    `json:"vpc_name"`
	Region       string    `json:"region"`
	AZ           string    `json:"az"`
	AZVpcID      string    `json:"az_vpc_id"`
	VRFName      string    `json:"vrf_name"`
	VLANId       int       `json:"vlan_id"`
	FirewallZone string    `json:"firewall_zone"`
	Status       string    `json:"status"`
	SagaTxID     string    `json:"saga_tx_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
```

### 3.2 数据库迁移

新建迁移文件 `internal/db/migrations/003_add_saga_tx_id.sql`：

```sql
-- vpc_registry 表增加 saga_tx_id 列，用于关联 SAGA 事务
ALTER TABLE vpc_registry ADD COLUMN IF NOT EXISTS saga_tx_id VARCHAR(64) DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_vpc_registry_saga_tx_id ON vpc_registry(saga_tx_id);
```

同时更新 `deployments/docker/init-postgres.sh` 中 `top_nsp_vpc` 数据库的 `vpc_registry` 建表语句，在 `status` 之后增加 `saga_tx_id VARCHAR(64) DEFAULT ''`。

### 3.3 DAO：`internal/top/vpc/dao/dao.go`

#### 3.3.1 修改 `RegisterVPC` 方法（第 23-40 行）

SQL INSERT 增加 `saga_tx_id` 字段：

```go
func (d *TopVPCDAO) RegisterVPC(ctx context.Context, vpc *models.VPCRegistry) error {
	query := `
		INSERT INTO vpc_registry (id, vpc_name, region, az, az_vpc_id, vrf_name, vlan_id, firewall_zone, status, saga_tx_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (vpc_name, az) DO UPDATE SET
		az_vpc_id = EXCLUDED.az_vpc_id,
		vrf_name = EXCLUDED.vrf_name,
		vlan_id = EXCLUDED.vlan_id,
		firewall_zone = EXCLUDED.firewall_zone,
		status = EXCLUDED.status,
		saga_tx_id = EXCLUDED.saga_tx_id,
		updated_at = CURRENT_TIMESTAMP
	`
	_, err := d.db.ExecContext(ctx, query,
		vpc.ID, vpc.VPCName, vpc.Region, vpc.AZ, vpc.AZVpcID,
		vpc.VRFName, vpc.VLANId, vpc.FirewallZone, vpc.Status, vpc.SagaTxID,
	)
	return err
}
```

#### 3.3.2 修改所有涉及 `vpc_registry` SELECT 的方法

以下方法的 SELECT 列表和 Scan 都需要加上 `saga_tx_id`：
- `GetVPCByNameAndAZ`（第 48-63 行）
- `GetVPCsByZone`（第 65-89 行）
- `ListAllVPCs`（第 208-233 行）

示例（`GetVPCByNameAndAZ`）：

```go
func (d *TopVPCDAO) GetVPCByNameAndAZ(ctx context.Context, vpcName, az string) (*models.VPCRegistry, error) {
	query := `
		SELECT id, vpc_name, region, az, az_vpc_id, vrf_name, vlan_id, firewall_zone, status, saga_tx_id, created_at, updated_at
		FROM vpc_registry WHERE vpc_name = $1 AND az = $2
	`
	vpc := &models.VPCRegistry{}
	err := d.db.QueryRowContext(ctx, query, vpcName, az).Scan(
		&vpc.ID, &vpc.VPCName, &vpc.Region, &vpc.AZ, &vpc.AZVpcID,
		&vpc.VRFName, &vpc.VLANId, &vpc.FirewallZone, &vpc.Status,
		&vpc.SagaTxID, &vpc.CreatedAt, &vpc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return vpc, nil
}
```

对 `GetVPCsByZone` 和 `ListAllVPCs` 做同样修改（SELECT 加列、Scan 加字段）。

#### 3.3.3 新增 `GetVPCsByName` 方法

在 `dao.go` 中新增，用于 `getVPCStatus` 查询某个 VPC 在所有 AZ 的状态：

```go
// GetVPCsByName 根据 vpc_name 查询所有 AZ 的 VPC 注册记录
func (d *TopVPCDAO) GetVPCsByName(ctx context.Context, vpcName string) ([]*models.VPCRegistry, error) {
	query := `
		SELECT id, vpc_name, region, az, az_vpc_id, vrf_name, vlan_id,
		       firewall_zone, status, saga_tx_id, created_at, updated_at
		FROM vpc_registry WHERE vpc_name = $1 AND status != 'deleted'
		ORDER BY az
	`
	rows, err := d.db.QueryContext(ctx, query, vpcName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vpcs []*models.VPCRegistry
	for rows.Next() {
		vpc := &models.VPCRegistry{}
		if err := rows.Scan(
			&vpc.ID, &vpc.VPCName, &vpc.Region, &vpc.AZ, &vpc.AZVpcID,
			&vpc.VRFName, &vpc.VLANId, &vpc.FirewallZone, &vpc.Status,
			&vpc.SagaTxID, &vpc.CreatedAt, &vpc.UpdatedAt,
		); err != nil {
			return nil, err
		}
		vpcs = append(vpcs, vpc)
	}
	return vpcs, rows.Err()
}
```

### 3.4 Orchestrator：`internal/top/orchestrator/orchestrator.go`

#### 3.4.1 SAGA Step 从 Sync 改为 Async + Poll

**修改 `CreateRegionVPC` 方法中的 `builder.AddStep`（第 80-88 行）**：

```go
// 当前代码
builder.AddStep(saga.Step{
    Name:             fmt.Sprintf("创建VPC-%s", az.ID),
    Type:             saga.StepTypeSync,
    ActionMethod:     "POST",
    ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
    ActionPayload:    payloadMap,
    CompensateMethod: "DELETE",
    CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
})

// 改为
builder.AddStep(saga.Step{
    Name:             fmt.Sprintf("创建VPC-%s", az.ID),
    Type:             saga.StepTypeAsync,
    ActionMethod:     "POST",
    ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
    ActionPayload:    payloadMap,
    CompensateMethod: "DELETE",
    CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
    PollMethod:       "GET",
    PollURL:          fmt.Sprintf("%s/api/v1/vpc/%s/status", az.NSPAddr, req.VPCName),
    PollIntervalSec:  5,
    PollMaxTimes:     120,
    PollSuccessPath:  "$.status",
    PollSuccessValue: "running",
    PollFailurePath:  "$.status",
    PollFailureValue: "failed",
})
```

**原理**：
- SAGA 发出 `POST /api/v1/vpc` → az-nsp 入库返回 200 → step 进入 `polling` 状态
- SAGA Poller 每 5 秒调用 `GET /api/v1/vpc/{name}/status`
- 当 worker 完成后 az-nsp 的 VPC status 变为 `"running"` → poll 匹配 `$.status == "running"` → step 成功
- 当 worker 失败后 az-nsp 的 VPC status 变为 `"failed"` → poll 匹配 `$.status == "failed"` → step 失败 → SAGA 触发补偿
- 超过 120 次（10 分钟）仍未完成 → 超时 → 触发补偿

**对应的 SAGA timeout 也需要加大**（第 71-73 行）：

```go
// 当前：300秒（5分钟）
// 要改大，因为 Async 步骤需等 worker 完成
builder := saga.NewSaga(fmt.Sprintf("region-vpc-create-%s", req.VPCName)).
    WithPayload(map[string]any{"vpc_name": req.VPCName, "region": req.Region}).
    WithTimeout(900)  // 改为 15 分钟，给 worker 足够时间
```

#### 3.4.2 预注册 VPC 时写入 `saga_tx_id`

**修改 `CreateRegionVPC` 第 107-117 行**：

```go
// 当前代码
vpcReg := &models.VPCRegistry{
    ID:           uuid.New().String(),
    VPCName:      req.VPCName,
    Region:       req.Region,
    AZ:           az.ID,
    AZVpcID:      "",
    VRFName:      req.VRFName,
    VLANId:       req.VLANId,
    FirewallZone: req.FirewallZone,
    Status:       "creating",
}

// 改为
vpcReg := &models.VPCRegistry{
    ID:           uuid.New().String(),
    VPCName:      req.VPCName,
    Region:       req.Region,
    AZ:           az.ID,
    AZVpcID:      "",
    VRFName:      req.VRFName,
    VLANId:       req.VLANId,
    FirewallZone: req.FirewallZone,
    Status:       "creating",
    SagaTxID:     txID,
}
```

#### 3.4.3 新增 `watchSagaTransaction` 方法

在 `CreateRegionVPC` 的 return 之前，启动后台 goroutine：

```go
// 在 return 之前添加
go o.watchSagaTransaction(ctx, txID, req.VPCName, azs)

return &models.VPCResponse{
    Success:    true,
    Message:    fmt.Sprintf("VPC创建任务已提交，事务ID: %s", txID),
    WorkflowID: txID,
}, nil
```

新增方法（在文件末尾添加）：

```go
// watchSagaTransaction 后台监听 SAGA 事务状态，完成后回写 vpc_registry
func (o *Orchestrator) watchSagaTransaction(ctx context.Context, txID, vpcName string, azs []*models.AZ) {
	if o.topDAO == nil || o.sagaEngine == nil {
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeout := time.After(15 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			logger.Info("SAGA监听超时，标记VPC失败", "tx_id", txID, "vpc_name", vpcName)
			for _, az := range azs {
				o.topDAO.UpdateVPCStatus(ctx, vpcName, az.ID, "failed")
			}
			return
		case <-ticker.C:
			status, err := o.sagaEngine.Query(ctx, txID)
			if err != nil || status == nil {
				continue
			}

			switch status.Status {
			case "succeeded":
				for _, az := range azs {
					o.topDAO.UpdateVPCStatus(ctx, vpcName, az.ID, "running")
				}
				logger.Info("VPC创建完成，状态已更新", "tx_id", txID, "vpc_name", vpcName)
				return

			case "failed":
				for i, step := range status.Steps {
					if i < len(azs) {
						if step.Status == "succeeded" {
							o.topDAO.UpdateVPCStatus(ctx, vpcName, azs[i].ID, "running")
						} else {
							o.topDAO.UpdateVPCStatus(ctx, vpcName, azs[i].ID, "failed")
						}
					}
				}
				logger.Info("VPC创建失败，状态已更新", "tx_id", txID, "vpc_name", vpcName, "error", status.LastError)
				return
				// pending / running / compensating → 继续轮询
			}
		}
	}
}
```

需要在文件顶部 import 中确保有 `"time"` （已有）。

#### 3.4.4 新增 `GetVPCStatusFromDB` 方法（供 API 层调用）

```go
// GetVPCStatusFromDB 从 Top 层数据库查询 VPC 各 AZ 状态
func (o *Orchestrator) GetVPCStatusFromDB(ctx context.Context, vpcName string) ([]*models.VPCRegistry, error) {
	if o.topDAO == nil {
		return nil, fmt.Errorf("topDAO is nil")
	}
	return o.topDAO.GetVPCsByName(ctx, vpcName)
}

// HasTopDAO 检查 topDAO 是否可用
func (o *Orchestrator) HasTopDAO() bool {
	return o.topDAO != nil
}
```

### 3.5 API：`internal/top/api/server.go`

**重写 `getVPCStatus` 方法（第 209-304 行）**：

DB 直查（快路径），查不到则降级扇出（慢路径）。

```go
func (s *Server) getVPCStatus(c *gin.Context) {
	vpcName := c.Param("vpc_name")
	ctx := c.Request.Context()

	// 快路径：从 Top 层数据库查询
	if s.orchestrator.HasTopDAO() {
		vpcs, err := s.orchestrator.GetVPCStatusFromDB(ctx, vpcName)
		if err == nil && len(vpcs) > 0 {
			azStatuses := make(map[string]interface{})
			overallStatus := "running"
			hasCreating := false
			hasFailed := false

			for _, vpc := range vpcs {
				azStatuses[vpc.AZ] = map[string]interface{}{
					"az":     vpc.AZ,
					"status": vpc.Status,
				}
				if vpc.Status == "creating" {
					hasCreating = true
				}
				if vpc.Status == "failed" {
					hasFailed = true
				}
			}

			if hasFailed {
				overallStatus = "failed"
			} else if hasCreating {
				overallStatus = "creating"
			}

			c.JSON(http.StatusOK, gin.H{
				"vpc_name":       vpcName,
				"overall_status": overallStatus,
				"az_statuses":    azStatuses,
				"source":         "database",
			})
			return
		}
	}

	// 慢路径（降级）：扇出查询各 AZ
	// -------- 以下保留原有的扇出查询逻辑不变 --------
	azs, err := s.registry.ListAllAZs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": fmt.Sprintf("获取AZ列表失败: %v", err),
		})
		return
	}

	type AZStatus struct {
		AZ           string                  `json:"az"`
		Status       string                  `json:"status"`
		Progress     models.ResourceProgress `json:"progress"`
		ErrorMessage string                  `json:"error_message,omitempty"`
	}

	azStatuses := make(map[string]*AZStatus)
	var mu sync.Mutex
	var wg sync.WaitGroup
	overallStatus := "running"
	hasCreating := false
	hasFailed := false

	for _, az := range azs {
		wg.Add(1)
		go func(az *models.AZ) {
			defer wg.Done()

			statusURL := fmt.Sprintf("%s/api/v1/vpc/%s/status", az.NSPAddr, vpcName)
			resp, err := s.tracedHTTP.Get(ctx, statusURL)
			if err != nil {
				logger.InfoContext(ctx, "查询AZ的VPC状态失败", "az", az.ID, "error", err)
				mu.Lock()
				azStatuses[az.ID] = &AZStatus{
					AZ:           az.ID,
					Status:       "unknown",
					ErrorMessage: fmt.Sprintf("查询失败: %v", err),
				}
				mu.Unlock()
				return
			}

			if resp.StatusCode == http.StatusNotFound {
				resp.Body.Close()
				mu.Lock()
				azStatuses[az.ID] = &AZStatus{
					AZ:     az.ID,
					Status: "not_found",
				}
				mu.Unlock()
				return
			}

			var vpcStatus models.VPCStatusResponse
			if err := json.NewDecoder(resp.Body).Decode(&vpcStatus); err != nil {
				resp.Body.Close()
				logger.InfoContext(ctx, "解析AZ的VPC状态失败", "az", az.ID, "error", err)
				return
			}
			resp.Body.Close()

			mu.Lock()
			azStatuses[az.ID] = &AZStatus{
				AZ:           az.ID,
				Status:       string(vpcStatus.Status),
				Progress:     vpcStatus.Progress,
				ErrorMessage: vpcStatus.ErrorMessage,
			}
			if vpcStatus.Status == models.ResourceStatusCreating {
				hasCreating = true
			}
			if vpcStatus.Status == models.ResourceStatusFailed {
				hasFailed = true
			}
			mu.Unlock()
		}(az)
	}

	wg.Wait()

	if hasFailed {
		overallStatus = "failed"
	} else if hasCreating {
		overallStatus = "creating"
	}

	c.JSON(http.StatusOK, gin.H{
		"vpc_name":       vpcName,
		"overall_status": overallStatus,
		"az_statuses":    azStatuses,
		"source":         "fallback",
	})
}
```

## 4. 改动后的完整时序图

```
用户                  top-nsp              SAGA Engine           az-nsp              worker
 │                      │                      │                    │                   │
 ├── createVPC ────────▶│                      │                    │                   │
 │                      ├── 预注册vpc_registry  │                    │                   │
 │                      │   status=creating     │                    │                   │
 │                      │   saga_tx_id=txID     │                    │                   │
 │                      ├── Submit(Async) ─────▶│                    │                   │
 │                      │                      ├── POST /vpc ──────▶│                   │
 │                      │                      │                    ├── 校验+入库        │
 │                      │                      │                    │   status=creating  │
 │                      │                      │◀── 200 ───────────┤                   │
 │                      │                      │                    ├── 任务入队 ────────▶│
 │                      │                      │  [进入polling]     │                   │
 │◀─ {txID} ───────────┤                      │                    │                   │
 │                      │                      │                    │                   │
 │                      ├─ go watchSaga(txID)   │                    │                   │
 │                      │                      │                    │                   │
 │                      │                      ├── GET /vpc/status ▶│   worker配置中...   │
 │                      │                      │◀── creating ──────┤                   │
 │                      │                      │  [继续polling]     │                   │
 │                      │                      │                    │                   │
 │                      │                      │                    │◀── 设备配置完成 ───┤
 │                      │                      │                    ├── 更新             │
 │                      │                      │                    │   status=running   │
 │                      │                      │                    │                   │
 │                      │                      ├── GET /vpc/status ▶│                   │
 │                      │                      │◀── running ───────┤                   │
 │                      │                      │  [step成功]        │                   │
 │                      │                      │  [→下一个AZ...]     │                   │
 │                      │                      │                    │                   │
 │                      │                      │  ... 所有AZ完成 ... │                   │
 │                      │                      │  SAGA → succeeded  │                   │
 │                      │                      │                    │                   │
 │                      │  watchSaga查到        │                    │                   │
 │                      │  status=succeeded     │                    │                   │
 │                      ├── UPDATE vpc_registry │                    │                   │
 │                      │   每个AZ→running      │                    │                   │
 │                      │                      │                    │                   │
 │                      │                      │                    │                   │
 ├── getVPCStatus ─────▶│                      │                    │                   │
 │                      ├── SELECT vpc_registry │                    │                   │
 │◀─ {running} ────────┤  (DB直查,无需HTTP扇出) │                    │                   │
```

## 5. 注意事项

### 5.1 SAGA 串行执行

当前 SAGA 引擎不支持并行 step，AZ 之间是串行执行的（AZ1 的 worker 完成后才开始 AZ2）。改为 Async 后总耗时 = 各 AZ worker 时间之和。如果需要并行需修改 saga 模块，建议作为后续优化。

### 5.2 PollURL 可用性

`PollURL` 使用 `GET /api/v1/vpc/{name}/status`，这是 az-nsp 已有的接口（`internal/az/api/server.go:83`），返回 `VPCStatusResponse`：
```json
{
  "vpc_id": "...",
  "vpc_name": "...",
  "az": "...",
  "status": "creating" | "running" | "failed",  // ← Poll 匹配的字段
  "progress": {"total": 5, "completed": 3, "failed": 0, "pending": 2},
  "tasks": [...],
  "error_message": "..."
}
```

SAGA Poller 使用 `$.status` 提取顶层 `status` 字段，匹配 `"running"` 或 `"failed"`，无需任何 az-nsp 侧的改动。

### 5.3 `watchSagaTransaction` 的 context

`watchSagaTransaction` 必须使用 `main()` 传入的长生命周期 ctx，而不是 HTTP request 的 ctx。当前代码中 `CreateRegionVPC` 接收的 `ctx` 来自 `c.Request.Context()`，在 HTTP 请求返回后会被 cancel。

**需要修改**：`watchSagaTransaction` 应该使用独立的 context，或者在 `Orchestrator` 中保存一个长生命周期的 context。

推荐做法是在 `Orchestrator` 中增加一个 `ctx` 字段：

```go
type Orchestrator struct {
	ctx        context.Context  // 新增：长生命周期 context
	registry   *registry.Registry
	azClient   *client.AZNSPClient
	topDAO     *topdao.TopVPCDAO
	sagaEngine *saga.Engine
	tracedHTTP *trace.TracedClient
}
```

在 `NewOrchestrator` 中传入 `ctx`，`watchSagaTransaction` 使用 `o.ctx` 而非方法参数的 `ctx`。

对应修改 `cmd/top_nsp/main.go` 中调用 `NewOrchestrator` 的地方（第 95 行），传入 `ctx`。

### 5.4 向后兼容

- `getVPCStatus` 保留了完整的扇出降级逻辑，DB 查不到时自动 fallback
- 响应中增加 `"source": "database"` 或 `"source": "fallback"` 字段，方便调试
- 已有的 API 接口契约不变

### 5.5 编译检查清单

改动涉及以下文件，修改完成后请确保 `go build ./...` 通过：

1. `internal/models/firewall.go` — 增加 `SagaTxID` 字段
2. `internal/top/vpc/dao/dao.go` — 所有 SELECT/Scan `vpc_registry` 的方法需加 `saga_tx_id`
3. `internal/top/orchestrator/orchestrator.go` — Async Step + watchSagaTransaction + ctx 处理
4. `internal/top/api/server.go` — getVPCStatus 改为 DB 优先
5. `cmd/top_nsp/main.go` — NewOrchestrator 传入 ctx
