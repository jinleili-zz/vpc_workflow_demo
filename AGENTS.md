# AGENTS.md

This file provides guidance to Qoder (qoder.com) when working with code in this repository.

## NSP System Overview

### 项目定位

NSP（Network Service Platform，网络服务平台）部署在私有云的云管区，作为网络基础设施编排的核心组件，对外提供RESTful API接口，供云管平台门户后端调用，实现对底层硬件网络设备（交换机、防火墙、负载均衡器等）的自动化编排与配置下发。

### 多AZ架构

NSP采用多可用区（Multi-AZ）架构设计，支持跨Region和AZ的资源编排：

- **一朵云**：可包含多个Region（如 cn-beijing、cn-shanghai）
- **一个Region**：最多支持3个可用区（AZ），命名格式为 `{region}-1a`、`{region}-1b`、`{region}-1c`
- **资源隔离**：每个AZ部署独立的AZ-NSP和Worker，队列隔离防止跨AZ任务竞争

### 设备纳管架构

NSP纳管的网络设备按AZ维度划分，每个AZ拥有一套完整的网络设备：

| 设备类型 | 品牌型号 | 数量/AZ | 说明 |
|---------|---------|--------|------|
| 数据中心交换机 | 华为 DC8800系列 | 1套 | VRF、VLAN、子网等配置 |
| 负载均衡器 | F5 | 1套 | LB Pool、Listener、健康检查等配置 |
| 防火墙 | 山石网科 | 1套 | 安全区域、安全策略等配置 |

**Worker与设备的对应关系：**

| Worker类型 | 纳管设备 | 职责 |
|-----------|---------|------|
| DC-Worker | 华为交换机 | 交换机配置下发（VRF、VLAN、路由等） |
| LB-Worker | F5负载均衡 | 负载均衡配置（Pool、Listener、VS等） |
| FW-Worker | 山石防火墙 | 防火墙配置（安全区域、策略等） |

**关键约束：**
- Worker按AZ维度独立部署，不会跨AZ纳管设备
- 每个AZ的Worker只与本AZ内的设备通信
- 队列隔离确保任务不会跨AZ执行

### TOP-NSP与AZ-NSP对应关系

TOP-NSP与AZ-NSP按服务类型一一对应，形成完整的服务链路：

```
┌─────────────────────────────────────────────────────────────────┐
│                        TOP-NSP层                                 │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐             │
│  │ TOP-NSP-VPC │  │ TOP-NSP-ELB │  │ TOP-NSP-VFW │             │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘             │
└─────────┼────────────────┼────────────────┼────────────────────┘
          │                │                │
          ▼                ▼                ▼
┌─────────────────────────────────────────────────────────────────┐
│                      AZ-NSP层 (每个AZ)                           │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐             │
│  │ AZ-NSP-VPC  │  │ AZ-NSP-ELB  │  │ AZ-NSP-VFW  │             │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘             │
└─────────┼────────────────┼────────────────┼────────────────────┘
          │                │                │
          ▼                ▼                ▼
┌─────────────────────────────────────────────────────────────────┐
│                       Worker层 (每个AZ)                          │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐             │
│  │  DC-Worker  │  │  LB-Worker  │  │  FW-Worker  │             │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘             │
└─────────┼────────────────┼────────────────┼────────────────────┘
          │                │                │
          ▼                ▼                ▼
┌─────────────────────────────────────────────────────────────────┐
│                     设备层 (每个AZ)                               │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐             │
│  │ 华为DC8800  │  │     F5      │  │   山石FW    │             │
│  └─────────────┘  └─────────────┘  └─────────────┘             │
└─────────────────────────────────────────────────────────────────┘
```

**服务对应说明：**
- **VPC服务**：TOP-NSP-VPC → AZ-NSP-VPC → DC-Worker + FW-Worker
- **ELB服务**：TOP-NSP-ELB → AZ-NSP-ELB → LB-Worker
- **VFW服务**：TOP-NSP-VFW → AZ-NSP-VFW → FW-Worker

```
┌──────────────────────────────────────────────────────────────────┐
│                           一朵云                                  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │                      TOP-NSP                                │  │
│  │            (top-nsp-vpc / top-nsp-vfw)                      │  │
│  │                   HTTP REST API                             │  │
│  └─────────────────────────┬──────────────────────────────────┘  │
│                            │ HTTP                                 │
│         ┌──────────────────┼──────────────────┐                  │
│         ▼                  ▼                  ▼                  │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐          │
│  │  Region     │    │  Region     │    │  Region     │          │
│  │ cn-beijing  │    │ cn-shanghai │    │ cn-guangzhou│          │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘          │
│         │                  │                  │                  │
│    ┌────┴────┐        ┌────┴────┐        ┌────┴────┐            │
│    ▼    ▼    ▼        ▼         ▼        ▼         ▼            │
│  ┌───┐┌───┐┌───┐    ┌───┐     ┌───┐    ┌───┐     ┌───┐          │
│  │1a││1b││1c│      │1a│     │1b│      │1a│     │1b│            │
│  └─┬─┘└─┬─┘└─┬─┘    └─┬─┘     └─┬─┘    └─┬─┘     └─┬─┘          │
│    │    │    │        │         │        │         │            │
│  Workers  Workers  Workers  Workers  Workers  Workers           │
└──────────────────────────────────────────────────────────────────┘
```

### 层次架构

NSP采用三层分布式架构设计：

- **TOP-NSP（云级编排层）**：作为全局编排器，负责跨Region的资源协调、AZ注册管理、健康监控，以及Region级资源（如VPC）的并行分发与自动回滚。TOP-NSP通过HTTP接口与AZ-NSP通信。

- **AZ-NSP（可用区服务层）**：部署在每个可用区，负责本AZ内的资源管理与工作流编排。AZ-NSP启动时自动向TOP-NSP注册，并以60秒间隔发送心跳维持在线状态。AZ-NSP通过基于Redis的asynq消息队列向Worker下发任务。

- **Worker（任务执行层）**：按设备类型分为Switch Worker（交换机配置）、Firewall Worker（防火墙配置）、LoadBalancer Worker（负载均衡配置）。Worker监听AZ专属队列（格式：`tasks_{region}_{az}_{device_type}`），执行具体的设备配置任务，并通过回调队列返回执行结果。

```
┌─────────────────────────────────────────────────────────────┐
│                        TOP-NSP                              │
│              (top-nsp-vpc / top-nsp-vfw)                    │
│                    HTTP REST API                            │
└─────────────────────┬───────────────────────────────────────┘
                      │ HTTP
        ┌─────────────┼─────────────┐
        ▼             ▼             ▼
   ┌─────────┐   ┌─────────┐   ┌─────────┐
   │ AZ-NSP  │   │ AZ-NSP  │   │ AZ-NSP  │
   │ beijing │   │ beijing │   │ shanghai│
   │   -1a   │   │   -1b   │   │   -1a   │
   └────┬────┘   └────┴────┘   └────┬────┘
        │ Redis/asynq              │
   ┌────┴────┐   ┌─────────┐   ┌────┴────┐
   │ Workers │   │ Workers │   │ Workers │
   │switch/fw│   │switch/fw│   │switch/fw│
   └─────────┘   └─────────┘   └─────────┘
```

### 微服务划分

NSP对外呈现为以下微服务：

| 服务 | 功能范围 | 实现状态 |
|------|---------|---------|
| **VPC服务** | VPC创建/删除（跨AZ并行）、子网管理、VRF/VLAN配置 | 已实现 |
| **VFW服务** | 防火墙安全区域管理、安全策略配置 | 已实现 |
| **ELB服务** | 负载均衡池管理、监听器配置 | 框架已就绪 |
| **NAT服务** | NAT网关、EIP等互联网访问功能 | 规划中 |

### 部署架构

NSP基于容器化部署，核心组件包括：

- **Redis Cluster**（3节点）：双数据库设计，DB0用于数据存储，DB1用于消息队列
- **MySQL**：持久化存储资源拓扑与状态数据
- **服务容器**：TOP-NSP、AZ-NSP按服务类型独立部署，Worker按设备类型和AZ维度部署

生产环境计划使用Kubernetes编排工具实现多副本高可用部署（每个服务3副本）。当前测试环境受限于资源，docker-compose配置为单实例部署。

---

## Project Overview (Technical)

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
