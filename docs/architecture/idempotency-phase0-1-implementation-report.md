# NSP 幂等改造阶段 0/1 实施报告

| 项目 | 内容 |
| --- | --- |
| 依据文档 | `docs/architecture/idempotency-analysis-and-design.md` |
| 实施范围 | 阶段 0 全部 + 阶段 1 核心（VPC/Subnet/PCCN 链路） |
| 代码分支 | `feat/idempotency-phase0-1`（commit `bec2065`） |
| 测试环境 | docker 单实例 PostgreSQL + Redis；单实例 top-nsp / az-nsp / worker(switch, firewall) |
| 测试结果 | `tests/idempotency` 10/10 通过（约 71s） |
| 报告日期 | 2026-07-23 |

## 1. 已完成的代码优化

### 1.1 阶段 0：协议与确定性缺陷修复

| 文档条目 | 修复内容 | 主要落点 |
| --- | --- | --- |
| 7.4 / A3 | AZ VPC/Subnet/PCCN 写接口响应统一携带 `code` 字段（成功 `"0"`），Saga executor 不再以 `response missing code field` 误判失败；删除接口同步补 `code`（Saga 补偿同样依赖） | `internal/models/types.go`、`internal/az/api/server.go`、`internal/az/orchestrator/orchestrator.go` |
| 7.6 / A8 | PCCN DAO `Create` 改为返回数据库实际持久化的 `resource_id` 与明确判定（inserted / reused / conflict）；同名资源处于 pending/creating/running 时返回 `RESOURCE_ALREADY_EXISTS`，不再创建孤儿 Task；failed/deleted 旧行复用时先清理历史任务 | `internal/db/dao/pccn_dao.go`、`internal/az/orchestrator/orchestrator.go` |
| 7.8 数据模型 | 新增唯一索引 `uq_tasks_resource_order ON tasks(resource_id, task_order)`（migration 006），重复提交工作流原子失败 | `internal/db/migrations/006_add_tasks_resource_order_unique.sql` |
| 7.7 / A12 | `publishTask` 将 DB `max_retries` 显式透传给 Broker `MaxRetry`，asynq 重试次数与任务记录一致 | `internal/orchestration/workflow.go` |
| 7.8 / A6 | Task 终态更新改为 CAS（`WHERE status NOT IN ('completed','failed','cancelled')`），返回 `applied`；只有 CAS 胜出者才能累计资源计数和发布下一步，并发/重复/终态后 Reply 只推进一次 | `internal/db/dao/task_dao.go`、`internal/orchestration/workflow.go` |

### 1.2 阶段 1：Northbound 与 AZ HTTP 幂等

- 新增 `orchestration_operations` 表（migration 005，`internal/db/migrations/005_create_orchestration_operations.sql`），唯一约束 `(owner_service, caller_scope, route_scope, idempotency_key)` 作为并发线性化点。
- 新增 `internal/operation` 包：
  - `Begin`：INSERT ... ON CONFLICT DO NOTHING 赢得创建权；冲突时加载已有 Operation 并校验 `request_hash`，不同则返回 `ErrRequestConflict`；
  - `Complete` / `WaitTerminal` / `GetByID`：终态响应持久化与重放；
  - gin 助手 `HandleCreate`：统一实现"创建-执行-持久化-重放/409/503"流程；进行中的相同请求等待首个请求完成后重放，超时返回 `503 OPERATION_UNAVAILABLE` 并携带 `operation_id`。
- Top NSP 消费北向 `Idempotency-Key`（POST /vpc、/subnet、/pccn）；AZ NSP 消费 Saga 的 `X-Idempotency-Key` / `X-Saga-Transaction-Id`；Top 在非 Saga 的 Subnet 链路向 AZ 透传派生键 `subnet:<operation_id>`（文档 7.2）。未携带 Key 的兼容期请求由服务端生成 `auto-` 前缀 Key。
- 响应统一包含 `operation_id`；Top/AZ 新增 `GET /api/v1/operations/:operation_id` 查询入口（文档 11.1）。
- AZ 删除改为 ensure-absent：资源不存在、删除中、已删除均返回成功，重复 DELETE 与 Saga 重复补偿收敛到同一终态（文档 7.10/11.6）。
- Top `CreateRegionVPC` 响应补充统一生成的 `vpc_id`，调用方重试可取得第一次的资源身份。

### 1.3 已知限制（诚实声明）

- Operation 在业务执行中途崩溃会停留在 `running`，相同 Key 的后续请求将等待后得到 503（不会产生重复执行）；自动恢复依赖阶段 4 的 Reconciler。
- 失败后相同 Key 会重放失败结果，需调用方换新 Key 重试（符合文档 11.1 语义）。
- `tests/functional` 已适配 `UpdateResult` 新签名并通过编译/vet，但其运行依赖独立 e2e 数据库环境，本次未执行。

## 2. 测试设计与结果

### 2.1 测试基建

- `tests/idempotency/docker-compose.yml`：单实例 PostgreSQL（主机 15433）+ 单实例 Redis（16379），初始化 `top_nsp_vpc` / `nsp_test_az1_vpc` 两个库并应用 saga + 001/004/005/006 migrations。
- `scripts/test-idempotency.sh`：一键拉起依赖、构建并启动单实例 top-nsp-vpc(:19080)、az-nsp-vpc(:19081)、switch/firewall worker 各 1 个，等待 AZ 注册后运行测试；退出时自动清理服务进程（含 SIGKILL 兜底，top-nsp 的 HTTP 服务不响应 SIGTERM）。

### 2.2 用例与覆盖场景（文档 15.2 矩阵）

| 测试 | 覆盖场景 | 关键断言 |
| --- | --- | --- |
| TestBusinessTopSequentialDuplicateVPC | 顺序重复 POST（同 Key 同 Body） | 同一 operation_id/vpc_id/saga_tx；Top 仅 1 个 Operation；AZ 1 条资源 3 个任务；端到端 running；Top 注册表聚合 running |
| TestBusinessTopConcurrentDuplicateVPC | 并发 20 次相同 POST | 仅 1 个胜出；全部返回同一 operation_id/vpc_id；AZ 单资源 |
| TestBusinessTopSameKeyDifferentBody | 相同 Key 不同参数 | 409 `IDEMPOTENCY_KEY_REUSED`；无覆盖 |
| TestBusinessAZSagaStepRetryDedup | Saga 请求成功但响应丢失后 Step 重试 | AZ 命中同一 Operation 重放；不创建第二套任务；workflow 到 running |
| TestBusinessAZPCCNConcurrentSameName | PCCN 同名并发创建 ×10 | 1 成功 9 冲突；1 条资源 2 个任务；无孤儿 Task；workflow 到 running |
| TestBusinessAZDeleteEnsureAbsent | 相同 DELETE 重试 / 删除不存在 | 均返回成功 code=0 |
| TestBusinessTopSubnetIdempotent | Top 重试（AZ 已创建子网但响应丢失） | AZ 派生键去重；子网不重复创建；subnet 到 running |
| TestConcurrentDuplicateReply | 两个 Consumer 并发处理同 Reply；终态后迟到 Reply | 真实 PG+Redis：completed_tasks 只 +1；下一步只发布一次；终态不被污染 |
| TestOperationBeginReplayAndConflict / BeginConcurrent / CompleteAndWaitTerminal | Operation 服务语义 | 真实 PG：并发 50 个 Begin 仅 1 个胜出；hash 冲突检测；Complete 后可重放 |

### 2.3 执行结果

```text
=== RUN   TestBusinessTopSequentialDuplicateVPC      --- PASS (12.11s)
=== RUN   TestBusinessTopConcurrentDuplicateVPC      --- PASS (10.26s)
=== RUN   TestBusinessTopSameKeyDifferentBody        --- PASS (0.03s)
=== RUN   TestBusinessAZSagaStepRetryDedup           --- PASS (12.10s)
=== RUN   TestBusinessAZPCCNConcurrentSameName       --- PASS (9.65s)
=== RUN   TestBusinessAZDeleteEnsureAbsent           --- PASS (9.58s)
=== RUN   TestBusinessTopSubnetIdempotent            --- PASS (16.66s)
=== RUN   TestConcurrentDuplicateReply               --- PASS (0.08s)
=== RUN   TestOperationBeginReplayAndConflict        --- PASS (0.03s)
=== RUN   TestOperationBeginConcurrent               --- PASS (0.15s)
=== RUN   TestOperationCompleteAndWaitTerminal       --- PASS (0.33s)
PASS
ok  workflow_qoder/tests/idempotency  70.988s
```

`go build ./...` 与 `go vet ./...` 全部通过。

## 3. 未实施项（后续阶段）

| 阶段 | 内容 |
| --- | --- |
| 阶段 2 | Transactional Outbox/Inbox、Task/Reply 协议 v2（event_id/generation/attempt）、`SubmitWorkflow` 单事务化、Replay 语义拆分 |
| 阶段 3 | Worker Operation Ledger、设备 Driver ensure 语义、Reply Outbox |
| 阶段 4 | Top 持久化 Reconciler、`operation_az_executions`、资源 generation/墓碑、Saga 外部幂等提交键 |
| 阶段 5 | VFW 链路统一到同一模型、幂等 SLO/告警/治理 |

## 4. 复跑方式

```bash
scripts/test-idempotency.sh
# 清理依赖容器
docker compose -f tests/idempotency/docker-compose.yml down -v
```
