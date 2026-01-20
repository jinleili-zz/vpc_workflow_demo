# DTM Saga分布式事务改造设计文档

## 1. 背景与问题分析

### 1.1 当前架构
- Top NSP作为全局编排器，调用多个AZ NSP创建VPC等资源
- Region级别VPC创建需要在多个AZ并行创建
- 失败时通过手动调用DeleteVPC进行回滚

### 1.2 存在的问题
1. **Top NSP崩溃风险**：如果Top NSP在调用过程中崩溃，部分AZ可能已创建成功，但无法完成回滚
2. **无事务协调器**：缺乏独立的事务管理器，回滚逻辑耦合在业务代码中
3. **幂等性保证不足**：重试机制不完善，可能导致重复创建
4. **缺乏可观测性**：难以追踪分布式事务的完整状态

### 1.3 改造目标
引入DTM（Distributed Transaction Manager）框架，使用Saga模式实现：
- 可靠的分布式事务协调
- 自动重试和补偿
- 事务状态持久化
- 可视化监控

## 2. DTM Saga模式设计

### 2.1 Saga模式原理
Saga模式将长事务拆分为多个本地事务，每个本地事务有对应的补偿事务：
- **正向操作**：执行业务逻辑（如创建VPC）
- **补偿操作**：回滚业务逻辑（如删除VPC）
- **DTM协调器**：负责编排、重试、持久化事务状态

### 2.2 VPC创建Saga流程设计

```
DTM Saga Transaction: CreateRegionVPC
├─ Step 1: CreateVPC in cn-beijing-1a
│  ├─ Action: POST /api/v1/dtm/vpc (AZ NSP cn-beijing-1a)
│  └─ Compensate: POST /api/v1/dtm/vpc/compensate (AZ NSP cn-beijing-1a)
├─ Step 2: CreateVPC in cn-beijing-1b  
│  ├─ Action: POST /api/v1/dtm/vpc (AZ NSP cn-beijing-1b)
│  └─ Compensate: POST /api/v1/dtm/vpc/compensate (AZ NSP cn-beijing-1b)
└─ Step 3: CreateVPC in cn-shanghai-1a
   ├─ Action: POST /api/v1/dtm/vpc (AZ NSP cn-shanghai-1a)
   └─ Compensate: POST /api/v1/dtm/vpc/compensate (AZ NSP cn-shanghai-1a)
```

**执行流程**：
1. Top NSP调用DTM创建Saga事务，注册所有AZ的Action/Compensate URL
2. DTM按顺序调用各AZ的Action接口（创建VPC）
3. 如果某个Action失败，DTM自动调用已成功步骤的Compensate接口
4. DTM持久化事务状态到MySQL，支持崩溃恢复

### 2.3 核心改造点

#### 2.3.1 AZ NSP新增DTM接口
```go
// 正向操作：创建VPC（幂等）
POST /api/v1/dtm/vpc
Request: {
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "vrf_name": "VRF-001",
  "vlan_id": 100,
  "firewall_zone": "trust-zone",
  "dtm_barrier_id": "barrier_id_from_dtm"  // DTM提供的barrier ID用于幂等
}
Response: {"dtmResult": "SUCCESS"}

// 补偿操作：删除VPC（幂等）
POST /api/v1/dtm/vpc/compensate
Request: {
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "dtm_barrier_id": "barrier_id_from_dtm"
}
Response: {"dtmResult": "SUCCESS"}
```

#### 2.3.2 Top NSP Orchestrator改造
- 移除手动并行调用和rollback逻辑
- 使用dtmcli SDK构建Saga事务
- 动态根据Region的AZ数量注册Saga步骤

#### 2.3.3 幂等性保证
- 使用DTM的Barrier机制确保Action/Compensate幂等
- VPC创建前检查是否已存在（通过vpc_name + barrier_id）
- VPC删除前检查状态，避免重复删除

## 3. 技术方案

### 3.1 DTM部署架构
```
┌─────────────┐
│   DTM       │
│  (HTTP API) │  - 协调Saga事务
│   + MySQL   │  - 持久化事务状态
└─────────────┘
      │
      ├─ 调用 ─> Top NSP (启动Saga)
      ├─ 调用 ─> AZ NSP cn-beijing-1a (Action/Compensate)
      ├─ 调用 ─> AZ NSP cn-beijing-1b (Action/Compensate)
      └─ 调用 ─> AZ NSP cn-shanghai-1a (Action/Compensate)
```

### 3.2 依赖变更
**go.mod新增**：
```go
require (
    github.com/dtm-labs/client/dtmcli v1.18.0
    github.com/dtm-labs/client/dtmgrpc v1.18.0
)
```

### 3.3 数据库变更
**DTM需要独立数据库**（由DTM自动初始化表结构）：
- 数据库名：`dtm_db`
- 主要表：`trans_global`（全局事务）、`trans_branch`（分支事务）

**AZ NSP数据库变更**（用于幂等控制）：
```sql
-- 每个AZ的MySQL数据库中新增barrier表
CREATE TABLE IF NOT EXISTS dtm_barrier (
    trans_type VARCHAR(45) NOT NULL,
    gid VARCHAR(128) NOT NULL,
    branch_id VARCHAR(128) NOT NULL,
    op VARCHAR(45) NOT NULL,
    barrier_id VARCHAR(128) NOT NULL,
    reason VARCHAR(255),
    create_time DATETIME DEFAULT CURRENT_TIMESTAMP,
    update_time DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (trans_type, gid, branch_id, op)
);
CREATE UNIQUE INDEX idx_barrier_id ON dtm_barrier (barrier_id);
```

## 4. 实施步骤

### 4.1 环境搭建
1. docker-compose.yml新增DTM服务
2. 配置DTM连接到MySQL（dtm_db数据库）
3. 初始化各AZ数据库的dtm_barrier表

### 4.2 代码改造
1. **更新依赖**：go.mod添加dtmcli
2. **AZ NSP改造**：
   - 新增DTM专用handler（dtm_handlers.go）
   - 实现CreateVPCAction、CreateVPCCompensate
   - 集成Barrier幂等检查
3. **Top NSP改造**：
   - 修改orchestrator.go的CreateRegionVPC方法
   - 使用dtmcli.Saga构建事务
   - 注册各AZ的Action/Compensate URL
4. **配置文件**：新增DTM_SERVER_ADDR环境变量

### 4.3 测试验证
1. **正常场景**：创建VPC，所有AZ成功
2. **部分失败场景**：模拟某个AZ NSP失败，验证自动补偿
3. **Top NSP崩溃场景**：创建过程中重启Top NSP，验证DTM恢复
4. **幂等性测试**：重复提交相同请求，验证不重复创建

## 5. 关键代码示例

### 5.1 Top NSP - Saga编排
```go
func (o *Orchestrator) CreateRegionVPC(ctx context.Context, req *models.VPCRequest) (*models.VPCResponse, error) {
    azs, err := o.registry.GetRegionAZs(ctx, req.Region)
    if err != nil {
        return nil, err
    }

    // 创建Saga事务
    saga := dtmcli.NewSaga(dtmServerAddr, dtmcli.MustGenGid(dtmServerAddr)).
        Add(fmt.Sprintf("%s/api/v1/dtm/vpc", azs[0].NSPAddr), 
            fmt.Sprintf("%s/api/v1/dtm/vpc/compensate", azs[0].NSPAddr), 
            req).
        Add(fmt.Sprintf("%s/api/v1/dtm/vpc", azs[1].NSPAddr), 
            fmt.Sprintf("%s/api/v1/dtm/vpc/compensate", azs[1].NSPAddr), 
            req).
        // ... 动态添加所有AZ
        EnableWaitResult()  // 等待所有分支完成

    // 提交Saga事务
    err = saga.Submit()
    if err != nil {
        return &models.VPCResponse{Success: false, Message: err.Error()}, nil
    }

    return &models.VPCResponse{Success: true, Message: "VPC创建成功"}, nil
}
```

### 5.2 AZ NSP - Action接口（幂等）
```go
func (s *Server) createVPCAction(c *gin.Context) {
    var req models.VPCRequest
    c.ShouldBindJSON(&req)

    // DTM Barrier幂等检查
    barrier := dtmcli.BarrierFromGin(c)
    if err := barrier.Call(s.db, func(tx *sql.Tx) error {
        // 业务逻辑：创建VPC
        return s.orchestrator.CreateVPC(context.Background(), &req)
    }); err != nil {
        c.JSON(http.StatusOK, gin.H{"dtmResult": "FAILURE"})
        return
    }

    c.JSON(http.StatusOK, gin.H{"dtmResult": "SUCCESS"})
}
```

### 5.3 AZ NSP - Compensate接口（幂等）
```go
func (s *Server) compensateVPCAction(c *gin.Context) {
    var req models.VPCRequest
    c.ShouldBindJSON(&req)

    barrier := dtmcli.BarrierFromGin(c)
    if err := barrier.Call(s.db, func(tx *sql.Tx) error {
        // 补偿逻辑：删除VPC
        return s.orchestrator.DeleteVPC(context.Background(), req.VPCName)
    }); err != nil {
        c.JSON(http.StatusOK, gin.H{"dtmResult": "FAILURE"})
        return
    }

    c.JSON(http.StatusOK, gin.H{"dtmResult": "SUCCESS"})
}
```

## 6. 优势与限制

### 6.1 优势
1. **可靠性**：DTM持久化事务状态，支持崩溃恢复
2. **自动补偿**：失败时自动调用Compensate，无需手动编写回滚逻辑
3. **可观测性**：DTM提供Web UI查看事务状态
4. **幂等性**：Barrier机制保证重试安全

### 6.2 限制
1. **性能开销**：增加DTM协调层，每个步骤需持久化
2. **顺序执行**：Saga默认串行执行各分支（可通过并发Saga优化）
3. **最终一致性**：补偿操作可能延迟，不是强一致性

## 7. 监控与运维

### 7.1 DTM Dashboard
- 访问 http://localhost:36789 查看事务列表
- 支持手动重试失败事务
- 查看每个分支的执行时间和结果

### 7.2 日志增强
- Top NSP记录Saga GID
- AZ NSP记录branch_id和barrier_id
- 关联分布式追踪（可集成Jaeger）

## 8. 后续优化方向

1. **并发Saga**：使用dtmcli.NewSaga().AddConcurrent实现AZ并行创建
2. **异步Saga**：长耗时操作改为异步模式，提升用户体验
3. **TCC模式**：对于需要预留资源的场景（如IP地址），考虑TCC模式
4. **Saga超时配置**：根据业务特点设置合理的超时时间

---

**文档版本**：v1.0  
**创建日期**：2026-01-20  
**作者**：Qoder AI  
