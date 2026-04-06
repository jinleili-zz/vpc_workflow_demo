# SAGA 分布式事务模块

## 概述

SAGA 模块是一个嵌入式的分布式事务 SDK，以后台 goroutine 方式运行在业务进程中，无需独立部署。它实现了 SAGA 模式，通过补偿机制实现事务的最终一致性。

## 核心特性

- **同步/异步步骤支持**: 支持立即返回的同步步骤和需要轮询的异步步骤
- **补偿回滚**: 步骤失败时自动逆序执行已完成步骤的补偿操作
- **崩溃恢复**: 引擎重启后自动恢复未完成的事务
- **超时处理**: 支持事务级别超时，超时后自动触发补偿
- **分布式安全**: 使用 `FOR UPDATE SKIP LOCKED` + 租约锁 + CAS 状态更新，保证多实例环境下的任务分配和状态变更安全
- **模板变量**: 支持在 URL 和 Payload 中使用模板变量引用上下文数据

---

## 架构设计

### 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                        业务服务进程                               │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                      SAGA Engine                           │  │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────────┐   │  │
│  │  │ Coordinator │  │   Poller    │  │    Executor     │   │  │
│  │  │ (状态机驱动) │  │ (异步轮询)  │  │  (HTTP 调用)    │   │  │
│  │  └──────┬──────┘  └──────┬──────┘  └────────┬────────┘   │  │
│  │         │                │                   │            │  │
│  │         └────────────────┼───────────────────┘            │  │
│  │                          │                                │  │
│  │                   ┌──────▼──────┐                         │  │
│  │                   │    Store    │                         │  │
│  │                   │ (PostgreSQL)│                         │  │
│  │                   └─────────────┘                         │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### 核心组件

| 组件 | 文件 | 职责 |
|------|------|------|
| **Engine** | `engine.go` | 引擎入口，提供 Submit/Query/Start/Stop API |
| **Coordinator** | `coordinator.go` | 协调器，状态机驱动事务执行流程 |
| **Executor** | `executor.go` | 执行器，负责 HTTP 调用（正向/补偿/轮询） |
| **Poller** | `poller.go` | 轮询器，处理异步步骤的状态查询 |
| **Store** | `store.go` | 持久化层，PostgreSQL 数据库操作 |
| **Template** | `template.go` | 模板引擎，渲染动态 URL 和 Payload |
| **JSONPath** | `jsonpath.go` | JSONPath 解析，用于轮询结果判断 |

---

## 状态机设计

### 事务状态流转

```
                    ┌─────────┐
                    │ pending │
                    └────┬────┘
                         │ Start
                         ▼
                    ┌─────────┐
          ┌────────│ running │────────┐
          │        └────┬────┘        │
          │             │             │
          │ Step Failed │ All Steps   │ Timeout
          │             │ Succeeded   │
          ▼             ▼             ▼
    ┌─────────────┐ ┌───────────┐ ┌─────────────┐
    │compensating │ │ succeeded │ │compensating │
    └──────┬──────┘ └───────────┘ └──────┬──────┘
           │                              │
           │ Compensation Done            │
           ▼                              ▼
      ┌────────┐                    ┌────────┐
      │ failed │                    │ failed │
      └────────┘                    └────────┘
```

### 步骤状态流转

```
同步步骤 (Sync):
  pending → running → succeeded
                   ↘ failed → compensating → compensated

异步步骤 (Async):
  pending → running → polling → succeeded
                            ↘ failed → compensating → compensated
```

---

## 数据库设计

### 表结构

执行数据库迁移脚本创建表：

```bash
psql -h localhost -U saga -d saga_test -f migrations/saga.sql
```

#### 1. saga_transactions (全局事务表)

| 字段 | 类型 | 说明 |
|------|------|------|
| id | VARCHAR(64) | 事务 ID (UUID) |
| status | VARCHAR(20) | 状态: pending/running/compensating/succeeded/failed |
| payload | JSONB | 全局负载数据 |
| current_step | INT | 当前步骤索引 |
| timeout_at | TIMESTAMPTZ | 超时时间 |
| locked_by | VARCHAR(128) | 持有锁的实例 ID (多实例并发控制) |
| locked_until | TIMESTAMPTZ | 锁过期时间，超过此时间其他实例可抢占 |

#### 2. saga_steps (步骤表)

| 字段 | 类型 | 说明 |
|------|------|------|
| id | VARCHAR(64) | 步骤 ID (UUID) |
| transaction_id | VARCHAR(64) | 所属事务 ID |
| step_index | INT | 步骤顺序 |
| step_type | VARCHAR(20) | 类型: sync/async |
| status | VARCHAR(20) | 步骤状态 |
| action_url | TEXT | 正向操作 URL |
| action_response | JSONB | 正向操作响应 |
| compensate_url | TEXT | 补偿操作 URL |
| poll_url | TEXT | 轮询 URL (async) |
| poll_success_path | TEXT | 成功判断 JSONPath |
| poll_success_value | TEXT | 成功期望值 |

#### 3. saga_poll_tasks (轮询任务表)

| 字段 | 类型 | 说明 |
|------|------|------|
| id | BIGSERIAL | 自增主键 |
| step_id | VARCHAR(64) | 关联步骤 ID |
| next_poll_at | TIMESTAMPTZ | 下次轮询时间 |
| locked_until | TIMESTAMPTZ | 分布式锁截止时间 |
| locked_by | VARCHAR(64) | 锁定者实例 ID |

---

## 使用指南

### 1. 初始化引擎

```go
import "github.com/paic/nsp-common/pkg/saga"

// 创建引擎
engine, err := saga.NewEngine(&saga.Config{
    DSN:              "postgres://user:pass@localhost:5432/dbname?sslmode=disable",
    WorkerCount:      4,                    // 并发 Worker 数量
    PollBatchSize:    20,                   // 每次轮询批量大小
    PollScanInterval: 3 * time.Second,      // 轮询扫描间隔
    HTTPTimeout:      30 * time.Second,     // HTTP 请求超时
})
if err != nil {
    log.Fatal(err)
}

// 启动后台任务
ctx := context.Background()
if err := engine.Start(ctx); err != nil {
    log.Fatal(err)
}

// 程序退出时停止引擎
defer engine.Stop()
```

### 2. 定义同步事务

```go
def, err := saga.NewSaga("order-checkout").
    AddStep(saga.Step{
        Name:             "扣减库存",
        Type:             saga.StepTypeSync,
        ActionMethod:     "POST",
        ActionURL:        "http://stock-service/api/v1/stock/deduct",
        ActionPayload:    map[string]any{"item_id": "SKU-001", "count": 2},
        CompensateMethod: "POST",
        CompensateURL:    "http://stock-service/api/v1/stock/rollback",
        CompensatePayload: map[string]any{"item_id": "SKU-001", "count": 2},
    }).
    AddStep(saga.Step{
        Name:             "创建订单",
        Type:             saga.StepTypeSync,
        ActionMethod:     "POST",
        ActionURL:        "http://order-service/api/v1/orders",
        ActionPayload:    map[string]any{"user_id": "U-001"},
        CompensateMethod: "DELETE",
        // 使用模板变量引用上一步的响应
        CompensateURL:    "http://order-service/api/v1/orders/{action_response.order_id}",
    }).
    Build()

if err != nil {
    log.Fatal(err)
}

// 提交事务
txID, err := engine.Submit(ctx, def)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Transaction ID: %s\n", txID)
```

### 3. 定义异步事务（带轮询）

```go
def, err := saga.NewSaga("device-config").
    AddStep(saga.Step{
        Name:             "设备配置下发",
        Type:             saga.StepTypeAsync,  // 异步步骤
        ActionMethod:     "POST",
        ActionURL:        "http://device-service/api/v1/config/apply",
        ActionPayload:    map[string]any{"device_id": "DEV-001"},
        CompensateMethod: "POST",
        CompensateURL:    "http://device-service/api/v1/config/rollback",
        
        // 轮询配置
        PollURL:          "http://device-service/api/v1/config/status?task_id={action_response.task_id}",
        PollMethod:       "GET",
        PollIntervalSec:  10,        // 每 10 秒轮询一次
        PollMaxTimes:     30,        // 最多轮询 30 次
        PollSuccessPath:  "$.status",
        PollSuccessValue: "success",
        PollFailurePath:  "$.status",
        PollFailureValue: "failed",
    }).
    Build()
```

### 4. 使用全局 Payload

```go
def, err := saga.NewSaga("transfer").
    WithPayload(map[string]any{
        "from_account": "ACC-001",
        "to_account":   "ACC-002",
        "amount":       1000,
    }).
    WithTimeout(300). // 5 分钟超时
    AddStep(saga.Step{
        Name:          "扣款",
        Type:          saga.StepTypeSync,
        ActionMethod:  "POST",
        ActionURL:     "http://account-service/api/v1/debit",
        ActionPayload: map[string]any{
            // 引用全局 Payload
            "account": "{transaction.payload.from_account}",
            "amount":  "{transaction.payload.amount}",
        },
        CompensateMethod: "POST",
        CompensateURL:    "http://account-service/api/v1/credit",
        CompensatePayload: map[string]any{
            "account": "{transaction.payload.from_account}",
            "amount":  "{transaction.payload.amount}",
        },
    }).
    AddStep(saga.Step{
        Name:          "入账",
        Type:          saga.StepTypeSync,
        ActionMethod:  "POST",
        ActionURL:     "http://account-service/api/v1/credit",
        ActionPayload: map[string]any{
            "account": "{transaction.payload.to_account}",
            "amount":  "{transaction.payload.amount}",
        },
        CompensateMethod: "POST",
        CompensateURL:    "http://account-service/api/v1/debit",
        CompensatePayload: map[string]any{
            "account": "{transaction.payload.to_account}",
            "amount":  "{transaction.payload.amount}",
        },
    }).
    Build()
```

### 5. 查询事务状态

```go
status, err := engine.Query(ctx, txID)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("事务 ID: %s\n", status.ID)
fmt.Printf("状态: %s\n", status.Status)
fmt.Printf("当前步骤: %d\n", status.CurrentStep)

for _, step := range status.Steps {
    fmt.Printf("  步骤 %d (%s): %s\n", step.Index, step.Name, step.Status)
}
```

---

## 模板变量语法

在 URL 和 Payload 中可以使用以下模板变量：

| 语法 | 说明 | 示例 |
|------|------|------|
| `{action_response.field}` | 当前步骤的响应字段 | `{action_response.order_id}` |
| `{step[N].action_response.field}` | 指定步骤的响应字段 | `{step[0].action_response.user_id}` |
| `{transaction.payload.field}` | 全局 Payload 字段 | `{transaction.payload.amount}` |

### 示例

```go
// 在 URL 中使用
CompensateURL: "http://order-service/api/v1/orders/{action_response.order_id}"

// 在 Payload 中使用
ActionPayload: map[string]any{
    "order_id": "{step[0].action_response.order_id}",
    "amount":   "{transaction.payload.amount}",
}
```

---

## JSONPath 语法

用于轮询结果判断，支持以下语法：

| 语法 | 说明 | 示例 |
|------|------|------|
| `$.field` | 顶层字段 | `$.status` |
| `$.nested.field` | 嵌套字段 | `$.result.code` |
| `$.array[N].field` | 数组索引 | `$.items[0].status` |

### 示例

```go
// 轮询响应: {"result": {"status": "success", "message": "ok"}}

PollSuccessPath:  "$.result.status"
PollSuccessValue: "success"
```

---

## 配置参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `DSN` | string | - | PostgreSQL 连接串 (必填) |
| `WorkerCount` | int | 4 | 并发 Worker 数量 |
| `PollBatchSize` | int | 20 | 每次轮询扫描的任务数 |
| `PollScanInterval` | Duration | 3s | 轮询扫描间隔 |
| `CoordScanInterval` | Duration | 5s | 协调器扫描间隔 |
| `HTTPTimeout` | Duration | 30s | HTTP 请求超时时间 |
| `InstanceID` | string | auto | 实例 ID (自动生成: hostname-pid) |
| `LeaseDuration` | Duration | 5m | 分布式锁租约时长 (Coordinator 内部配置) |

---

## 运行测试

```bash
# 启动 PostgreSQL
docker run -d --name saga-postgres \
  -e POSTGRES_USER=saga \
  -e POSTGRES_PASSWORD=saga123 \
  -e POSTGRES_DB=saga_test \
  -p 5432:5432 postgres:15

# 执行迁移
docker exec -i saga-postgres psql -U saga -d saga_test < migrations/saga.sql

# 运行测试
cd nsp-common/pkg/saga
TEST_DSN="postgres://saga:saga123@localhost:5432/saga_test?sslmode=disable" go test -v
```

---

## 最佳实践

### 1. 幂等性设计

所有被调用的服务接口应该是幂等的，因为：
- 网络超时可能导致重试
- 崩溃恢复可能重新执行步骤

每个 HTTP 请求会携带 `X-Idempotency-Key` 头，值为步骤 ID，服务端应基于此实现幂等。

### 2. 补偿接口设计

- 补偿接口也应该是幂等的
- 补偿操作应该能处理"未执行"的情况（如订单不存在时删除订单应返回成功）

### 3. 超时设置

```go
saga.NewSaga("name").
    WithTimeout(300). // 设置合理的超时时间
    AddStep(...)
```

### 4. 监控告警

关注以下指标：
- `saga_transactions` 表中 `status = 'failed'` 的记录
- `status = 'compensating'` 且长时间未变化的记录
- `saga_poll_tasks` 表中 `locked_until` 过期的任务
- `saga_transactions` 表中 `locked_until` 过期但状态仍为 running 的事务（可能需要人工介入）

---

## 错误处理

| 场景 | 处理方式 |
|------|----------|
| 步骤执行失败 | 自动触发补偿流程 |
| 轮询超时 | 标记步骤失败，触发补偿 |
| 补偿失败 | 记录错误，事务标记为 failed，需人工介入 |
| 引擎崩溃 | 重启后自动恢复未完成事务 |

---

## 多实例并发安全

SAGA 模块支持多实例部署，通过以下机制保证并发安全：

### 1. 分布式锁机制

每个事务在被处理前需要获取分布式锁：

```
saga_transactions 表新增字段：
- locked_by:    VARCHAR(128)  -- 持有锁的实例 ID
- locked_until: TIMESTAMPTZ   -- 锁过期时间
```

锁获取规则（`ClaimTransaction`）：
- 仅当 `locked_by IS NULL` 或 `locked_until < NOW()` 或 `locked_by = 本实例` 时可获取
- 支持可重入（同一实例可续期）
- 仅对非终态事务（pending/running/compensating）加锁

### 2. CAS 状态更新

状态变更使用 Compare-And-Swap 语义（`UpdateTransactionStatusCAS`）：

```sql
UPDATE saga_transactions
SET status = $new_status
WHERE id = $id AND status = $expected_status
```

仅当当前状态匹配预期时才更新，防止并发覆盖。

### 3. 任务扫描安全

恢复扫描和超时扫描使用 `FOR UPDATE SKIP LOCKED`：

```sql
SELECT * FROM saga_transactions
WHERE status IN ('pending', 'running', 'compensating')
  AND (locked_by IS NULL OR locked_until < NOW())
FOR UPDATE SKIP LOCKED
```

- 已被锁定的事务会被跳过，不会阻塞
- 扫描后立即设置 `locked_by` 和 `locked_until`
- 处理完成或失败后释放锁

### 4. 崩溃恢复

实例崩溃后：
1. 其持有的锁会在 `locked_until` 过期后自动释放
2. 其他实例在下次扫描时可接管未完成事务
3. 默认租约时长 5 分钟，可通过 `LeaseDuration` 配置

### 5. 实例标识

每个实例需要唯一的 `InstanceID`：
- 默认自动生成：`hostname-pid`
- 可通过配置显式指定
- 用于锁归属识别和锁释放验证

---

## 文件结构

```
nsp-common/
├── migrations/
│   └── saga.sql                   # 数据库建表脚本
└── pkg/saga/
    ├── coordinator.go             # 协调器（状态机驱动）
    ├── definition.go              # SAGA/Step 定义结构
    ├── engine.go                  # 引擎入口 API
    ├── executor.go                # HTTP 执行器
    ├── jsonpath.go                # JSONPath 解析
    ├── poller.go                  # 异步轮询器
    ├── saga_test.go               # 单元测试
    ├── saga_integration_test.go   # 多实例集成测试
    ├── store.go                   # PostgreSQL 持久化
    ├── template.go                # 模板变量渲染
    └── README.md                  # 本文档
```
