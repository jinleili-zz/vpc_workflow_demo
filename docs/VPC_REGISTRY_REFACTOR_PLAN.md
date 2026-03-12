# VPC Registry 表重构方案：一个 VPC 一条记录

## 设计目标

1. `vpc_registry` 中一个 VPC 只有一条记录
2. `status` 字段存储从各 AZ 汇聚后的整体状态
3. `GET /vpcs` 直接查表，不再扇出，每个 VPC 一条
4. `GET /vpc/:name/status` 单行查询，不再合并多条记录
5. per-AZ 执行详情仍可查询

## 方案选型

| | 方案 A: JSONB 列 (采用) | 方案 B: 独立子表 |
|--|--|--|
| **表结构** | `vpc_registry` 增加 `az_details JSONB` 列 | 新建 `vpc_az_status` 表 |
| **一个 VPC 几条记录** | 1 条 | 1 条 (vpc_registry) + N 条 (vpc_az_status) |
| **查 VPC 列表** | 一次 SELECT | 一次 SELECT |
| **查单个 VPC 详情** | 一次 SELECT | JOIN 或两次 SELECT |
| **更新 per-AZ 状态** | 一次 UPDATE (JSONB 操作) | N 次 UPDATE |
| **SQL 聚合查询** | 需要 JSONB 函数 | 标准 SQL |
| **扩展性** | 字段变更需改 JSONB 结构 | ALTER TABLE 即可 |

**采用方案 A (JSONB)**，理由：

1. 最贴合"一个资源一条记录"的诉求，查询路径最简单
2. 每个 VPC 的 AZ 数量很少（2-5个），JSONB 完全胜任
3. per-AZ 详情总是跟 VPC 一起取用，没有独立查询 AZ 状态的需求
4. `watchSagaTransaction` 更新从 N 次 SQL 变成 1 次，更高效
5. 对于 creating 状态的 VPC，如果需要实时进度，仍可穿透查询 saga_steps

---

## 一、表结构变更

### 新 `vpc_registry` DDL

```sql
CREATE TABLE IF NOT EXISTS vpc_registry (
    id              VARCHAR(64)  PRIMARY KEY,
    vpc_name        VARCHAR(128) NOT NULL UNIQUE,   -- 唯一键改为仅 vpc_name
    region          VARCHAR(64)  NOT NULL,
    vrf_name        VARCHAR(128),
    vlan_id         INT,
    firewall_zone   VARCHAR(128),
    status          VARCHAR(32)  DEFAULT 'creating', -- 汇聚后的整体状态
    saga_tx_id      VARCHAR(64)  DEFAULT '',
    az_details      JSONB        DEFAULT '{}',       -- 新增: per-AZ 执行详情
    created_at      TIMESTAMPTZ  DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  DEFAULT NOW()
);
```

### `az_details` JSONB 结构

```json
{
  "cn-beijing-1a": {
    "status": "running",
    "az_vpc_id": "vpc-uuid-xxx",
    "error": ""
  },
  "cn-beijing-1b": {
    "status": "failed",
    "az_vpc_id": "",
    "error": "timeout waiting for worker"
  }
}
```

### 去掉的列

- `az VARCHAR(64)` -- 移入 az_details 的 key
- `az_vpc_id VARCHAR(64)` -- 移入 az_details 的 value

### 约束变更

- 旧: `UNIQUE (vpc_name, az)` -- 一个 VPC N 条
- 新: `UNIQUE (vpc_name)` -- 一个 VPC 一条

---

## 二、Model 变更

```go
type VPCRegistry struct {
    ID           string                 `json:"id"`
    VPCName      string                 `json:"vpc_name"`
    Region       string                 `json:"region"`
    VRFName      string                 `json:"vrf_name"`
    VLANId       int                    `json:"vlan_id"`
    FirewallZone string                 `json:"firewall_zone"`
    Status       string                 `json:"status"`
    SagaTxID     string                 `json:"saga_tx_id,omitempty"`
    AZDetails    map[string]AZDetail    `json:"az_details"`
    CreatedAt    time.Time              `json:"created_at"`
    UpdatedAt    time.Time              `json:"updated_at"`
}

type AZDetail struct {
    Status  string `json:"status"`
    AZVpcID string `json:"az_vpc_id,omitempty"`
    Error   string `json:"error,omitempty"`
}
```

去掉的字段: `AZ string`, `AZVpcID string`

---

## 三、DAO 变更

| 方法 | 现在 | 改后 |
|------|------|------|
| `RegisterVPC` | INSERT 含 az 列，UPSERT on (vpc_name, az) | INSERT 含 az_details JSONB，UPSERT on (vpc_name) |
| `UpdateVPCStatus` | `(vpcName, az, status)` 逐 AZ 更新 | **改为 `UpdateVPCOverallStatus(vpcName, status, azDetails)`** 整体更新一条 |
| `GetVPCByNameAndAZ` | 按 (vpc_name, az) 查 | **改为 `GetVPCByName(vpcName)`** 单条查询 |
| `GetVPCsByName` | 返回 N 条 | **改为 `GetVPCByName`** 返回 1 条 |
| `GetVPCsByZone` | 按 zone 查，含重复 VPC | 同样查，每个 VPC 一条 |
| `DeleteVPC` | `(vpcName, az)` | `(vpcName)` |
| `ListAllVPCs` | 返回 N*M 条 | 返回 M 条 |

---

## 四、Orchestrator 变更

### 4.1 `CreateRegionVPC`

- 现在: for 循环为每个 AZ 写一条 vpc_registry (N 次 INSERT)
- 改后: 构建 `az_details` 后写一条 (1 次 INSERT)

### 4.2 `watchSagaTransaction`

- 现在: 根据 SAGA 结果逐 AZ 更新 vpc_registry (N 次 UPDATE)
- 改后: 从 saga steps 汇聚状态，一次 `UpdateVPCOverallStatus`

### 4.3 `CreateAZSubnet`

- 现在: `GetVPCByNameAndAZ(vpcName, az)` 拿 firewall_zone
- 改后: `GetVPCByName(vpcName)` -- firewall_zone 是 VPC 级属性

---

## 五、API Server 变更

### `getVPCStatus`

- 现在: 查 N 条 -> 手动计算 overall_status -> 组装 az_statuses
- 改后: 查 1 条，直接从 az_details 构造响应
- API 响应格式完全不变

### `listVPCs`

- 现在: 扇出查询所有 AZ -> 合并
- 改后: 优先 DB 直查（一次 SELECT），降级再扇出

---

## 六、受影响文件清单

| 文件 | 改动 |
|------|------|
| `internal/db/migrations/004_*.sql` | 新增迁移脚本 |
| `internal/models/firewall.go` | VPCRegistry 去掉 AZ/AZVpcID，加 AZDetails；新增 AZDetail |
| `internal/top/vpc/dao/dao.go` | 所有 VPC DAO 方法重写 |
| `internal/top/orchestrator/orchestrator.go` | CreateRegionVPC、watchSagaTransaction、CreateAZSubnet |
| `internal/top/api/server.go` | getVPCStatus、listVPCs |
| `tests/functional/functional_test.go` | DAO 测试用例适配 |

## 七、API 兼容性

| API | 响应格式变化 |
|-----|------------|
| `GET /vpc/:name/status` | 不变 -- `overall_status` + `az_statuses` 结构一致 |
| `GET /vpcs` | 微调 -- 每个 VPC 一条，新增 `az_details` 字段 |
| `POST /vpc` | 不变 |
| `GET /vpc/id/:vpc_id` | 不变 -- 仍扇出查 AZ |
| `DELETE /vpc/id/:vpc_id` | 不变 -- 仍扇出删 AZ |
