# AGENTS.md

This file provides guidance to Qoder (qoder.com) when working with code in this repository.

## Project Positioning

This repository implements an NSP (Network Service Platform) demo focused on multi-Region, multi-AZ network resource orchestration.

The current codebase is no longer the early single-server demo. The active implementation is a distributed NSP architecture with:

- `top-nsp-vpc`: Top-layer VPC orchestrator
- `top-nsp-vfw`: Top-layer firewall policy orchestrator
- `az-nsp-vpc`: AZ-layer VPC/Subnet/PCCN workflow service
- `az-nsp-vfw`: AZ-layer firewall policy workflow service
- `worker`: device-specific task worker for `switch`, `firewall`, and `loadbalancer`

## Current Architecture

### Layering

- **Top NSP**: Receives northbound API requests, manages AZ registration and heartbeat state, coordinates cross-AZ and cross-Region orchestration, and persists top-level topology.
- **AZ NSP**: Owns AZ-local workflow submission, reply consumption, compensation scanning, and AZ-local resource/task persistence.
- **Workers**: Execute device-specific tasks from Redis-backed queues and publish replies back to the AZ NSP reply queue.

### Service Split

- **VPC service**: Implemented end to end.
- **VFW service**: Implemented end to end for firewall policy orchestration.
- **PCCN service**: Implemented end to end for cross-VPC connectivity orchestration.
- **ELB service**: Handler and worker scaffolding exists, but this is not a complete top-to-bottom product path yet.
- **NAT service**: Not implemented.

### Multi-AZ Model

- A cloud can contain multiple Regions.
- A Region can contain multiple AZs.
- Each AZ runs its own AZ NSP and workers.
- Top NSP discovers AZs dynamically through registration and heartbeat.
- Queue isolation is per Region, per AZ, and per device type.

## Persistence and Messaging

### Redis

Redis is used for two purposes:

- AZ registration and heartbeat state in the Top NSP registry
- asynq task queues and reply queues

The code supports both single-node Redis and Redis Cluster. In Docker, the main compose file uses a 3-node Redis Cluster.

Relevant code:

- `internal/top/registry`
- `internal/config/redis.go`
- `internal/queue/queue.go`

### PostgreSQL

PostgreSQL is the active persistent storage layer for resource state, task state, topology, firewall policy data, and Saga state.

Key persisted data includes:

- AZ-layer resources: `vpc_resources`, `subnet_resources`, `pccn_resources`, `firewall_policies`
- AZ-layer task tracking: `tasks`
- Top-layer topology: `vpc_registry`, `subnet_registry`, `cidr_zone_mapping`, `pccn_registry`, `policy_registry`, `policy_az_records`
- Saga engine tables

Do not assume MySQL is current. The repository still contains older MySQL-era docs and scripts, but the active implementation uses PostgreSQL.

Relevant code:

- `internal/db/migrations/001_init_postgresql.sql`
- `internal/db/migrations/004_create_pccn_tables.sql`
- `internal/db/migrations/005_create_orchestration_operations.sql`
- `internal/db/migrations/006_add_tasks_resource_order_unique.sql`
- `deployments/docker/init-postgres.sh`
- `deployments/docker/saga-migration.sql`

## Idempotency

The system implements the phase 0/1 scope of `docs/architecture/idempotency-analysis-and-design.md`:

- All AZ VPC/Subnet/PCCN write responses carry a unified `code` field (`"0"` on success), which the Saga executor requires to recognize success.
- `orchestration_operations` (migration 005) is the linearization point for idempotent writes: `(owner_service, caller_scope, route_scope, idempotency_key)`. Top NSP consumes the northbound `Idempotency-Key` header; AZ NSP consumes Saga's `X-Idempotency-Key` (and Top's derived key on the Subnet path). Same key + same request replays the stored response; same key + different request returns `409 IDEMPOTENCY_KEY_REUSED`.
- `tasks (resource_id, task_order)` is unique (migration 006); task terminal updates are CAS-guarded so duplicate/concurrent replies only advance the workflow once.
- `internal/operation` holds the shared Operation service and the gin `HandleCreate` helper.
- AZ deletes follow ensure-absent semantics: deleting a missing/deleting/deleted resource succeeds.

Relevant code:

- `internal/operation`
- `internal/orchestration/workflow.go`
- `internal/db/dao/task_dao.go`
- `internal/db/dao/pccn_dao.go`

Idempotency business tests (single-instance docker PostgreSQL/Redis + one top/az/worker instance):

```bash
scripts/test-idempotency.sh
```

## Workflow Model

### Top-layer orchestration

Top-layer orchestration is HTTP-based and Saga-backed.

- Region-level VPC creation submits one Saga sync step per AZ.
- PCCN creation submits one Saga sync step per involved AZ.
- After Saga submission succeeds, Top NSP continues polling AZ-local status and updates top-level topology tables.

Relevant code:

- `internal/top/orchestrator/orchestrator.go`
- `cmd/top_nsp/main.go`

### AZ-layer orchestration

AZ NSP persists workflow/task records in PostgreSQL and submits device tasks to asynq through the broker abstraction.

- VPC workflow steps:
  1. `create_vrf_on_switch`
  2. `create_vlan_subinterface`
  3. `create_firewall_zone`
- Subnet workflow steps:
  1. `create_subnet_on_switch`
  2. `configure_subnet_routing`
- PCCN workflow steps:
  1. `create_pccn_connection`
  2. `configure_pccn_routing`
- VFW policy workflow uses AZ-local task orchestration and reply consumption in the same pattern.

Relevant code:

- `internal/az/orchestrator/orchestrator.go`
- `internal/az/vfw/orchestrator/orchestrator.go`
- `internal/orchestration`

### Worker execution

Workers are selected by `WORKER_TYPE`:

- `switch`
- `firewall`
- `loadbalancer`

Handlers currently live in:

- `tasks/handlers.go`
- `tasks/pccn_handlers.go`

This is still a demo system. Handlers mainly log and simulate device-side work rather than calling real device SDKs.

## Queue Naming

Queue naming is not the old underscore format.

Current queue patterns:

- Task queue: `tasks:{region}:{az}:{device_type}`
- Priority queues:
  - `tasks:{region}:{az}:{device_type}_critical`
  - `tasks:{region}:{az}:{device_type}_high`
  - `tasks:{region}:{az}:{device_type}`
  - `tasks:{region}:{az}:{device_type}_low`
- Reply queue: `replies:{region}:{az}:{service}`

Relevant code:

- `internal/queue/queue.go`

## Auth, Trace, and Bootstrap

The active services share common bootstrap logic for:

- logger setup
- distributed trace middleware and traced HTTP client
- optional AK/SK authentication
- Saga engine setup

Relevant code:

- `internal/bootstrap/bootstrap.go`
- `internal/config/config.go`
- `config/config.yaml`

Notes:

- Config loading is based on `config/config.yaml` plus `NSP_`-prefixed environment variable overrides.
- `top_nsp` and `az_nsp` use the hot-reload-capable config loader.
- Service-to-service signing support exists through configured credentials.
- `top-nsp-vpc` currently forces `EnableAuth = false` in code even though auth primitives are initialized.

## Main Entry Points

The active binaries are:

- `cmd/top_nsp/main.go`
- `cmd/top_nsp_vfw/main.go`
- `cmd/az_nsp/main.go`
- `cmd/az_nsp_vfw/main.go`
- `cmd/worker/main.go`

Do not assume there is a current `cmd/api_server`, `cmd/switch_worker`, or `cmd/firewall_worker` implementation. Older docs and helper scripts may still reference those paths, but they are not the current entry points.

## Build and Run

### Recommended build path

Docker is the canonical way to run the current system.

```bash
cd deployments/docker
./build-images.sh
docker-compose up -d
```

The build script builds:

- `nsp-top-vpc`
- `nsp-top-vfw`
- `nsp-az-vpc`
- `nsp-az-vfw`
- `nsp-worker`

### Direct Go builds

If you need local binaries, build the current entry points instead of the old legacy ones:

```bash
go build -o bin/top_nsp ./cmd/top_nsp
go build -o bin/top_nsp_vfw ./cmd/top_nsp_vfw
go build -o bin/az_nsp ./cmd/az_nsp
go build -o bin/az_nsp_vfw ./cmd/az_nsp_vfw
go build -o bin/worker ./cmd/worker
```

### Test entry points

- Docker end-to-end script: `deployments/docker/test-e2e.sh`
- Local shell E2E script: `scripts/test-e2e-local.sh`
- Go E2E tests: `tests/e2e`
- Go functional tests: `tests/functional`

Notes:

- `tests/e2e` expects the dedicated E2E Docker environment.
- `tests/functional` expects a reachable PostgreSQL instance with the expected schema.

## Environment Variables

Important runtime variables:

- `REGION`
- `AZ`
- `PORT`
- `WORKER_TYPE`
- `WORKER_COUNT`
- `REDIS_ADDR`
- `REDIS_BROKER_DB`
- `POSTGRES_HOST`
- `POSTGRES_PORT`
- `POSTGRES_USER`
- `POSTGRES_PASSWORD`
- `TOP_NSP_ADDR`
- `TOP_NSP_VFW_ADDR`
- `AZ_NSP_VFW_ADDR`
- `NSP_ADDR`
- `NSP_VFW_ADDR`

Also note:

- Config-file-driven settings can be overridden with `NSP_`-prefixed variables because the config loader uses the `NSP` prefix.
- The compose files currently mix direct env vars and `NSP_` env vars.

## API Surface

### Top NSP VPC

Main routes include:

- `GET /api/v1/health`
- `GET /api/v1/regions`
- `GET /api/v1/regions/:region/azs`
- `GET /api/v1/azs`
- `POST /api/v1/register/az`
- `POST /api/v1/heartbeat`
- `POST /api/v1/vpc`
- `GET /api/v1/vpcs`
- `GET /api/v1/vpc/:vpc_name/status`
- `DELETE /api/v1/vpc/:vpc_name`
- `POST /api/v1/subnet`
- `GET /api/v1/subnet/:subnet_name/status`
- `DELETE /api/v1/subnet/:subnet_name`
- `POST /api/v1/pccn`
- `GET /api/v1/pccn/:pccn_name/status`
- `GET /api/v1/pccns`
- `DELETE /api/v1/pccn/:pccn_name`
- `GET /api/v1/operations/:operation_id`

Relevant code:

- `internal/top/api/server.go`

### AZ NSP VPC

Main routes include:

- `GET /api/v1/health`
- `GET /api/v1/vpcs`
- `POST /api/v1/vpc`
- `GET /api/v1/vpc/:vpc_name/status`
- `DELETE /api/v1/vpc/:vpc_name`
- `POST /api/v1/subnet`
- `GET /api/v1/subnet/:subnet_name/status`
- `DELETE /api/v1/subnet/:subnet_name`
- `POST /api/v1/pccn`
- `GET /api/v1/pccn/:pccn_name/status`
- `GET /api/v1/pccns`
- `DELETE /api/v1/pccn/:pccn_name`
- `POST /api/v1/task/replay/:task_id`
- `GET /api/v1/task/:task_id`
- `GET /api/v1/operations/:operation_id`

Relevant code:

- `internal/az/api/server.go`

### Top NSP VFW

Main routes include:

- `GET /api/v1/health`
- `POST /api/v1/register/az`
- `POST /api/v1/heartbeat`
- `POST /api/v1/firewall/policy`
- `GET /api/v1/firewall/policy/:policy_id/status`
- `DELETE /api/v1/firewall/policy/:policy_id`
- `GET /api/v1/firewall/policies`
- `GET /api/v1/firewall/zone/:zone/policy-count`

Relevant code:

- `internal/top/vfw/api/server.go`

### AZ NSP VFW

Main routes include:

- `GET /api/v1/health`
- `GET /api/v1/firewall/policies`
- `POST /api/v1/firewall/policy`
- `GET /api/v1/firewall/policy/:policy_name/status`
- `DELETE /api/v1/firewall/policy/:policy_name`
- `GET /api/v1/firewall/policy/id/:policy_id`
- `GET /api/v1/firewall/zone/:zone/policy-count`

Relevant code:

- `internal/az/vfw/api/server.go`

## Key Packages

- `internal/top/orchestrator`: top-layer VPC and PCCN orchestration
- `internal/top/registry`: AZ registration and health tracking in Redis
- `internal/top/vpc/dao`: top-layer VPC and subnet topology persistence
- `internal/top/pccn/dao`: top-layer PCCN persistence
- `internal/top/vfw`: top-layer firewall policy API, DAO, and service
- `internal/az/orchestrator`: AZ-layer VPC, Subnet, PCCN orchestration
- `internal/az/vfw`: AZ-layer firewall policy orchestration
- `internal/orchestration`: shared workflow manager and reply-handling logic
- `internal/db/dao`: AZ-layer DAOs for resources and tasks
- `internal/client`: HTTP clients for AZ NSP communication
- `internal/config`: config loading and Redis helpers
- `internal/bootstrap`: logger/auth/trace/saga bootstrap
- `tasks`: worker handlers

## Development Guidance

### Git submission workflow

- Any code submission in this repository must follow a PR workflow rather than stopping at a local commit.
- The expected flow is: create or reuse the working branch, commit the intended changes, push the branch to the remote, and open a GitHub PR.
- Do not treat "commit only" as a completed submission unless the user explicitly asks for a local-only commit without PR.

### Adding a new worker task

1. Add or update the handler in `tasks/handlers.go` or another task file under `tasks/`.
2. Register the handler in `cmd/worker/main.go`.
3. If it participates in an AZ workflow, add the step in the relevant AZ orchestrator.
4. If Top NSP needs awareness of the new resource, update top-layer orchestration and persistence.
5. Update Docker images if you test through compose.

### Adding a new AZ

The code supports dynamic AZ registration, but deployment still requires explicit service definitions.

For the main Docker deployment:

- add an `az-nsp-vpc-*` service
- add an `az-nsp-vfw-*` service if VFW should be present in that AZ
- add worker services for the required device types

Top NSP does not need static AZ configuration as long as the new AZ NSP can register successfully.

## Important Notes

- This repository contains both current code and older transitional artifacts. Prefer code under `cmd/top_nsp`, `cmd/az_nsp`, `cmd/worker`, `internal/top`, `internal/az`, and `internal/db`.
- Files such as `start.sh` and some old docs still reference pre-refactor entry points. Treat them as legacy unless verified against current code.
- The system uses HTTP for inter-NSP calls and Redis/asynq for worker dispatch.
- Top-level orchestration relies on PostgreSQL-backed Saga state.
- AZ heartbeat interval is 60 seconds; health check logic currently treats an AZ as unhealthy after 5 minutes without heartbeat.
- The module currently declares `go 1.25.6` in `go.mod`.
