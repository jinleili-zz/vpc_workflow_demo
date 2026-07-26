# NSP Effectively-once Idempotency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the Operation, Generation, transactional Outbox/Inbox, Worker Ledger, monotonic state-machine, deletion/compensation, and persistent reconciliation requirements in `docs/architecture/idempotency-analysis-and-design.md`, with real PostgreSQL/Redis tests proving the documented invariants.

**Architecture:** PostgreSQL is the correctness source. Every write request first resolves to one durable operation through a unique idempotency scope and canonical request hash. AZ workflow submission and reply advancement commit Tasks and Outbox/Inbox facts atomically; Redis/asynq remains at-least-once. Workers coordinate stable device operation keys through a ledger and an ensure-style driver. Top aggregation is driven by persisted per-AZ execution records and a restart-safe reconciler.

**Tech Stack:** Go 1.25.6, PostgreSQL, Redis/asynq via `nsp-platform/taskqueue`, Gin, `database/sql`, Docker Compose, Go unit/integration tests.

## Global Constraints

- Preserve VPC, Subnet, PCCN, and VFW public routes while adding the common operation response and `GET /api/v1/operations/:operation_id`.
- During the compatibility window, missing northbound `Idempotency-Key` is accepted with a generated non-replayable key; Saga-to-AZ requests require and consume their existing stable key.
- Caller scope comes from authenticated identity when present and from a configured service namespace otherwise; source IP is never a correctness boundary.
- A transaction may write PostgreSQL facts only; it must never block on Redis or a device call.
- Task, Reply, Outbox, Inbox, and Worker Ledger identities remain stable across delivery retries.
- Existing untracked files outside this plan are user-owned and must not be staged.

---

## File Structure

- `internal/db/migrations/005_create_operations.sql`: operation and per-AZ execution schema.
- `internal/db/migrations/006_create_outbox_inbox.sql`: durable event delivery and consumption schema.
- `internal/db/migrations/007_add_generation_and_worker_ledger.sql`: generation/task fencing and worker ledger schema.
- `internal/operation/`: canonical hashing, models, repository, service, HTTP context/error mapping.
- `internal/orchestration/`: v2 envelopes, transactional workflow repository, dispatcher, reply application, replay rules.
- `internal/worker/`: ledger repository, lease coordination, ensure-style drivers, executor.
- `internal/top/reconciler/`: persisted per-AZ aggregation and restart recovery.
- Existing API/orchestrator/DAO/main packages: thin adapters and dependency wiring only.
- `internal/**/_test.go`, `tests/functional`, and `tests/e2e`: unit, real-database concurrency, component, and fault-injection coverage.

### Task 1: Lock Down Stage-0 Protocol and Deterministic Defects

**Files:**
- Modify: `internal/models/types.go`
- Modify: `internal/models/firewall.go`
- Modify: `internal/az/api/server.go`
- Modify: `internal/az/vfw/api/server.go`
- Modify: `internal/db/dao/pccn_dao.go`
- Modify: `internal/az/orchestrator/orchestrator.go`
- Modify: `internal/orchestration/workflow.go`
- Modify: `internal/db/dao/task_dao.go`
- Test: `internal/az/api/idempotency_contract_test.go`
- Test: `internal/az/vfw/api/idempotency_contract_test.go`
- Test: `internal/db/dao/pccn_dao_integration_test.go`
- Test: `internal/orchestration/workflow_test.go`

**Interfaces:**
- Produces: response field `Code string \`json:"code"\``; `PCCNDAO.Create(ctx, pccn) (*models.PCCNResource, error)` returning the persisted identity; `TaskStore.CompleteOnce(...) (bool, error)`; Broker tasks with `MaxRetry: &task.MaxRetries`.

- [ ] Write contract tests proving successful AZ VPC/PCCN/VFW writes include JSON `code: "0"` and Saga headers are captured in request context/log fields.
- [ ] Run the focused API tests and verify they fail because the response DTOs omit `code`.
- [ ] Add `Code`, `OperationID`, `ResourceID`, and `Status` fields without removing legacy response fields; rerun and verify green.
- [ ] Write a PostgreSQL test that inserts a PCCN, repeats the same natural key with a different supplied ID, and asserts the DAO returns the original persisted ID and does not create orphan tasks.
- [ ] Run it against the disposable PostgreSQL and verify the old `Create` contract fails.
- [ ] Change PCCN insertion to `INSERT ... ON CONFLICT ... RETURNING` and require orchestrators to use the returned row identity; rerun green.
- [ ] Write a broker-spy test asserting `publishTask` maps database `MaxRetries` to `taskqueue.Task.MaxRetry`; verify red, implement the pointer mapping, and verify green.
- [ ] Write a concurrent reply regression test where many identical terminal replies race; assert only one CAS winner, one resource terminal increment, and one next-step emission; verify red, implement `RowsAffected`-checked CAS, and verify green.

### Task 2: Add Durable Operation Identity and Canonical Request Hashing

**Files:**
- Create: `internal/db/migrations/005_create_operations.sql`
- Create: `internal/operation/model.go`
- Create: `internal/operation/canonical.go`
- Create: `internal/operation/repository.go`
- Create: `internal/operation/service.go`
- Create: `internal/operation/http.go`
- Test: `internal/operation/canonical_test.go`
- Test: `internal/operation/repository_integration_test.go`

**Interfaces:**
- Produces: `Begin(ctx context.Context, cmd BeginCommand) (*Operation, Decision, error)`, where `Decision` is `new`, `replay`, or `conflict`; `Get(ctx, operationID)`; `CanonicalHash(version int16, target string, payload any) (string, json.RawMessage, error)`.

- [ ] Write canonicalization tests proving object key order is ignored, array order is preserved, target/path values affect the hash, and secrets are excluded.
- [ ] Run them and verify red because `internal/operation` does not exist.
- [ ] Implement version-1 canonical JSON and SHA-256 hashing; rerun green.
- [ ] Add the operation/per-AZ migration with exact unique and reconciliation indexes from the design, using repository string IDs consistently with existing resource tables.
- [ ] Write real PostgreSQL tests for sequential replay, 100-way concurrent replay, same-key/different-body conflict, same natural resource/different-key arbitration, and stable stored response replay.
- [ ] Run them and verify red before adding repository code.
- [ ] Implement insert-first conflict resolution using the unique constraint as the linearization point; never use check-then-insert.
- [ ] Add monotonic `UpdateStatusCAS` and `StoreResponseCAS` methods with version checks; rerun all operation tests green.

### Task 3: Make AZ Workflow Submission and Reply Advancement Transactional

**Files:**
- Create: `internal/db/migrations/006_create_outbox_inbox.sql`
- Modify: `internal/db/migrations/007_add_generation_and_worker_ledger.sql`
- Modify: `internal/models/resource.go`
- Modify: `internal/orchestration/types.go`
- Create: `internal/orchestration/repository.go`
- Create: `internal/orchestration/outbox.go`
- Modify: `internal/orchestration/workflow.go`
- Modify: `internal/db/dao/task_dao.go`
- Modify: `internal/db/dao/dao.go`
- Modify: `internal/az/vfw/dao/dao.go`
- Test: `internal/orchestration/repository_integration_test.go`
- Test: `internal/orchestration/outbox_integration_test.go`

**Interfaces:**
- Consumes: Task 2 operation identity.
- Produces: `SubmitTx(ctx, SubmitWorkflowCommand) (*Workflow, error)` and `ApplyReplyTx(ctx, ReplyV2) (ApplyReplyResult, error)`; `OutboxDispatcher.Run(ctx)`; schema-v2 `TaskEnvelope` and `ReplyEnvelope` carrying every documented identity field.

- [ ] Write schema tests rejecting v2 Task/Reply messages missing event, operation, workflow, resource, generation, task, step, attempt, operation-key, or desired-hash identity.
- [ ] Verify red, then add the v2 DTOs and validation until green.
- [ ] Add generation/version/current-operation columns, the workflow-step unique index, outbox/inbox tables, dispatch/lease indexes, and safe idempotent migration guards.
- [ ] Write PostgreSQL tests proving resource + tasks + first Outbox commit or roll back together, and one workflow/generation/step cannot produce duplicate Tasks.
- [ ] Verify red, implement `SubmitTx` using one `sql.Tx`, and verify green.
- [ ] Write tests proving duplicate `event_id` is a no-op, same event/different payload is a conflict, old generation is stale, future generation is invalid, wrong task/workflow is invalid, old attempt cannot override a new terminal result, and only a CAS winner creates the next Outbox.
- [ ] Verify red, implement `ApplyReplyTx`, then rerun including a concurrent 100-reply test.
- [ ] Write Redis component tests for DB-success/Redis-down, Redis-success/mark-before-crash, expired dispatcher lease takeover, retry backoff, and dead-letter threshold.
- [ ] Implement claim/publish/mark as separate short transactions and verify recovery plus Inbox deduplication.
- [ ] Replace direct workflow publishing with durable Outbox; keep a bounded v1 consumer during migration only.

### Task 4: Wire Strict HTTP Idempotency Across Top and AZ APIs

**Files:**
- Modify: `internal/top/api/server.go`
- Modify: `internal/top/vfw/api/server.go`
- Modify: `internal/az/api/server.go`
- Modify: `internal/az/vfw/api/server.go`
- Modify: `internal/top/orchestrator/orchestrator.go`
- Modify: `internal/top/vfw/service/policy.go`
- Modify: `internal/az/orchestrator/orchestrator.go`
- Modify: `internal/az/vfw/orchestrator/orchestrator.go`
- Modify: `internal/client/az_client.go`
- Test: `internal/top/api/idempotency_test.go`
- Test: `internal/top/vfw/api/idempotency_test.go`
- Test: `internal/az/api/idempotency_test.go`
- Test: `internal/az/vfw/api/idempotency_test.go`
- Test: `internal/client/az_client_test.go`

**Interfaces:**
- Consumes: `operation.Service`, schema-v2 workflow submission.
- Produces: stable `X-Root-Operation-Id`, `X-Parent-Operation-Id`, `X-Idempotency-Key`, `X-Saga-Transaction-Id`, and `X-Resource-Generation`; `GET /api/v1/operations/:operation_id` on all four NSP APIs.

- [ ] Write handler tests for new/replay/conflict/invalid-key/generated-key compatibility and stable stored responses for VPC, Subnet, PCCN, and VFW.
- [ ] Verify red before introducing shared HTTP helpers.
- [ ] Implement caller-scope extraction, key validation, compatibility warning header, normalized route scope, and error mapping (`400`, `409`, `503`).
- [ ] Refactor handlers to begin/load the Operation before orchestration and persist every post-begin error/result; only the `new` decision may dispatch.
- [ ] Add operation lookup routes and current-operation/generation fields to resource status responses.
- [ ] Write client tests for all stable parent/root/generation/Saga headers and `code == "0"` response parsing; verify red, implement, and rerun green.
- [ ] Add AZ tests proving a retried Saga Step returns the original child Operation/Resource/Workflow without adding Tasks.
- [ ] Run 100-way handler concurrency tests against real PostgreSQL and verify one Operation and one workflow.

### Task 5: Add Worker Ledger and Ensure-style Device Execution

**Files:**
- Complete: `internal/db/migrations/007_add_generation_and_worker_ledger.sql`
- Create: `internal/worker/driver.go`
- Create: `internal/worker/ledger.go`
- Create: `internal/worker/executor.go`
- Create: `internal/worker/simulated_driver.go`
- Modify: `tasks/handlers.go`
- Modify: `tasks/pccn_handlers.go`
- Modify: `cmd/worker/main.go`
- Test: `internal/worker/executor_integration_test.go`
- Test: `tasks/handlers_test.go`

**Interfaces:**
- Produces: `DeviceDriver.Get`, `EnsurePresent`, and `EnsureAbsent`; `Executor.Execute(ctx, TaskEnvelope, desired) (ReplyEnvelope, error)`; durable Worker result and Reply Outbox.

- [ ] Write desired-state and deterministic target/operation-key tests for every existing switch, firewall, loadbalancer, and PCCN task.
- [ ] Verify red, implement the common driver interface and simulated queryable state, then verify green.
- [ ] Write PostgreSQL concurrency tests proving one lease holder executes, matching succeeded Ledger entries replay stored results, and the same operation key with a different desired hash conflicts.
- [ ] Verify red, implement insert/CAS lease acquisition and result persistence with Reply Outbox, then verify green.
- [ ] Add failpoint tests before Ensure, after Ensure/before Ledger commit, after Ledger commit/before Reply publish, and after Reply publish; verify takeover performs Get/Compare before Ensure and never creates a second logical object.
- [ ] Convert task handlers to decode/validate v2 envelopes and delegate execution; register create/delete handlers including PCCN deletion.
- [ ] Verify repeated delivery N times produces one logical simulated device object and one replayable business result.

### Task 6: Persist Top per-AZ Aggregation and Reconcile After Restart

**Files:**
- Create: `internal/top/reconciler/reconciler.go`
- Create: `internal/top/reconciler/repository.go`
- Modify: `internal/top/orchestrator/orchestrator.go`
- Modify: `internal/top/vfw/service/policy.go`
- Modify: `cmd/top_nsp/main.go`
- Modify: `cmd/top_nsp_vfw/main.go`
- Test: `internal/top/reconciler/reconciler_integration_test.go`

**Interfaces:**
- Consumes: `operation_az_executions` and AZ operation query API.
- Produces: lease-safe `RunOnce(ctx)` and `Run(ctx)` reconciliation; monotonic Top operation aggregation shared by VPC, PCCN, and VFW.

- [ ] Write aggregation table tests for all documented combinations: succeeded, running, failed, compensating, compensated, and compensation-failed.
- [ ] Verify red, implement pure aggregation, and verify green.
- [ ] Write PostgreSQL tests proving independent per-AZ CAS updates, two reconcilers cannot regress a terminal state, and expired leases are reclaimable.
- [ ] Implement persisted claims and aggregation updates; rerun green.
- [ ] Replace correctness dependence on request-local watcher goroutines with operation/per-AZ rows and start reconcilers from both Top binaries.
- [ ] Add restart tests that stop a reconciler mid-operation, create a new instance, and verify it completes from database facts without resubmitting child operations.
- [ ] Update Top VFW so AZ `accepted` never becomes Top `succeeded` until every child operation is terminal.

### Task 7: Make Delete, Compensation, and Replay Convergent

**Files:**
- Modify: `internal/az/orchestrator/orchestrator.go`
- Modify: `internal/az/vfw/orchestrator/orchestrator.go`
- Modify: `internal/top/orchestrator/orchestrator.go`
- Modify: `internal/top/vfw/service/policy.go`
- Modify: `internal/orchestration/repository.go`
- Modify: `tasks/handlers.go`
- Modify: `tasks/pccn_handlers.go`
- Test: `internal/az/orchestrator/delete_integration_test.go`
- Test: `internal/orchestration/replay_integration_test.go`

**Interfaces:**
- Produces: delete Operations ending in `deleted`/`delete_failed`; transport requeue reuses task identity and increments attempt; business re-execution creates a new Operation/workflow or generation.

- [ ] Write tests for delete when present, deleting, deleted, and missing; every case must return/replay a successful ensure-absent Operation.
- [ ] Verify red, add tombstone/generation arbitration and device delete workflows, then verify green.
- [ ] Write tests proving repeated Saga compensation uses the stable compensation key and cannot delete a later generation.
- [ ] Implement persisted compensation tasks and generation fencing; rerun green.
- [ ] Write replay tests proving successful Tasks cannot be reset, transport replay increments attempt on the same Task, and business replay requires a new Idempotency-Key and audit reason.
- [ ] Split the replay service/handler semantics and verify old-attempt Reply ordering cannot alter the current terminal result.

### Task 8: Prove the End-to-End Invariants With Real Services and Fault Injection

**Files:**
- Modify: `deployments/docker/init-postgres.sh`
- Modify: `deployments/docker/init-postgres-e2e.sh`
- Modify: `deployments/docker/docker-compose.e2e.yml`
- Modify: `tests/functional/functional_test.go`
- Modify: `tests/e2e/e2e_test.go`
- Create: `tests/idempotency/idempotency_test.go`
- Create: `tests/idempotency/failpoints.go`

**Interfaces:**
- Consumes: all earlier tasks.
- Produces: automated evidence for every row in section 15.2 and every global invariant in section 15.1.

- [ ] Start one PostgreSQL and one Redis instance to stay within the memory limit; create logical databases for Top/AZ/VFW/Worker roles and apply migrations.
- [ ] Add reusable barrier, connection-drop, broker outage, process restart, and failpoint controls; no pure-mock database test may stand in for isolation/locking behavior.
- [ ] Implement the section 15.2 matrix as table-driven subtests, including 100 sequential/concurrent POSTs, response loss, every DB/Redis boundary, concurrent Reply, attempt/generation reordering, repeat delete/compensation, service restarts, worker takeover, PCCN concurrency, and VFW partial failure.
- [ ] For each fault case, first run against the pre-fix path/failpoint and record the expected invariant failure, then run the new path and assert final convergence.
- [ ] Add database invariant queries for uniqueness, task terminality, counters, pending Outbox discoverability, ledger uniqueness, tombstones, and terminal-state monotonicity.
- [ ] Run unit tests, real PostgreSQL functional tests, Redis component tests, E2E idempotency tests, `go test -race ./...`, and all five documented `go build` commands.

### Task 9: Review, Documentation, and GitHub Delivery

**Files:**
- Modify: `docs/architecture/idempotency-analysis-and-design.md`
- Create: `docs/idempotency-operations-runbook.md`
- Modify: `README.md`

**Interfaces:**
- Produces: rollout/rollback instructions, observable operational recovery, reviewed commit, pushed branch, and draft PR.

- [ ] Mark design checklist items only where automated or runtime evidence exists; retain explicit gaps rather than weakening the standard.
- [ ] Document migration order, feature switches, metrics/log identity, dead-event recovery, Inbox retention, and rollback constraints.
- [ ] Run placeholder/contradiction/scope scans over the implementation plan and design.
- [ ] Run fresh full verification and retain exact commands/results for the PR body.
- [ ] Request focused code review against `1597661..HEAD`, fix every Critical/Important finding, and rerun affected plus full verification.
- [ ] Inspect `git status` and the full staged diff; stage only task files, excluding the two pre-existing untracked documents.
- [ ] Commit with a terse idempotency implementation message, push the current branch, and open a draft PR to the repository default branch with design coverage and verification evidence.
