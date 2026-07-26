# NSP 幂等整改实施总结

> 评审对象：研发、架构、测试及发布评审人员
> 对应设计：`docs/architecture/idempotency-analysis-and-design.md`
> 实施基线：`8daa377`（含 `b713e04`、`d6e7547`、`7dba578`、`8daa377`）
> 覆盖服务：Top NSP VPC/VFW、AZ NSP VPC/VFW、Worker
> 覆盖资源：VPC、Subnet、PCCN、VFW Policy

## 1. 结论

本次整改已经落地端到端幂等主链路：

- Northbound 和 AZ 写请求具备稳定 Operation、请求规范化、同 Key 重放和同 Key 异参冲突检测；
- Top 层具备 Saga 单次提交、持久化 per-AZ 执行记录和可重启 Reconciler；
- AZ 层具备资源、Task、Outbox 原子提交，以及 Reply Inbox、CAS、Generation Fence；
- Worker 具备执行 Ledger、可续租租约、设备状态查询/比较和 `EnsurePresent`/`EnsureAbsent` 语义；
- VPC、Subnet、PCCN、VFW 的重复创建、重复消息、迟到 Reply 和重复删除具备收敛保护；
- PostgreSQL 并发集成测试、Redis/Outbox 集成测试和单 AZ Docker E2E 已通过。

按原设计的最终目标衡量，本次可判定为“核心正确性链路完成，生产治理能力部分完成”。尚不能直接宣称完整生产级 exactly-once，主要原因是：当前设备 Driver 仍是数据库模拟实现，真实设备 SDK、完整反向补偿工作流、多 AZ 故障注入长稳测试、指标告警和归档治理仍待补齐。

## 2. 状态口径

本文使用以下状态：

| 状态 | 判定口径 |
| --- | --- |
| 已完成 | 设计目标已有实际代码和自动化测试证据，主链路可运行 |
| 部分完成 | 核心机制已实现，但仍有资源、故障模型、发布控制或运维治理缺口 |
| 未覆盖 | 不在本次实现范围，或仅存在脚手架、设计而没有可运行实现 |

“已完成”表示在当前 Demo 的 PostgreSQL、Redis/asynq 和模拟设备边界内满足定义的不变量，不等同于真实设备和生产环境已经完成验收。

## 3. 原设计问题与实现映射

| 编号 | 原设计问题 | 状态 | 本次实现 | 主要证据 |
| --- | --- | --- | --- | --- |
| A1 | Northbound 无幂等键和 Operation | 已完成 | Top VPC/PCCN/VFW 写请求接入 Operation；支持稳定重放、异参 409 和 Operation 查询 | `internal/operation`、`internal/top/api`、`internal/top/vfw/api` |
| A2 | Saga 幂等 Header 未被 AZ 消费 | 已完成 | AZ VPC/PCCN/VFW 消费 Saga/幂等身份并创建或重放 Operation | `internal/az/api`、`internal/az/vfw/api` |
| A3 | Saga 成功响应协议不兼容 | 已完成 | AZ 写接口返回 Saga 可识别的 `code: "0"` | AZ API 契约测试 |
| A4 | Resource/Task/Redis Publish 非原子 | 已完成 | Resource、Task、Outbox 在一个 PostgreSQL 事务中落库，异步 Dispatcher 投递 Redis | `internal/orchestration/durable_repository.go`、`outbox_dispatcher.go` |
| A5 | Worker 成功后 Reply 失败会重复设备操作 | 已完成（Demo 边界） | Worker Ledger、租约接管、状态查询/比较和 Ensure 语义；Reply 由 Outbox 重试 | `internal/worker`、migration 008 |
| A6 | Reply check-then-act 并发竞态 | 已完成 | Inbox 去重、Task CAS、终态保护和下一步 Outbox 同事务 | `internal/orchestration/reply_repository.go` |
| A7 | Reply 无 Attempt/Generation | 已完成 | Task/Reply v2 携带 Operation、Workflow、Generation、Step、Attempt、Event 等身份 | `tasks/protocol.go`、migration 006 |
| A8 | PCCN Resource/Task ID 不一致 | 已完成 | PCCN DAO 使用数据库实际持久化 ID 创建后续 Task | PCCN DAO 和回归测试 |
| A9 | DELETE/Compensation 非 ensure-absent | 部分完成 | 删除接口重复调用收敛到 absent；404 视为成功；Generation/Tombstone 隔离迟到 Reply；Worker 支持 EnsureAbsent | AZ/Top 删除实现、Worker 集成测试 |
| A10 | Top watcher 不可恢复 | 已完成 | DB 驱动 Reconciler 使用租约恢复 per-AZ 执行和聚合 | `internal/top/reconciler` |
| A11 | Top JSON 状态 read-modify-write 竞态 | 已完成 | per-AZ 执行独立持久化，VFW per-AZ 更新使用原子、带 Fence 的更新 | `operation_az_executions`、Reconciler 集成测试 |
| A12 | Task 最大重试次数未传 Broker | 已完成 | Workflow Task 的重试配置显式映射到 Broker Task | Workflow/协议回归测试 |
| A13 | Top VFW 受理即标记 running | 已完成 | 由持久化 per-AZ 执行结果聚合终态，不再以 fan-out 受理代替完成 | `internal/top/vfw`、Reconciler |
| A14 | 无幂等、并发、崩溃专项测试 | 部分完成 | 已覆盖真实 PostgreSQL 并发、Outbox 发布窗口、Worker 接管和 Docker E2E；尚缺长稳和完整进程 kill 矩阵 | 第 8 节 |
| A15 | Top VPC/VFW 无可信 caller scope | 部分完成 | Operation 已具备 caller scope 模型，但 Top VPC 当前仍强制关闭认证，生产租户隔离来源尚未闭环 | `internal/operation`、`cmd/top_nsp/main.go` |

## 4. 分层实施情况

### 4.1 Operation 与 HTTP 幂等

`internal/operation` 提供统一的请求身份和持久化语义：

1. 对业务请求做语义规范化并计算 `request_hash`；
2. 以 caller scope、幂等键和操作类型查找或创建 Operation；
3. 同 Key、同规范化参数返回原 Operation；
4. 同 Key、不同参数返回 HTTP 409，不覆盖第一次请求；
5. 将首次响应持久化，重放时返回稳定 Operation/Resource/Status；
6. 通过 `GET /api/v1/operations/:operation_id` 查询执行状态。

Operation 不只承担 API 去重，还作为 Top/AZ/Worker 身份链路的根。下游 Task 和 Reply 均可关联 root operation、当前 operation、resource、workflow 和 generation。

### 4.2 Top NSP：单次 Saga 提交与持久化 Reconciler

Top 层新增两项关键能力：

- `internal/top/sagaonce`：以稳定 external key 和 Saga definition hash 持久化提交意图；并发请求只提交一份 Saga，进程在 Saga 已持久化但关联未完成时可恢复；
- `internal/top/reconciler`：以 `operation_az_executions` 为扫描源，通过租约接管未完成执行，查询 AZ Operation 状态并聚合 Top 终态。

VPC、PCCN 和 VFW 已接入这一模型。PCCN 会保存 AZ 目标快照，避免重试或注册表变化后改变原操作的下游目标集合。VFW 的 per-AZ 状态更新使用带版本/世代约束的原子更新，避免多 Reconciler 相互覆盖。

### 4.3 AZ NSP：事务 Outbox、Inbox 与状态机

AZ 提交流程调整为：

```text
HTTP Operation
    -> PostgreSQL 事务：Resource + Task + Outbox
    -> Outbox Dispatcher
    -> Redis/asynq Worker Queue
```

数据库提交不再依赖 Redis 当次可用。Dispatcher 使用租约认领待发送事件，支持失败重试、进程接管和发布后标记前崩溃恢复。即使发生重复发布，下游仍由 Worker Ledger 和 Reply Inbox 去重。

Reply 消费调整为：

```text
Reply
    -> Inbox event_id 去重
    -> 校验 operation/workflow/resource/generation/step/attempt
    -> Task CAS / 终态保护
    -> 更新 Resource/Workflow
    -> 创建下一步 Outbox
    -> 同一 PostgreSQL 事务提交
```

旧 Generation、已删除资源和身份不匹配的 Reply 不会推进当前工作流。v1 Reply 兼容通过 `NSP_WORKFLOW_V1_REPLY_ENABLED` 控制，默认关闭。

### 4.4 Worker：Ledger、租约与 Ensure

Worker 运行时位于 `internal/worker`，核心执行顺序为：

1. 以稳定 `operation_key` 读取或创建 `worker_operations`；
2. 校验相同 operation key 必须对应相同 `desired_hash`；
3. 认领并持续续租执行权；
4. 通过 Driver `Get`/`Compare` 判断设备当前状态；
5. 仅在必要时调用 `EnsurePresent` 或 `EnsureAbsent`；
6. 持久化执行结果，并通过 Reply Outbox 重试回送。

如果 Worker 在 Ensure 成功后、Ledger 标记成功前崩溃，接管者先查询设备状态；状态已经符合期望时不再盲目重复变更。

当前 `sqlDeviceDriver` 使用 `worker_device_state` 模拟可查询设备状态。这证明了运行时协议和恢复算法，但还不是实际交换机、防火墙或负载均衡器的 SDK 集成。

### 4.5 删除、重建与补偿

本次已实现的删除正确性包括：

- 重复 DELETE 在资源存在、删除中和已不存在时均向 absent 收敛；
- 下游返回 404 视为删除成功；
- 删除通过 target claim 的 retiring、active 和 generation 阻止旧创建重新激活资源；
- 删除后重建使用新 generation，旧 Task/Reply 无权修改新实例；
- VPC 删除对 Subnet 和 Firewall Policy 依赖采取 fail-closed；
- PCCN、Subnet、VFW、VPC 的重复删除进入 E2E 验证。

仍需注意：当前并非所有资源都已把“删除”和每个反向补偿步骤建模为完整、独立、可查询的持久化 Operation/Worker 工作流。现有实现已保证 API 和状态收敛，但全面的反向设备步骤编排仍属于后续工作。

## 5. 数据库变更

本次使用 4 个增量迁移，均采用可重复执行的 `IF NOT EXISTS`/兼容性 DDL。不要把它们误认为只需部署到一个共享数据库；脚本会按各服务数据库现有表结构判断角色。

### 5.1 Migration 005：Operation、幂等别名和目标占用

文件：`internal/db/migrations/005_create_operations.sql`

| 对象 | 用途 |
| --- | --- |
| `orchestration_operations` | 持久化请求 Hash、Operation 类型、状态、响应和错误 |
| `orchestration_idempotency_aliases` | 将 caller scope/幂等键映射到稳定 Operation |
| `orchestration_target_claims` | 对业务目标进行 active/retiring 占用，管理 generation 和重建 |
| `operation_az_executions` | 持久化 Top 到各 AZ 的子 Operation、状态、租约和聚合信息 |

迁移还包含存量 Top 资源的兼容回填和 synthetic claim，避免升级后把已有资源当作新目标重新创建；VFW per-AZ 记录增加 Region 维度及唯一约束。

### 5.2 Migration 006：Task v2、Outbox 与 Inbox

文件：`internal/db/migrations/006_create_outbox_inbox.sql`

主要变更：

- `tasks` 增加 operation、root operation、workflow、generation、step、attempt、protocol version 和 version 等字段；
- 为 v2 工作流步骤增加唯一约束，避免同一 workflow/generation/step 产生多份业务 Task；
- AZ 资源表增加 current operation、generation、version 和 `deleted_at`；
- 新增 `outbox_events`，保存待发送事件、发送状态、重试次数、租约和错误；
- 新增 `inbox_events`，以 event identity 保证 Reply 业务处理最多一次。

### 5.3 Migration 007：Top Saga 单次提交

文件：`internal/db/migrations/007_create_top_saga_submissions.sql`

新增 `top_saga_submissions`：

- `external_key` 为外部稳定提交键；
- `operation_id` 关联根 Operation；
- `definition_hash`/`definition_payload` 防止同 Key 被不同 Saga 定义复用；
- `saga_transaction_id` 保存实际 Saga 事务；
- Saga payload 上的 external key 唯一索引用于处理“Saga 已落库、关联记录尚未更新”的恢复窗口。

仅 Top VPC 且存在 Saga 表的数据库需要此迁移。

### 5.4 Migration 008：Worker Ledger 与模拟设备状态

文件：`internal/db/migrations/008_create_worker_ledger.sql`

| 对象 | 用途 |
| --- | --- |
| `worker_operations` | 保存执行身份、desired hash、状态、结果、错误和可续租租约 |
| `worker_device_state` | Demo Driver 的可查询设备状态，用于验证接管后的 Get/Compare/Ensure |

该迁移应用到包含 AZ `tasks` 表的数据库。Docker Worker 已补充 PostgreSQL 连接变量，以共享相应 AZ 的 Ledger。

## 6. API 与消息协议变化

### 6.1 HTTP

- 写请求支持 `X-Idempotency-Key`；
- Top 到 AZ 继续传递 Saga/幂等身份；
- 成功响应统一包含 Saga 可识别的 `code: "0"`；
- Operation 响应暴露 `operation_id`、资源身份和状态；
- Top VPC、Top VFW、AZ VPC、AZ VFW 均提供：

```text
GET /api/v1/operations/:operation_id
```

- 同 Key、不同规范化请求返回 409；
- 同 Key、同请求在进行中或完成后均返回原 Operation，而不是创建第二份资源或工作流。

### 6.2 Task/Reply v2

v2 身份至少覆盖：

- protocol version；
- root operation ID / operation ID；
- workflow ID / task ID；
- resource ID / generation；
- step / attempt；
- event ID；
- desired state hash 或等价执行摘要。

身份不完整、Hash 冲突、旧 generation、终态后的迟到 Reply 均不会作为合法状态推进。

## 7. 关键正确性不变量

当前实现和测试重点维护以下不变量：

1. 同一幂等作用域和 Key 最多对应一个 Operation；
2. 同一 Key 不能对应两个不同请求 Hash；
3. 同一 workflow/generation/step 最多对应一个业务 Task；
4. 同一 Reply event 最多改变一次业务状态；
5. Task 和 Resource 终态不能被迟到 Reply 回退；
6. 旧 generation 不能改变删除后重建的新实例；
7. 已提交的 Outbox 事件在 Redis 恢复后仍可被发现和发送；
8. 同一 Worker operation key 不能对应不同 desired hash；
9. Worker 接管时先查询状态，避免盲目重复外部副作用；
10. 重复删除最终收敛到数据库和模拟设备状态均 absent；
11. Top/AZ/Worker 重启或租约过期后，未完成操作可被其他实例接管。

## 8. 测试覆盖与验证结果

### 8.1 自动化覆盖

| 层级 | 已覆盖场景 | 主要测试 |
| --- | --- | --- |
| Operation/DAO | 顺序重放、异参冲突、并发单赢家、响应 CAS、目标规格冲突、新 generation、租约续期/接管/防栅栏、删除资源迁移不重激活 | `internal/operation/repository_integration_test.go` |
| HTTP 契约 | Top/AZ VPC/VFW 同 Key 重放、异参 409、并发请求只产生一份 Operation、Operation 查询、Saga `code` | `internal/*/api/*idempotency*_test.go` |
| AZ Durable Workflow | 原子提交/回滚/重放、Outbox 租约恢复/死信/并发认领、Redis 发布窗口、重复 Reply、身份冲突、旧 generation、删除后迟到 Reply | `internal/orchestration/durable_integration_test.go` |
| Top Saga | 并发单次 Saga、Saga 持久化后崩溃恢复、未关联 Saga 解析 | `internal/top/sagaonce/service_integration_test.go` |
| Top Reconciler | 重启恢复、多 Reconciler 抢占、补偿结果聚合、VFW 原子 fenced 更新 | `internal/top/reconciler/reconciler_integration_test.go` |
| Worker | 成功重放跳过 Handler、desired hash 冲突、Reply 重试、Ensure 后崩溃接管、删除 EnsureAbsent、慢 Handler 续租 | `internal/worker/runtime_integration_test.go` |
| 删除 | AZ 重复删除、删除和迟到消息收敛 | `internal/az/orchestrator/delete_idempotency_integration_test.go` |
| Docker E2E | 固定 Key VPC 重放 10 次、同 Key 异参 409、PCCN/Subnet/VFW/VPC 重复删除 | `tests/e2e/e2e_test.go` |

### 8.2 本次基线实际执行结果

在提交 `8daa377` 上已执行：

```bash
git diff --check
go vet ./...
go test <除 tests/e2e、tests/functional 外的全部 Go package>
go test -race -p 1 <PostgreSQL/Redis 相关集成测试 package>
sudo -E /usr/local/go/bin/go test ./tests/e2e -count=1
```

结果：

- 差异检查通过；
- `go vet ./...` 通过；
- 非 E2E/Functional Go package 全部通过；
- PostgreSQL/Redis 集成测试在 Race Detector 下通过；
- 单 AZ Docker E2E 通过，耗时约 68 秒；
- E2E 完成后测试栈和临时 PostgreSQL/Redis 容器已停止。

### 8.3 尚未覆盖的验证

- 未执行完整多 Region、多 AZ Docker 故障矩阵；本次受运行环境内存限制采用单 AZ E2E；
- `tests/functional` 依赖独立 PostgreSQL 实例、预置 Schema 和外部凭据，本次未作为最终基线执行；相应数据库正确性由 package 级真实 PostgreSQL 集成测试和 Docker E2E 覆盖；
- 尚未把随机乱序、长时间 Redis/PostgreSQL 中断、网络分区和各进程 kill 点全部纳入 CI；
- 尚未针对真实交换机、防火墙和负载均衡器 SDK 执行设备幂等测试；
- 尚未进行长稳、容量和 Outbox/Inbox/Ledger 数据增长测试。

## 9. 部署升级步骤

### 9.1 前置检查

1. 记录当前应用镜像、Git 版本和数据库版本；
2. 备份所有 Top/AZ PostgreSQL 数据库，并验证备份可恢复；
3. 检查 PostgreSQL、Redis Cluster、Saga 表和各服务健康状态；
4. 统计旧 v1 队列积压，明确最大 retry/retention 窗口；
5. 在维护窗口内停止旧版本写流量，或确保发布过程不会出现 v2 Producer 对接 v1-only Consumer。

### 9.2 执行迁移

对现有 Docker 数据卷执行：

```bash
cd deployments/docker
./run-idempotency-migrations.sh
```

E2E Compose 使用：

```bash
./run-idempotency-migrations.sh docker-compose.e2e.yml
```

脚本使用 `ON_ERROR_STOP`，遍历非模板数据库并按表结构判断角色：

1. 所有 NSP Top/AZ 数据库执行 005；
2. 存在 `tasks` 表的 AZ 数据库执行 006 和 008；
3. 同时存在 `saga_transactions` 和 `vpc_registry` 的 Top VPC 数据库执行 007。

新建 Docker 环境由 `init-postgres.sh` 或 `init-postgres-e2e.sh` 初始化 001/004/005/006/007/008，无需另行手工建表。

### 9.3 推荐应用升级顺序

1. 部署具备 Task/Reply v2 和 Worker Ledger 能力的 Worker；
2. 部署具备 v2 Reply、Inbox/CAS 和 Outbox 能力的 AZ NSP VPC/VFW；
3. 确认 AZ Dispatcher、Worker Ledger 和 Reply 消费正常；
4. 部署具备 Operation、SagaOnce 和 Reconciler 的 Top NSP VPC/VFW；
5. 恢复写流量，先以少量 VPC/VFW 请求灰度；
6. 验证 Operation 重放、409 冲突、Outbox 清空、Worker Operation 完成和 per-AZ 聚合；
7. 在超过旧消息最大 retry/retention 窗口且确认无 v1 消息后，保持 `NSP_WORKFLOW_V1_REPLY_ENABLED=false`。

如确有未排空的 v1 Reply，可短时设置：

```text
NSP_WORKFLOW_V1_REPLY_ENABLED=true
```

仅用于兼容窗口，排空后必须恢复 `false`。不要在 v2 Producer 已运行时回退到 v1-only Consumer。

### 9.4 升级后检查

至少检查：

- 相同 Key/相同 Body 返回相同 `operation_id`；
- 相同 Key/不同 Body 返回 409；
- `outbox_events` 无持续增长的 pending/retry/publishing；
- `operation_az_executions` 无异常长期租约或 stuck running；
- `worker_operations` 无持续增长的过期 running；
- Redis task/reply 队列最终下降；
- 重复 DELETE 返回收敛成功；
- Top Operation 终态与各 AZ Operation 终态一致。

## 10. 回滚原则

迁移以增量表和增量列为主，应用回滚时应保留 005–008 Schema，不建议在故障窗口执行破坏性降级 DDL。

安全回滚顺序：

1. 停止新写流量和 Top Reconciler；
2. 停止 AZ Outbox Dispatcher，确认并记录 pending/retry/publishing 事件；
3. 确认 Redis 中不存在旧应用无法理解的 v2 Task/Reply；必要时保持 v2 Consumer 运行至排空；
4. 确认 Worker Ledger 中 running 执行已完成或租约已安全释放；
5. 仅在消息协议兼容条件满足后回退应用镜像；
6. 保留 Operation、Outbox、Inbox、Saga submission 和 Worker Ledger 数据，供恢复或审计使用。

禁止场景：

- v2 Producer 继续发送时回退为 v1-only Consumer；
- Outbox 仍处于 publishing 时直接切换到绕过 Ledger/Inbox 的旧路径；
- 删除新增表后再尝试恢复未完成 Operation；
- 将设备执行“可能成功但未知”的任务直接按失败重做。

## 11. 剩余限制、风险与建议

| 优先级 | 限制/风险 | 影响 | 建议 |
| --- | --- | --- | --- |
| P0 | 真实设备 Driver 未接入 | 模拟状态证明算法，但不能证明厂商设备请求令牌、查询一致性和冲突码行为 | 为 switch/firewall/loadbalancer 分别实现并认证 Get/Compare/Ensure；建立设备能力矩阵 |
| P0 | 删除/补偿未全部建模为独立持久化反向工作流 | API 已收敛，但复杂部分失败时的逐设备反向步骤可观测性不足 | 为每个资源建立 Delete Operation、反向 Task 图和补偿 Ledger |
| P1 | 多 AZ/多 Region 故障注入不足 | 不能完整证明网络分区和大规模并发下的收敛时间 | 在 CI 或专用环境增加多 AZ kill/断网/Redis/PostgreSQL 短时不可用测试 |
| P1 | 指标、告警和人工恢复工具未完整落地 | stuck Operation、Outbox、租约和死信主要依赖 SQL/日志排查 | 实现设计文档 §15.4 指标、Dashboard、告警及安全重放/修复命令 |
| P1 | 数据保留和归档策略未落地 | Operation、Inbox、Outbox、Ledger 长期增长 | 定义按终态和审计周期归档/清理的 Job 与 SLO |
| P1 | 发布 Feature Flag 不完整 | 当前只有 v1 Reply 兼容开关，无法逐层独立灰度所有能力 | 增加 API enforce、Top Reconciler、Worker Ledger 等显式灰度开关 |
| P1 | DELETE Operation 查询协议不完全统一 | 重复删除能收敛，但删除过程的统一 Operation 可见性不足 | 统一 create/delete/compensate Operation 类型和查询响应 |
| P1 | caller scope 信任链未闭环 | Top VPC 强制关闭 Auth 时，生产多租户幂等作用域不可信 | 启用并验证 AK/SK/租户身份，将认证主体作为 caller scope |
| P2 | ELB/NAT 未纳入业务验收 | ELB 只有脚手架，NAT 未实现 | 新服务接入时强制复用 Operation/Outbox/Inbox/Ledger 契约 |
| P2 | 长稳与容量基线缺失 | 暂无积压恢复时间、表增长和吞吐上限数据 | 增加 24h/72h soak、批量重放和故障恢复性能测试 |

## 12. 代码与测试证据索引

| 能力 | 代码/文档位置 |
| --- | --- |
| 原始分析与目标设计 | `docs/architecture/idempotency-analysis-and-design.md` |
| 发布与兼容手册 | `docs/architecture/idempotency-rollout-runbook.md` |
| Operation/规范化/Repository | `internal/operation` |
| Top VPC/PCCN 编排 | `internal/top/orchestrator` |
| Top Saga 单次提交 | `internal/top/sagaonce` |
| Top 持久化 Reconciler | `internal/top/reconciler` |
| Top VFW API/Service/DAO | `internal/top/vfw` |
| AZ VPC/Subnet/PCCN 编排 | `internal/az/orchestrator` |
| AZ VFW 编排 | `internal/az/vfw` |
| Outbox/Inbox/Reply CAS | `internal/orchestration` |
| Worker Ledger/Driver/Reply Outbox | `internal/worker` |
| Task/Reply v2 协议 | `tasks/protocol.go` |
| Worker Handler 注册 | `cmd/worker/main.go` |
| 数据库迁移 | `internal/db/migrations/005_create_operations.sql` 至 `008_create_worker_ledger.sql` |
| 存量库迁移脚本 | `deployments/docker/run-idempotency-migrations.sh` |
| Docker E2E | `tests/e2e/e2e_test.go`、`deployments/docker/docker-compose.e2e.yml` |

## 13. 研发评审检查清单

### 13.1 正确性

- [ ] 同 Key 同 Body 的响应和 Operation 身份稳定；
- [ ] 同 Key 不同 Body 稳定返回 409；
- [ ] Top、AZ、Task、Reply、Worker Ledger 的身份可以沿链路关联；
- [ ] Outbox/Inbox 的事务边界符合代码实现；
- [ ] Task/Resource 终态和 generation fence 不可被迟到消息突破；
- [ ] Top SagaOnce 和 Reconciler 在多实例下只有合法持有者推进；
- [ ] Worker 接管时不会盲目重复设备变更；
- [ ] 重复 DELETE 和 404 下游响应收敛到 absent。

### 13.2 数据库与兼容

- [ ] 005–008 在各数据库的应用范围正确；
- [ ] 存量资源 backfill/target claim 不会重新激活已删除资源；
- [ ] DDL 在目标 PostgreSQL 版本上完成演练并记录耗时；
- [ ] 旧 v1 消息已经清点并有明确排空窗口；
- [ ] 回滚期间不会形成 v2 Producer 对 v1-only Consumer；
- [ ] Schema、Operation 和 Ledger 数据在应用回滚时保留。

### 13.3 测试与发布

- [ ] 本文第 8.2 节命令在候选提交上重新执行通过；
- [ ] 增加生产目标规模下的多 AZ 故障注入结果；
- [ ] 真实设备 Driver 完成幂等能力认证；
- [ ] Outbox、stuck Operation、过期 Worker lease 和 dead-letter 告警可用；
- [ ] 灰度观察重复率、冲突率、stale Reply 和最终收敛时长；
- [ ] 明确 v1 兼容关闭日期及 Operation/Inbox/Outbox/Ledger 保留周期。

## 14. 评审建议

本次代码可以按“Demo 架构的持久化幂等主链路”进行合并评审。研发评审重点应放在事务边界、状态机单调性、租约 Fence、Generation 隔离和升级顺序，而不是只检查唯一索引。

进入生产集成前，建议将以下三项设为强制门槛：

1. 完成真实设备 Driver 的 Get/Compare/Ensure 能力和故障语义认证；
2. 完成多 AZ 进程 kill、网络分区和 Redis/PostgreSQL 短时中断测试；
3. 完成 stuck Operation、Outbox、Worker Lease、dead-letter 指标告警和人工恢复手册。
