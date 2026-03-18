# PCCN (Private Cloud Connection Network) 设计方案

## 1. 概述

PCCN（私有云连接网络）用于打通两个VPC之间的网络连接，**支持跨Region场景**。本方案基于现有的Saga分布式事务框架，实现PCCN的创建和删除操作，支持失败自动回滚和状态轮询。

## 2. 数据模型设计

### 2.1 PCCN请求模型

```go
// internal/models/types.go

// VPCRef VPC引用（支持跨Region）
type VPCRef struct {
    VPCName string `json:"vpc_name" binding:"required"` // VPC名称
    Region  string `json:"region" binding:"required"`   // VPC所属Region
}

// PCCNRequest PCCN创建请求
type PCCNRequest struct {
    PCCNName string `json:"pccn_name" binding:"required"` // PCCN名称
    VPC1     VPCRef `json:"vpc1" binding:"required"`      // VPC1引用（含Region）
    VPC2     VPCRef `json:"vpc2" binding:"required"`      // VPC2引用（含Region）
}

// PCCNResponse PCCN创建响应
type PCCNResponse struct {
    Success    bool   `json:"success"`
    Message    string `json:"message"`
    PCCNID     string `json:"pccn_id,omitempty"`      // PCCN唯一标识
    TxID       string `json:"tx_id,omitempty"`        // Saga事务ID（用于追踪事务状态）
}
```

**关于 `TxID` 字段说明：**
- `TxID` 是 Saga 事务的唯一标识符，由 Saga 引擎生成
- 用户可通过此 ID 查询事务执行状态（成功/失败/进行中）
- 用于问题排查：当创建失败时，可通过 TxID 追踪具体哪个步骤失败
- 与 AZ 层的 `workflow_id` 区分：
  - Top 层返回 `tx_id`（Saga 事务 ID，全局唯一）
  - AZ 层返回 `workflow_id`（本地工作流 ID，AZ 内唯一）

### 2.2 PCCN注册表模型（Top层）

```go
// internal/models/resource.go

// PCCNRegistry PCCN注册表（Top层）
type PCCNRegistry struct {
    ID           string             `json:"id"`
    PCCNName     string             `json:"pccn_name"`
    VPC1Name     string             `json:"vpc1_name"`           // VPC1名称
    VPC1Region   string             `json:"vpc1_region"`         // VPC1所属Region
    VPC2Name     string             `json:"vpc2_name"`           // VPC2名称
    VPC2Region   string             `json:"vpc2_region"`         // VPC2所属Region
    Status       string             `json:"status"`              // creating, running, failed, deleting, deleted
    TxID         string             `json:"tx_id"`               // Saga事务ID
    VPCDetails   map[string]VPCDetail `json:"vpc_details"`       // key: "{region}/{vpc_name}"
    CreatedAt    time.Time          `json:"created_at"`
    UpdatedAt    time.Time          `json:"updated_at"`
}

// VPCDetail VPC在PCCN中的详情
type VPCDetail struct {
    Region      string   `json:"region"`                // VPC所属Region
    AZs         []string `json:"azs"`                   // VPC涉及的AZ列表
    Status      string   `json:"status"`                // creating, running, failed
    Subnets     []string `json:"subnets"`               // 子网CIDR列表
    Error       string   `json:"error,omitempty"`
}
```

### 2.3 数据库表设计

```sql
-- Top层数据库
CREATE TABLE pccn_registry (
    id           VARCHAR(36) PRIMARY KEY,
    pccn_name    VARCHAR(255) UNIQUE NOT NULL,
    vpc1_name    VARCHAR(255) NOT NULL,
    vpc1_region  VARCHAR(64) NOT NULL,
    vpc2_name    VARCHAR(255) NOT NULL,
    vpc2_region  VARCHAR(64) NOT NULL,
    status       VARCHAR(32) NOT NULL DEFAULT 'creating',
    tx_id        VARCHAR(36),
    vpc_details  JSONB,
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 索引
CREATE INDEX idx_pccn_vpc1 ON pccn_registry(vpc1_name, vpc1_region);
CREATE INDEX idx_pccn_vpc2 ON pccn_registry(vpc2_name, vpc2_region);
CREATE INDEX idx_pccn_status ON pccn_registry(status);
CREATE INDEX idx_pccn_vpc1_region ON pccn_registry(vpc1_region);
CREATE INDEX idx_pccn_vpc2_region ON pccn_registry(vpc2_region);
```

## 3. API设计

### 3.1 Top NSP API

#### 创建PCCN

```
POST /api/v1/pccn
Content-Type: application/json

Request (同Region场景):
{
    "pccn_name": "pccn-test-001",
    "vpc1": {
        "vpc_name": "vpc-beijing-001",
        "region": "cn-beijing"
    },
    "vpc2": {
        "vpc_name": "vpc-beijing-002",
        "region": "cn-beijing"
    }
}

Request (跨Region场景):
{
    "pccn_name": "pccn-cross-region-001",
    "vpc1": {
        "vpc_name": "vpc-beijing-001",
        "region": "cn-beijing"
    },
    "vpc2": {
        "vpc_name": "vpc-shanghai-001",
        "region": "cn-shanghai"
    }
}

Response (Sync):
{
    "success": true,
    "message": "PCCN创建任务已提交，事务ID: xxx",
    "pccn_id": "uuid-xxx",
    "tx_id": "tx-xxx"
}
```

#### 删除PCCN

```
DELETE /api/v1/pccn/:pccn_name

Response:
{
    "success": true,
    "message": "PCCN删除任务已提交"
}
```

#### 查询PCCN状态

```
GET /api/v1/pccn/:pccn_name/status

Response (同Region):
{
    "pccn_name": "pccn-test-001",
    "overall_status": "running",
    "vpc_details": {
        "cn-beijing/vpc-beijing-001": {
            "region": "cn-beijing",
            "azs": ["cn-beijing-1a", "cn-beijing-1b"],
            "status": "running",
            "subnets": ["10.0.1.0/24", "10.0.2.0/24"]
        },
        "cn-beijing/vpc-beijing-002": {
            "region": "cn-beijing",
            "azs": ["cn-beijing-1a"],
            "status": "running",
            "subnets": ["10.1.1.0/24", "10.1.2.0/24"]
        }
    },
    "source": "database"
}

Response (跨Region):
{
    "pccn_name": "pccn-cross-region-001",
    "overall_status": "running",
    "vpc_details": {
        "cn-beijing/vpc-beijing-001": {
            "region": "cn-beijing",
            "azs": ["cn-beijing-1a", "cn-beijing-1b"],
            "status": "running",
            "subnets": ["10.0.1.0/24", "10.0.2.0/24"]
        },
        "cn-shanghai/vpc-shanghai-001": {
            "region": "cn-shanghai",
            "azs": ["cn-shanghai-1a"],
            "status": "running",
            "subnets": ["10.2.1.0/24"]
        }
    },
    "source": "database"
}
```

### 3.2 AZ NSP API

#### 创建PCCN连接（在AZ层）

```
POST /api/v1/pccn
Content-Type: application/json

Request:
{
    "pccn_id": "uuid-xxx",
    "pccn_name": "pccn-test-001",
    "vpc_name": "vpc-beijing-001",
    "vpc_region": "cn-beijing",           // 本VPC所属Region
    "peer_vpc_name": "vpc-shanghai-001",
    "peer_vpc_region": "cn-shanghai"      // 对端VPC所属Region
}

Response:
{
    "success": true,
    "message": "PCCN连接创建工作流已启动",
    "workflow_id": "wf-xxx"
}
```

#### 删除PCCN连接

```
DELETE /api/v1/pccn/:pccn_name

Response:
{
    "success": true,
    "message": "PCCN连接已删除"
}
```

#### 查询PCCN状态

```
GET /api/v1/pccn/:pccn_name/status

Response:
{
    "pccn_id": "uuid-xxx",
    "pccn_name": "pccn-test-001",
    "vpc_name": "vpc-beijing-001",
    "vpc_region": "cn-beijing",
    "peer_vpc_name": "vpc-shanghai-001",
    "peer_vpc_region": "cn-shanghai",
    "status": "running",
    "subnets": ["10.0.1.0/24", "10.0.2.0/24"],
    "progress": {
        "total": 2,
        "completed": 2,
        "failed": 0,
        "pending": 0
    }
}
```

## 4. Saga事务流程设计

### 4.1 创建PCCN的Saga流程

**核心设计**：将Poll纳入Saga事务，实现真正的失败自动回滚。

```
┌─────────────────────────────────────────────────────────────────┐
│                    Top NSP: CreatePCCN                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. 预检查阶段                                                   │
│     ├── 验证VPC1存在且状态为running (根据vpc1.region查询)        │
│     ├── 验证VPC2存在且状态为running (根据vpc2.region查询)        │
│     ├── 获取VPC1所属的AZ列表 (从vpc1.region的Registry)          │
│     ├── 获取VPC2所属的AZ列表 (从vpc2.region的Registry)          │
│     └── 健康检查所有相关AZ NSP (可能跨Region)                    │
│                                                                 │
│  2. 生成统一的PCCN ID                                            │
│                                                                 │
│  3. 构建Saga事务定义（每个AZ两个Step：提交+等待）                 │
│     ├── Step 1: 提交VPC1-AZ1创建请求 (Sync)                     │
│     ├── Step 2: 等待VPC1-AZ1创建完成 (Poll)                     │
│     ├── Step 3: 提交VPC1-AZ2创建请求 (Sync)                     │
│     ├── Step 4: 等待VPC1-AZ2创建完成 (Poll)                     │
│     ├── Step 5: 提交VPC2-AZ1创建请求 (Sync)                     │
│     ├── Step 6: 等待VPC2-AZ1创建完成 (Poll)                     │
│     └── ...                                                     │
│                                                                 │
│  4. 提交Saga事务                                                 │
│                                                                 │
│  5. 预注册PCCN到pccn_registry                                    │
│                                                                 │
│  6. Saga引擎自动执行所有Step，包括Poll等待                       │
│     ├── 所有Step成功 → PCCN创建成功                             │
│     └── 任意Step失败 → 自动执行Compensate回滚                    │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### 4.2 Saga Step类型说明

| Step类型 | 说明 | 成功条件 |
|---------|------|---------|
| Sync | 同步HTTP调用 | HTTP返回200即成功 |
| Poll | 轮询等待 | 轮询直到满足成功/失败条件 |

### 4.3 Saga步骤定义

```go
// internal/top/orchestrator/orchestrator.go

func (o *Orchestrator) CreatePCCN(ctx context.Context, req *models.PCCNRequest) (*models.PCCNResponse, error) {
    // 1. 预检查：验证两个VPC存在且状态正常（跨Region查询）
    vpc1, err := o.topDAO.GetVPCByName(ctx, req.VPC1.VPCName)
    if err != nil {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC1不存在: %v", err)}, nil
    }
    if vpc1.Region != req.VPC1.Region {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC1 Region不匹配: 请求=%s, 实际=%s", req.VPC1.Region, vpc1.Region)}, nil
    }

    vpc2, err := o.topDAO.GetVPCByName(ctx, req.VPC2.VPCName)
    if err != nil {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC2不存在: %v", err)}, nil
    }
    if vpc2.Region != req.VPC2.Region {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("VPC2 Region不匹配: 请求=%s, 实际=%s", req.VPC2.Region, vpc2.Region)}, nil
    }

    // 2. 获取两个VPC涉及的AZ（可能跨Region）
    vpc1AZs := o.getAZsFromVPCDetails(ctx, vpc1, req.VPC1.Region)
    vpc2AZs := o.getAZsFromVPCDetails(ctx, vpc2, req.VPC2.Region)

    // 3. 健康检查所有AZ（跨Region）
    for _, az := range append(vpc1AZs, vpc2AZs...) {
        if err := o.azClient.HealthCheck(ctx, az.NSPAddr); err != nil {
            return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("AZ %s 不健康", az.ID)}, nil
        }
    }

    // 4. 生成统一的PCCN ID
    pccnID := uuid.New().String()

    // 5. 构建Saga事务（每个AZ两个Step：提交请求 + Poll等待）
    builder := saga.NewSaga(fmt.Sprintf("pccn-create-%s", req.PCCNName)).
        WithPayload(map[string]any{
            "pccn_name":   req.PCCNName,
            "vpc1_name":   req.VPC1.VPCName,
            "vpc1_region": req.VPC1.Region,
            "vpc2_name":   req.VPC2.VPCName,
            "vpc2_region": req.VPC2.Region,
        }).
        WithTimeout(30 * time.Minute) // 整体超时30分钟（考虑Poll时间）

    // 为VPC1的每个AZ添加两个Step：提交 + Poll
    for _, az := range vpc1AZs {
        payload := map[string]any{
            "pccn_id":         pccnID,
            "pccn_name":       req.PCCNName,
            "vpc_name":        req.VPC1.VPCName,
            "vpc_region":      req.VPC1.Region,
            "peer_vpc_name":   req.VPC2.VPCName,
            "peer_vpc_region": req.VPC2.Region,
        }

        // Step A: 提交创建请求 (Sync)
        builder.AddStep(saga.Step{
            Name:             fmt.Sprintf("提交PCCN创建-VPC1-%s", az.ID),
            Type:             saga.StepTypeSync,
            ActionMethod:     "POST",
            ActionURL:        fmt.Sprintf("%s/api/v1/pccn", az.NSPAddr),
            ActionPayload:    payload,
            CompensateMethod: "DELETE",
            CompensateURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, req.PCCNName),
        })

        // Step B: Poll等待创建完成 (Poll)
        builder.AddStep(saga.Step{
            Name:             fmt.Sprintf("等待PCCN创建完成-VPC1-%s", az.ID),
            Type:             saga.StepTypePoll,
            PollURL:          fmt.Sprintf("%s/api/v1/pccn/%s/status", az.NSPAddr, req.PCCNName),
            PollInterval:     5 * time.Second,
            PollTimeout:      15 * time.Minute,
            SuccessCondition: "$.status == 'running'",
            FailureCondition: "$.status == 'failed'",
            // Poll失败时，触发前面Sync Step的Compensate
            CompensateMethod: "DELETE",
            CompensateURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, req.PCCNName),
        })
    }

    // 为VPC2的每个AZ添加两个Step：提交 + Poll
    for _, az := range vpc2AZs {
        payload := map[string]any{
            "pccn_id":         pccnID,
            "pccn_name":       req.PCCNName,
            "vpc_name":        req.VPC2.VPCName,
            "vpc_region":      req.VPC2.Region,
            "peer_vpc_name":   req.VPC1.VPCName,
            "peer_vpc_region": req.VPC1.Region,
        }

        // Step A: 提交创建请求 (Sync)
        builder.AddStep(saga.Step{
            Name:             fmt.Sprintf("提交PCCN创建-VPC2-%s", az.ID),
            Type:             saga.StepTypeSync,
            ActionMethod:     "POST",
            ActionURL:        fmt.Sprintf("%s/api/v1/pccn", az.NSPAddr),
            ActionPayload:    payload,
            CompensateMethod: "DELETE",
            CompensateURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, req.PCCNName),
        })

        // Step B: Poll等待创建完成 (Poll)
        builder.AddStep(saga.Step{
            Name:             fmt.Sprintf("等待PCCN创建完成-VPC2-%s", az.ID),
            Type:             saga.StepTypePoll,
            PollURL:          fmt.Sprintf("%s/api/v1/pccn/%s/status", az.NSPAddr, req.PCCNName),
            PollInterval:     5 * time.Second,
            PollTimeout:      15 * time.Minute,
            SuccessCondition: "$.status == 'running'",
            FailureCondition: "$.status == 'failed'",
            CompensateMethod: "DELETE",
            CompensateURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, req.PCCNName),
        })
    }

    def, err := builder.Build()
    if err != nil {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("构建Saga定义失败: %v", err)}, nil
    }

    // 6. 提交Saga事务
    txID, err := o.sagaEngine.Submit(ctx, def)
    if err != nil {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("提交Saga事务失败: %v", err)}, nil
    }

    // 7. 预注册PCCN（包含跨Region信息）
    vpcDetails := make(map[string]models.VPCDetail)
    vpc1Key := fmt.Sprintf("%s/%s", req.VPC1.Region, req.VPC1.VPCName)
    vpc2Key := fmt.Sprintf("%s/%s", req.VPC2.Region, req.VPC2.VPCName)

    vpc1AZIDs := make([]string, len(vpc1AZs))
    for i, az := range vpc1AZs {
        vpc1AZIDs[i] = az.ID
    }
    vpc2AZIDs := make([]string, len(vpc2AZs))
    for i, az := range vpc2AZs {
        vpc2AZIDs[i] = az.ID
    }

    vpcDetails[vpc1Key] = models.VPCDetail{
        Region: req.VPC1.Region,
        AZs:    vpc1AZIDs,
        Status: "creating",
    }
    vpcDetails[vpc2Key] = models.VPCDetail{
        Region: req.VPC2.Region,
        AZs:    vpc2AZIDs,
        Status: "creating",
    }

    pccnReg := &models.PCCNRegistry{
        ID:         pccnID,
        PCCNName:   req.PCCNName,
        VPC1Name:   req.VPC1.VPCName,
        VPC1Region: req.VPC1.Region,
        VPC2Name:   req.VPC2.VPCName,
        VPC2Region: req.VPC2.Region,
        Status:     "creating",
        TxID:       txID,
        VPCDetails: vpcDetails,
    }
    o.pccnDAO.RegisterPCCN(ctx, pccnReg)

    // 8. 启动后台goroutine监听Saga状态，更新数据库
    o.wg.Add(1)
    go func() {
        defer o.wg.Done()
        o.watchPCCNSagaCompletion(txID, req.PCCNName, vpc1AZs, vpc2AZs, req.VPC1, req.VPC2)
    }()

    return &models.PCCNResponse{
        Success:  true,
        Message:  fmt.Sprintf("PCCN创建任务已提交，事务ID: %s", txID),
        PCCNID:   pccnID,
        TxID:     txID,
    }, nil
}
```

### 4.3 失败回滚机制

当Saga事务中任一步骤失败时，Saga引擎自动执行补偿：

```
┌─────────────────────────────────────────────────────────────────┐
│                    Saga 失败回滚流程                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  假设 Step 2 (VPC2-AZ1) 失败:                                   │
│                                                                 │
│  1. Saga引擎检测到Step 2失败                                    │
│                                                                 │
│  2. 自动触发补偿（Compensate）:                                  │
│     ├── Compensate Step 1 (VPC1-AZ1): DELETE /api/v1/pccn/xxx  │
│     └── (Step 2 未成功，无需补偿)                                │
│                                                                 │
│  3. 更新pccn_registry状态为failed                               │
│                                                                 │
│  4. 记录失败原因到VPCDetails                                     │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## 5. Saga自动回滚机制与状态更新

### 5.1 状态更新流程对比

| 场景 | VPC创建（现有） | PCCN创建（新设计） |
|------|----------------|-------------------|
| Saga Step类型 | 仅Sync | Sync + Poll |
| Poll位置 | 后台协程独立Poll | 纳入Saga事务 |
| Saga成功含义 | API调用成功 | Worker执行完成 |
| 后台协程职责 | Poll Worker状态 | 仅同步状态到DB |

### 5.2 PCCN状态更新流程

```
┌─────────────────────────────────────────────────────────────────┐
│              PCCN状态更新流程（参考watchSagaAndPollAZs）         │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ AZ层状态更新（pccn_resources）                           │   │
│  ├─────────────────────────────────────────────────────────┤   │
│  │ 1. 收到创建请求 → status = "pending"                     │   │
│  │ 2. 提交Workflow → status = "creating"                    │   │
│  │ 3. Worker执行 → WorkflowHooks → status = "running"/"failed"│  │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ Top层状态更新（pccn_registry）                           │   │
│  ├─────────────────────────────────────────────────────────┤   │
│  │ 1. 提交Saga前 → status = "creating"（预注册）            │   │
│  │ 2. 后台协程监听Saga完成 → 更新status                     │   │
│  │    ├── Saga成功 → status = "running"                    │   │
│  │    └── Saga失败 → status = "failed"                     │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### 5.3 后台协程实现

由于Poll已纳入Saga，后台协程只需监听Saga状态并同步到数据库：

```go
// internal/top/orchestrator/orchestrator.go

// watchPCCNSagaCompletion 监听PCCN Saga事务完成并更新数据库状态
// 参考: watchSagaAndPollAZs
func (o *Orchestrator) watchPCCNSagaCompletion(txID, pccnName string, vpc1AZs, vpc2AZs []*models.AZ, vpc1, vpc2 models.VPCRef) {
    if o.pccnDAO == nil || o.sagaEngine == nil {
        return
    }

    // 等待Saga完成（包含Poll Step）
    sagaStatus := o.waitForPCCNSagaCompletion(txID, pccnName)
    if sagaStatus != saga.TxStatusSucceeded {
        // Saga失败（已自动回滚），更新状态为failed
        o.markPCCNFromSagaFailure(txID, pccnName)
        return
    }

    // Saga成功，更新状态为running并收集子网信息
    o.markPCCNFromSagaSuccess(txID, pccnName, vpc1AZs, vpc2AZs, vpc1, vpc2)
}

// waitForPCCNSagaCompletion 等待Saga事务完成
func (o *Orchestrator) waitForPCCNSagaCompletion(txID, pccnName string) saga.TxStatus {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    timeout := time.After(35 * time.Minute) // 略大于Saga整体超时

    for {
        select {
        case <-o.ctx.Done():
            // 服务关闭时使用独立context标记状态
            dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "interrupted", nil)
            cancel()
            return saga.TxStatusFailed

        case <-timeout:
            logger.Info("PCCN Saga等待超时", "tx_id", txID, "pccn_name", pccnName)
            dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", nil)
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
            // pending / running / compensating → 继续等待
        }
    }
}

// markPCCNFromSagaSuccess Saga成功时更新状态
func (o *Orchestrator) markPCCNFromSagaSuccess(txID, pccnName string, vpc1AZs, vpc2AZs []*models.AZ, vpc1, vpc2 models.VPCRef) {
    dbCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    // 查询Saga事务详情，获取Poll Step的结果
    status, err := o.sagaEngine.Query(dbCtx, txID)
    if err != nil || status == nil {
        logger.Info("查询Saga状态失败", "tx_id", txID, "error", err)
        o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "running", nil)
        return
    }

    vpcDetails := make(map[string]models.VPCDetail)

    // 从Poll Step的结果中提取子网信息
    for _, step := range status.Steps {
        if !strings.Contains(step.Name, "等待PCCN创建完成") {
            continue
        }
        if saga.StepStatus(step.Status) != saga.StepStatusSucceeded {
            continue
        }

        // 解析Poll响应
        var pollResult struct {
            VPCName   string   `json:"vpc_name"`
            VPCRegion string   `json:"vpc_region"`
            Subnets   []string `json:"subnets"`
        }
        if step.Result != "" {
            if err := json.Unmarshal([]byte(step.Result), &pollResult); err != nil {
                continue
            }
        }

        // 更新VPC详情
        vpcKey := fmt.Sprintf("%s/%s", pollResult.VPCRegion, pollResult.VPCName)
        if existing, ok := vpcDetails[vpcKey]; ok {
            // 合并AZ信息
            existing.Subnets = append(existing.Subnets, pollResult.Subnets...)
            vpcDetails[vpcKey] = existing
        } else {
            vpcDetails[vpcKey] = models.VPCDetail{
                Region:  pollResult.VPCRegion,
                Status:  "running",
                Subnets: pollResult.Subnets,
            }
        }
    }

    // 更新数据库
    o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "running", vpcDetails)
    logger.Info("PCCN Saga成功，状态已更新", "tx_id", txID, "pccn_name", pccnName)
}

// markPCCNFromSagaFailure Saga失败时更新状态
func (o *Orchestrator) markPCCNFromSagaFailure(txID, pccnName string) {
    dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    status, err := o.sagaEngine.Query(dbCtx, txID)
    if err != nil || status == nil {
        o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", nil)
        return
    }

    vpcDetails := make(map[string]models.VPCDetail)

    // 根据Step状态判断失败原因
    for _, step := range status.Steps {
        vpcKey := extractVPCKeyFromStepName(step.Name)
        if vpcKey == "" {
            continue
        }

        if saga.StepStatus(step.Status) == saga.StepStatusSucceeded {
            // API成功但被补偿了
            vpcDetails[vpcKey] = models.VPCDetail{Status: "compensated"}
        } else {
            vpcDetails[vpcKey] = models.VPCDetail{
                Status: "failed",
                Error:  step.LastError,
            }
        }
    }

    o.pccnDAO.UpdatePCCNStatus(dbCtx, pccnName, "failed", vpcDetails)
    logger.Info("PCCN Saga失败，状态已更新", "tx_id", txID, "pccn_name", pccnName)
}
```

### 5.4 失败自动回滚

当Saga事务中任一步骤（包括Poll Step）失败时，Saga引擎自动执行补偿：

```
┌─────────────────────────────────────────────────────────────────┐
│                    Saga 失败自动回滚流程                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  假设 Step 4 (等待VPC1-AZ2创建完成) 的Poll检测到失败:           │
│                                                                 │
│  1. Saga引擎检测到Poll Step失败                                 │
│                                                                 │
│  2. 自动触发补偿（Compensate）:                                  │
│     ├── Compensate Step 3 (提交VPC1-AZ2创建): DELETE           │
│     ├── Compensate Step 1 (提交VPC1-AZ1创建): DELETE           │
│     └── (Step 2 Poll成功，但Step 1的Compensate仍会执行)        │
│                                                                 │
│  3. 后台协程检测到Saga失败                                       │
│                                                                 │
│  4. 更新pccn_registry状态为failed                               │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### 5.5 Poll Step配置说明

| 配置项 | 说明 | 建议值 |
|-------|------|--------|
| PollInterval | 轮询间隔 | 5秒 |
| PollTimeout | 单个AZ的Poll超时 | 15分钟 |
| SuccessCondition | 成功条件（JSONPath） | `$.status == 'running'` |
| FailureCondition | 失败条件（JSONPath） | `$.status == 'failed'` |
| Saga整体超时 | 整个事务超时 | 30分钟 |
| 后台协程超时 | 等待Saga完成的超时 | 35分钟（略大于Saga超时）|

## 6. Worker任务设计

### 6.1 任务参数定义

```go
// tasks/handlers.go

// PCCNParams PCCN任务参数
type PCCNParams struct {
    PCCNID         string `json:"pccn_id"`
    PCCNName       string `json:"pccn_name"`
    VPCName        string `json:"vpc_name"`
    VPCRegion      string `json:"vpc_region"`       // 本VPC所属Region
    PeerVPCName    string `json:"peer_vpc_name"`
    PeerVPCRegion  string `json:"peer_vpc_region"`  // 对端VPC所属Region
    AZ             string `json:"az"`
}
```

### 6.2 Worker任务Handler

```go
// tasks/pccn_handlers.go

package tasks

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    "github.com/paic/nsp-common/pkg/logger"
    "github.com/paic/nsp-common/pkg/taskqueue"
)

// CreatePCCNConnectionHandler 创建PCCN连接的Worker Handler
// 该Handler打印两个VPC的子网信息
func CreatePCCNConnectionHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
    return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
        var params PCCNParams
        if err := json.Unmarshal(tp.Params, &params); err != nil {
            return nil, fmt.Errorf("解析任务参数失败: %v", err)
        }

        logger.InfoContext(ctx, "开始创建PCCN连接",
            "pccn_name", params.PCCNName,
            "vpc_name", params.VPCName,
            "vpc_region", params.VPCRegion,
            "peer_vpc_name", params.PeerVPCName,
            "peer_vpc_region", params.PeerVPCRegion,
            "taskID", tp.TaskID,
        )

        // 模拟处理延迟
        time.Sleep(2 * time.Second)

        // 打印两个VPC的子网信息（模拟）
        // 实际实现中，这里会查询本地VPC的子网信息
        vpcSubnets := []string{
            "10.0.1.0/24",
            "10.0.2.0/24",
        }

        logger.InfoContext(ctx, "本地VPC子网信息",
            "vpc_name", params.VPCName,
            "vpc_region", params.VPCRegion,
            "subnets", vpcSubnets,
        )

        logger.InfoContext(ctx, "对端VPC信息（用于路由配置）",
            "peer_vpc_name", params.PeerVPCName,
            "peer_vpc_region", params.PeerVPCRegion,
            "is_cross_region", params.VPCRegion != params.PeerVPCRegion,
        )

        result := map[string]interface{}{
            "message":         fmt.Sprintf("PCCN连接创建成功: %s(%s) <-> %s(%s)",
                params.VPCName, params.VPCRegion,
                params.PeerVPCName, params.PeerVPCRegion),
            "pccn_id":         params.PCCNID,
            "pccn_name":       params.PCCNName,
            "vpc_name":        params.VPCName,
            "vpc_region":      params.VPCRegion,
            "peer_vpc_name":   params.PeerVPCName,
            "peer_vpc_region": params.PeerVPCRegion,
            "vpc_subnets":     vpcSubnets,
            "is_cross_region": params.VPCRegion != params.PeerVPCRegion,
            "timestamp":       time.Now().Unix(),
        }

        logger.InfoContext(ctx, "PCCN连接创建完成", "pccn_name", params.PCCNName)

        if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
            logger.InfoContext(ctx, "PCCN任务回调失败", "error", err)
            return nil, err
        }

        return &taskqueue.TaskResult{Data: result, Message: "PCCN connection created"}, nil
    }
}

// ConfigurePCCNRoutingHandler 配置PCCN路由的Worker Handler
func ConfigurePCCNRoutingHandler(cbSender *taskqueue.CallbackSender) taskqueue.HandlerFunc {
    return func(ctx context.Context, tp *taskqueue.TaskPayload) (*taskqueue.TaskResult, error) {
        var params PCCNParams
        if err := json.Unmarshal(tp.Params, &params); err != nil {
            return nil, fmt.Errorf("解析任务参数失败: %v", err)
        }

        logger.InfoContext(ctx, "开始配置PCCN路由",
            "pccn_name", params.PCCNName,
            "vpc_name", params.VPCName,
            "vpc_region", params.VPCRegion,
            "peer_vpc_region", params.PeerVPCRegion,
            "taskID", tp.TaskID,
        )

        time.Sleep(2 * time.Second)

        // 跨Region路由需要特殊处理
        isCrossRegion := params.VPCRegion != params.PeerVPCRegion
        routingType := "intra-region"
        if isCrossRegion {
            routingType = "cross-region"
        }

        // 模拟配置路由
        logger.InfoContext(ctx, "配置路由规则",
            "vpc_name", params.VPCName,
            "vpc_region", params.VPCRegion,
            "peer_vpc_name", params.PeerVPCName,
            "peer_vpc_region", params.PeerVPCRegion,
            "routing_type", routingType,
            "config_cmd", "ip route add <peer_cidr> via <pccn_gateway>",
        )

        result := map[string]interface{}{
            "message":         fmt.Sprintf("PCCN路由配置成功: %s", params.PCCNName),
            "pccn_name":       params.PCCNName,
            "vpc_name":        params.VPCName,
            "vpc_region":      params.VPCRegion,
            "peer_vpc_name":   params.PeerVPCName,
            "peer_vpc_region": params.PeerVPCRegion,
            "routing_type":    routingType,
            "timestamp":       time.Now().Unix(),
        }

        logger.InfoContext(ctx, "PCCN路由配置完成", "pccn_name", params.PCCNName, "routing_type", routingType)

        if err := cbSender.Success(ctx, tp.TaskID, result); err != nil {
            logger.InfoContext(ctx, "PCCN路由任务回调失败", "error", err)
            return nil, err
        }

        return &taskqueue.TaskResult{Data: result, Message: "PCCN routing configured"}, nil
    }
}
```

### 6.3 AZ层工作流定义

```go
// internal/az/orchestrator/orchestrator.go

// PCCNRequest AZ层PCCN请求
type PCCNRequest struct {
    PCCNID        string `json:"pccn_id" binding:"required"`
    PCCNName      string `json:"pccn_name" binding:"required"`
    VPCName       string `json:"vpc_name" binding:"required"`
    VPCRegion     string `json:"vpc_region" binding:"required"`      // 本VPC所属Region
    PeerVPCName   string `json:"peer_vpc_name" binding:"required"`
    PeerVPCRegion string `json:"peer_vpc_region" binding:"required"` // 对端VPC所属Region
}

func (o *AZOrchestrator) CreatePCCN(ctx context.Context, req *PCCNRequest) (*models.PCCNResponse, error) {
    logger.InfoContext(ctx, "开始创建PCCN连接",
        "az", o.az,
        "pccn_name", req.PCCNName,
        "vpc_name", req.VPCName,
        "vpc_region", req.VPCRegion,
        "peer_vpc_region", req.PeerVPCRegion,
    )

    // 获取VPC信息以获取子网列表
    vpc, err := o.vpcDAO.GetByName(ctx, req.VPCName, o.az)
    if err != nil {
        return &models.PCCNResponse{
            Success: false,
            Message: fmt.Sprintf("VPC不存在: %v", err),
        }, nil
    }

    // 获取VPC的子网列表
    subnets, err := o.subnetDAO.ListByVPCID(ctx, vpc.ID)
    if err != nil {
        return &models.PCCNResponse{
            Success: false,
            Message: fmt.Sprintf("获取子网列表失败: %v", err),
        }, nil
    }

    // 构建子网CIDR列表
    var subnetCIDRs []string
    for _, subnet := range subnets {
        subnetCIDRs = append(subnetCIDRs, subnet.CIDR)
    }

    // 创建PCCN资源记录
    pccnResource := &models.PCCNResource{
        ID:            req.PCCNID,
        PCCNName:      req.PCCNName,
        VPCName:       req.VPCName,
        VPCRegion:     req.VPCRegion,
        PeerVPCName:   req.PeerVPCName,
        PeerVPCRegion: req.PeerVPCRegion,
        AZ:            o.az,
        Status:        models.ResourceStatusPending,
        Subnets:       subnetCIDRs,
        TotalTasks:    0,
    }

    if err := o.pccnDAO.Create(ctx, pccnResource); err != nil {
        return &models.PCCNResponse{
            Success: false,
            Message: fmt.Sprintf("创建PCCN资源记录失败: %v", err),
        }, nil
    }

    // 构建任务参数
    params := o.buildPCCNTaskParams(ctx, req, subnetCIDRs)

    // 定义工作流
    def := &taskqueue.WorkflowDefinition{
        Name:         "create_pccn",
        ResourceType: string(models.ResourceTypePCCN),
        ResourceID:   req.PCCNID,
        Metadata:     map[string]string{"az": o.az, "vpc_region": req.VPCRegion, "peer_vpc_region": req.PeerVPCRegion},
        Steps: []taskqueue.StepDefinition{
            {
                TaskType:   "create_pccn_connection",
                TaskName:   "创建PCCN连接",
                QueueTag:   string(queue.DeviceTypeSwitch),
                Priority:   taskqueue.PriorityNormal,
                Params:     params,
            },
            {
                TaskType:   "configure_pccn_routing",
                TaskName:   "配置PCCN路由",
                QueueTag:   string(queue.DeviceTypeSwitch),
                Priority:   taskqueue.PriorityNormal,
                Params:     params,
            },
        },
    }

    if err := o.pccnDAO.UpdateTotalTasks(ctx, req.PCCNID, len(def.Steps)); err != nil {
        return &models.PCCNResponse{
            Success: false,
            Message: fmt.Sprintf("更新任务总数失败: %v", err),
        }, nil
    }

    if err := o.pccnDAO.UpdateStatus(ctx, req.PCCNID, models.ResourceStatusCreating, ""); err != nil {
        return &models.PCCNResponse{
            Success: false,
            Message: fmt.Sprintf("更新PCCN状态失败: %v", err),
        }, nil
    }

    workflowID, err := o.engine.SubmitWorkflow(ctx, def)
    if err != nil {
        return &models.PCCNResponse{
            Success: false,
            Message: fmt.Sprintf("提交工作流失败: %v", err),
        }, nil
    }

    logger.InfoContext(ctx, "PCCN创建流程启动成功",
        "az", o.az,
        "pccn_name", req.PCCNName,
        "pccn_id", req.PCCNID,
        "workflow_id", workflowID,
        "is_cross_region", req.VPCRegion != req.PeerVPCRegion,
    )

    return &models.PCCNResponse{
        Success:    true,
        Message:    "PCCN创建工作流已启动",
        PCCNID:     req.PCCNID,
        TxID:       workflowID, // AZ层返回workflow_id
    }, nil
}

func (o *AZOrchestrator) buildPCCNTaskParams(ctx context.Context, req *PCCNRequest, subnets []string) string {
    params := map[string]interface{}{
        "pccn_id":         req.PCCNID,
        "pccn_name":       req.PCCNName,
        "vpc_name":        req.VPCName,
        "vpc_region":      req.VPCRegion,
        "peer_vpc_name":   req.PeerVPCName,
        "peer_vpc_region": req.PeerVPCRegion,
        "az":              o.az,
        "subnets":         subnets,
    }
    data, _ := json.Marshal(params)
    return string(data)
}
```

## 7. 删除PCCN流程

### 7.1 删除流程设计

```
┌─────────────────────────────────────────────────────────────────┐
│                    DeletePCCN 流程                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. 查询PCCN信息                                                 │
│     ├── 验证PCCN存在                                            │
│     └── 验证状态为running                                       │
│                                                                 │
│  2. 获取涉及的AZ（可能跨Region）                                 │
│     ├── 从VPC1的Region获取AZ列表                                │
│     └── 从VPC2的Region获取AZ列表                                │
│                                                                 │
│  3. 构建Saga事务                                                 │
│     ├── Step 1~N: 删除VPC1所有AZ的PCCN连接                      │
│     └── Step N+1~M: 删除VPC2所有AZ的PCCN连接                    │
│                                                                 │
│  4. 提交Saga事务                                                 │
│                                                                 │
│  5. 更新状态为deleting                                           │
│                                                                 │
│  6. Poll等待删除完成                                             │
│                                                                 │
│  7. 最终状态更新为deleted                                         │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### 7.2 删除实现代码

```go
func (o *Orchestrator) DeletePCCN(ctx context.Context, pccnName string) (*models.PCCNResponse, error) {
    // 1. 查询PCCN信息
    pccn, err := o.pccnDAO.GetPCCNByName(ctx, pccnName)
    if err != nil {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("PCCN不存在: %v", err)}, nil
    }

    if pccn.Status != "running" {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("PCCN状态不是running，无法删除: %s", pccn.Status)}, nil
    }

    // 2. 获取涉及的AZ（跨Region）
    vpc1AZs := o.getAZsFromRegionAndVPC(ctx, pccn.VPC1Region, pccn.VPC1Name)
    vpc2AZs := o.getAZsFromRegionAndVPC(ctx, pccn.VPC2Region, pccn.VPC2Name)

    // 3. 构建Saga事务
    builder := saga.NewSaga(fmt.Sprintf("pccn-delete-%s", pccnName)).
        WithPayload(map[string]any{
            "pccn_name":   pccnName,
            "vpc1_region": pccn.VPC1Region,
            "vpc2_region": pccn.VPC2Region,
        }).
        WithTimeout(120)

    // 为VPC1的每个AZ添加删除Step
    for _, az := range vpc1AZs {
        builder.AddStep(saga.Step{
            Name:         fmt.Sprintf("删除PCCN-VPC1-%s-%s", pccn.VPC1Region, az.ID),
            Type:         saga.StepTypeSync,
            ActionMethod: "DELETE",
            ActionURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, pccnName),
        })
    }

    // 为VPC2的每个AZ添加删除Step
    for _, az := range vpc2AZs {
        builder.AddStep(saga.Step{
            Name:         fmt.Sprintf("删除PCCN-VPC2-%s-%s", pccn.VPC2Region, az.ID),
            Type:         saga.StepTypeSync,
            ActionMethod: "DELETE",
            ActionURL:    fmt.Sprintf("%s/api/v1/pccn/%s", az.NSPAddr, pccnName),
        })
    }

    def, err := builder.Build()
    if err != nil {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("构建Saga定义失败: %v", err)}, nil
    }

    // 4. 提交Saga事务
    txID, err := o.sagaEngine.Submit(ctx, def)
    if err != nil {
        return &models.PCCNResponse{Success: false, Message: fmt.Sprintf("提交Saga事务失败: %v", err)}, nil
    }

    // 5. 更新状态
    o.pccnDAO.UpdatePCCNStatus(ctx, pccnName, "deleting", nil)

    // 6. 启动后台监听
    o.wg.Add(1)
    go func() {
        defer o.wg.Done()
        o.watchPCCNDeletion(txID, pccnName)
    }()

    return &models.PCCNResponse{
        Success:  true,
        Message:  fmt.Sprintf("PCCN删除任务已提交，事务ID: %s", txID),
        PCCNID:   pccn.ID,
        TxID:     txID,
    }, nil
}
```

## 8. 文件结构

```
internal/
├── models/
│   ├── types.go           # 添加PCCNRequest, PCCNResponse
│   └── resource.go        # 添加PCCNRegistry, PCCNResource, PCCNStatusResponse
├── top/
│   ├── api/
│   │   └── server.go      # 添加PCCN相关API路由
│   ├── orchestrator/
│   │   └── orchestrator.go # 添加CreatePCCN, DeletePCCN, watchPCCNSagaAndPollAZs
│   └── pccn/
│       └── dao/
│           └── dao.go     # 新增PCCNDAO
├── az/
│   ├── api/
│   │   └── server.go      # 添加PCCN相关API路由
│   └── orchestrator/
│       └── orchestrator.go # 添加CreatePCCN, DeletePCCN, GetPCCNStatus
├── db/
│   └── dao/
│       └── dao.go         # 添加PCCNDAO（AZ层）
└── client/
    └── az_client.go       # 添加GetPCCNStatus, CreatePCCN方法

tasks/
└── pccn_handlers.go       # 新增PCCN Worker任务Handler

migrations/
└── 003_create_pccn_tables.sql  # 数据库迁移脚本
```

## 9. 时序图

### 9.1 创建PCCN时序图（包含Poll Step）

```
┌────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐     ┌────────┐
│ Client │     │ Top NSP  │     │ Saga     │     │ AZ NSP   │     │ Worker │
└───┬────┘     └────┬─────┘     │ Engine   │     │          │     │        │
    │               │           └────┬─────┘     └────┬─────┘     └───┬────┘
    │ POST /pccn   │                │                │               │
    │──────────────>│                │                │               │
    │               │                │                │               │
    │               │ 预检查VPC1/VPC2│                │               │
    │               │────────────────────────────────>│               │
    │               │                │                │               │
    │               │ 构建Saga事务   │                │               │
    │               │ (含Poll Step)  │                │               │
    │               │                │                │               │
    │               │ Submit Saga    │                │               │
    │               │───────────────>│                │               │
    │               │                │                │               │
    │ 200 OK        │                │                │               │
    │ (tx_id)       │                │                │               │
    │<──────────────│                │                │               │
    │               │                │                │               │
    │               │                │ Step1: POST   │               │
    │               │                │ (Sync)        │               │
    │               │                │───────────────>│               │
    │               │                │                │ Submit WF     │
    │               │                │                │──────────────>│
    │               │                │                │               │
    │               │                │ 200 OK        │               │
    │               │                │<───────────────│               │
    │               │                │                │               │
    │               │                │ Step2: Poll   │               │
    │               │                │ (每5秒)        │               │
    │               │                │───────────────>│               │
    │               │                │ GET /status   │               │
    │               │                │<───────────────│               │
    │               │                │ status:creating│              │
    │               │                │                │               │
    │               │                │ (继续Poll...)  │               │
    │               │                │                │               │
    │               │                │───────────────>│               │
    │               │                │ status:running│               │
    │               │                │<───────────────│               │
    │               │                │                │               │
    │               │                │ Step2成功 ✓   │               │
    │               │                │                │               │
    │               │                │ Step3: POST   │               │
    │               │                │ (下一个AZ)    │               │
    │               │                │───────────────>│               │
    │               │                │ ...            │               │
    │               │                │                │               │
    │               │                │ 所有Step完成   │               │
    │               │                │ Saga: succeeded│              │
    │               │<───────────────│                │               │
    │               │                │                │               │
    │               │ 更新DB状态     │                │               │
    │               │ status=running │                │               │
    │               │                │                │               │
```

### 9.2 失败回滚时序图

```
┌────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐     ┌────────┐
│ Client │     │ Top NSP  │     │ Saga     │     │ AZ NSP   │     │ Worker │
└───┬────┘     └────┬─────┘     │ Engine   │     │          │     │        │
    │               │           └────┬─────┘     └────┬─────┘     └───┬────┘
    │               │                │                │               │
    │               │                │ Step1: POST   │               │
    │               │                │───────────────>│               │
    │               │                │ 200 OK        │               │
    │               │                │<───────────────│               │
    │               │                │                │               │
    │               │                │ Step2: Poll   │               │
    │               │                │───────────────>│               │
    │               │                │ status:running│               │
    │               │                │<───────────────│               │
    │               │                │ Step2成功 ✓   │               │
    │               │                │                │               │
    │               │                │ Step3: POST   │               │
    │               │                │ (VPC2-AZ1)    │               │
    │               │                │───────────────>│               │
    │               │                │ 200 OK        │               │
    │               │                │<───────────────│               │
    │               │                │                │               │
    │               │                │ Step4: Poll   │               │
    │               │                │───────────────>│               │
    │               │                │ status:failed │               │
    │               │                │<───────────────│               │
    │               │                │                │               │
    │               │                │ Step4失败 ✗   │               │
    │               │                │                │               │
    │               │                │ 触发Compensate│               │
    │               │                │───────────────>│               │
    │               │                │ DELETE /pccn  │               │
    │               │                │ (VPC2-AZ1)    │               │
    │               │                │<───────────────│               │
    │               │                │                │               │
    │               │                │───────────────>│               │
    │               │                │ DELETE /pccn  │               │
    │               │                │ (VPC1-AZ1)    │               │
    │               │                │<───────────────│               │
    │               │                │                │               │
    │               │                │ Saga: failed  │               │
    │               │<───────────────│                │               │
    │               │                │                │               │
    │               │ 更新DB状态     │                │               │
    │               │ status=failed  │                │               │
    │               │                │                │               │
```

## 10. 错误处理

### 10.1 错误场景

| 场景 | 处理方式 |
|------|----------|
| VPC不存在 | 返回400错误，不启动Saga |
| VPC状态非running | 返回400错误，不启动Saga |
| AZ不健康 | 返回503错误，不启动Saga |
| Saga API调用失败 | 自动触发补偿，回滚已成功的步骤 |
| Worker执行失败 | 通过Poll检测，更新状态为failed |
| Poll超时 | 标记为failed，记录超时原因 |

### 10.2 幂等性保证

- PCCN名称全局唯一，重复创建返回已存在错误
- AZ层使用统一的PCCN ID，确保跨AZ幂等
- 删除操作幂等，已删除的PCCN再次删除返回成功

## 11. 测试用例

### 11.1 创建PCCN

```bash
# 同Region场景
curl -X POST http://top-nsp:8080/api/v1/pccn \
  -H "Content-Type: application/json" \
  -d '{
    "pccn_name": "pccn-same-region-001",
    "vpc1": {
        "vpc_name": "vpc-beijing-001",
        "region": "cn-beijing"
    },
    "vpc2": {
        "vpc_name": "vpc-beijing-002",
        "region": "cn-beijing"
    }
  }'

# 跨Region场景
curl -X POST http://top-nsp:8080/api/v1/pccn \
  -H "Content-Type: application/json" \
  -d '{
    "pccn_name": "pccn-cross-region-001",
    "vpc1": {
        "vpc_name": "vpc-beijing-001",
        "region": "cn-beijing"
    },
    "vpc2": {
        "vpc_name": "vpc-shanghai-001",
        "region": "cn-shanghai"
    }
  }'

# 查询状态
curl http://top-nsp:8080/api/v1/pccn/pccn-cross-region-001/status
```

### 11.2 删除PCCN

```bash
curl -X DELETE http://top-nsp:8080/api/v1/pccn/pccn-cross-region-001
```

## 12. 跨Region特殊处理

### 12.1 跨Region场景识别

```go
// 判断是否跨Region
isCrossRegion := req.VPC1.Region != req.VPC2.Region
```

### 12.2 跨Region路由配置

跨Region场景下，路由配置需要额外处理：

| 场景 | 路由类型 | 特殊处理 |
|------|----------|----------|
| 同Region | intra-region | 直接配置VPC间路由 |
| 跨Region | cross-region | 需要配置跨Region网关路由 |

### 12.3 跨Region延迟考虑

- Poll超时时间可适当延长
- 健康检查需要考虑跨Region网络延迟
- Saga事务超时时间建议增加

## 13. 扩展考虑

### 13.1 多VPC网状连接

当前设计支持两个VPC互通。未来可扩展支持多VPC网状连接：

- 一个PCCN关联多个VPC
- 使用图结构存储VPC间的连接关系
- 支持动态添加/移除VPC

### 13.2 连接策略

未来可增加更细粒度的连接控制：

- 支持指定互通的子网
- 支持带宽限制
- 支持QoS策略
- 支持访问控制列表
