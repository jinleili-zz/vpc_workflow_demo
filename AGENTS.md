# AGENTS.md

This file provides guidance to Qoder (qoder.com) when working with code in this repository.

## Project Overview

This is a distributed VPC workflow system built with Go 1.22+ that orchestrates network infrastructure provisioning across multiple regions and availability zones using Redis-backed task queues (asynq).

**Key Architecture:**
- **Top NSP**: Global orchestrator that coordinates region-level services and manages AZ registration
- **AZ NSP**: Regional service nodes that execute workflows in specific availability zones
- **Workers**: Task processors (switch workers, firewall workers) that handle device configuration
- **Redis**: Dual-purpose backend (DB0: data storage, DB1: message queue)

## Build and Development Commands

### Building
```bash
# Build all components for legacy deployment
go build -o bin/api_server ./cmd/api_server
go build -o bin/switch_worker ./cmd/switch_worker
go build -o bin/firewall_worker ./cmd/firewall_worker

# Build Docker images (recommended)
cd deployments/docker && ./build-images.sh
```

### Running Tests
```bash
# Local end-to-end test
./scripts/test-e2e-local.sh

# Docker end-to-end test
cd deployments/docker && ./test-e2e.sh
```

### Starting Services

**Docker (recommended):**
```bash
cd deployments/docker
docker-compose up -d
```

**Local (legacy):**
```bash
# Requires Redis running on localhost:6379
./start.sh
```

### Deployment Configuration

Key environment variables for services:
- `REDIS_ADDR`: Redis address (default: redis:6379 in Docker, localhost:6379 local)
- `REDIS_DATA_DB`: Redis database for data storage (default: 0)
- `REDIS_BROKER_DB`: Redis database for task queue (default: 1)
- `REGION`: Region identifier (e.g., cn-beijing, cn-shanghai)
- `AZ`: Availability zone identifier (e.g., cn-beijing-1a)
- `TOP_NSP_ADDR`: Top NSP address for AZ registration
- `WORKER_COUNT`: Number of concurrent workers (default: 2)

## Architecture

### Service Hierarchy
```
Top NSP (Global)
  ├── cn-beijing Region
  │   ├── cn-beijing-1a (AZ NSP + Workers)
  │   └── cn-beijing-1b (AZ NSP + Workers)
  └── cn-shanghai Region
      └── cn-shanghai-1a (AZ NSP + Workers)
```

### Service Levels
- **Region-level services** (VPC): Top NSP orchestrates parallel creation across all AZs in a region
- **AZ-level services** (Subnet): Top NSP routes requests to specific AZ NSP

### Workflow Execution
VPC creation uses Chain mode with sequential task execution:
1. `create_vrf_on_switch` - Create VRF on switch
2. `create_vlan_subinterface` - Create VLAN subinterface  
3. `create_firewall_zone` - Create firewall security zone

Each task enqueues the next task upon completion. Tasks are processed by dedicated workers monitoring AZ-specific queues (format: `vpc_tasks_{region}_{az}`).

### Key Packages
- `internal/top/orchestrator`: Region/AZ orchestration logic, parallel task distribution, automatic rollback on partial failures
- `internal/top/registry`: AZ registration, health monitoring (60s heartbeat)
- `internal/az/api`: AZ NSP API server with auto-registration to Top NSP
- `internal/client`: HTTP client for inter-NSP communication
- `internal/config`: Configuration and Redis client management
- `tasks/vpc_tasks.go`: VPC workflow task definitions (asynq handlers)
- `tasks/subnet_tasks.go`: Subnet workflow task definitions

### Critical Implementation Details
- **Queue isolation**: Each AZ uses dedicated queues to prevent cross-AZ task competition
- **State management**: Workflow states stored in Redis with format `workflow:{id}:state`
- **VPC lookup**: VPC status queryable by VPC name stored in Redis with key `vpc:{name}`
- **Rollback mechanism**: If any AZ fails during Region-level VPC creation, all successful AZs are automatically rolled back (see `internal/top/orchestrator/orchestrator.go:162`)
- **Dynamic AZ registration**: AZ NSPs auto-register on startup and send heartbeats every 60 seconds

### Main Entry Points
- `cmd/api_server/main.go`: Legacy single-server mode (not used in NSP architecture)
- Top NSP and AZ NSP share binaries - behavior controlled by `SERVICE_TYPE` environment variable
- Workers are separate binaries that connect to Redis queue for their AZ

## API Reference

### Top NSP API (Port 8080)

**Health Check:**
```bash
GET /api/v1/health
```

**List Regions:**
```bash
GET /api/v1/regions
```

**List AZs in Region:**
```bash
GET /api/v1/regions/:region/azs
```

**Create Region-level VPC** (parallel across all AZs):
```bash
POST /api/v1/vpc
{
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "vrf_name": "VRF-001",
  "vlan_id": 100,
  "firewall_zone": "trust-zone"
}
```

**Create AZ-level Subnet** (single AZ):
```bash
POST /api/v1/subnet
{
  "subnet_name": "test-subnet-001",
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "az": "cn-beijing-1a",
  "cidr": "10.0.1.0/24"
}
```

### AZ NSP API (Port varies by deployment)

**Query VPC Status:**
```bash
GET /api/v1/vpc/:vpc_name/status
```

**Query Subnet Status:**
```bash
GET /api/v1/subnet/:subnet_name/status
```

## Adding New Features

### Adding New Task Types
1. Define task handler in `tasks/vpc_tasks.go` or create new task file
2. Register handler in AZ NSP worker initialization
3. Update workflow chain in AZ NSP API to include new task
4. Rebuild Docker images

### Adding New Regions/AZs
Modify `deployments/docker/docker-compose.yml`:
- Add new AZ NSP service with appropriate `REGION` and `AZ` environment variables
- Add corresponding switch and firewall workers for the new AZ
- No Top NSP changes needed - AZs auto-register on startup

## Important Notes

- This system is a demonstration - task handlers only log actions without actual device SDKs
- All inter-NSP communication uses HTTP; worker-to-queue uses Redis/asynq
- Redis must be running before starting any services
- Docker deployment manages service dependencies automatically via healthchecks
