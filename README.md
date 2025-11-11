# VPC分布式任务工作流系统

基于 **RocketMQ** 消息队列实现的分布式、多层级VPC创建工作流系统。

## 🚀 核心特性

✅ **基于RocketMQ的Workflow**: 使用Apache RocketMQ实现高性能异步任务编排  
✅ **多层级架构**: Top NSP统一入口 + AZ NSP区域执行  
✅ **Chain任务链**: 任务顺序执行，VRF → VLAN → Firewall  
✅ **自动服务发现**: AZ NSP自动注册到Top NSP  
✅ **分布式执行**: Worker可跨服务器、跨AZ部署  
✅ **任务状态管理**: Redis存储任务状态，支持查询  
✅ **失败重试**: RocketMQ自动重试机制  
✅ **容器化部署**: Docker Compose一键启动全部服务  

## 系统架构

```
┌──────────────────────────────────────────────────────┐
│                     用户请求                          │
└────────────────────────┬─────────────────────────────┘
                         │
                         ▼
              ┌──────────────────┐
              │    Top NSP       │  (统一入口 :8080)
              │  - 服务注册中心   │  
              │  - 任务编排调度   │
              └────────┬─────────┘
                       │
       ┌───────────────┼───────────────┐
       │               │               │
       ▼               ▼               ▼
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│  AZ NSP     │ │  AZ NSP     │ │  AZ NSP     │
│ (bj-1a)     │ │ (bj-1b)     │ │ (sh-1a)     │
└──────┬──────┘ └──────┬──────┘ └──────┬──────┘
       │               │               │
       ▼               ▼               ▼
┌──────────────────────────────────────────┐
│           RocketMQ Cluster               │
│  ┌────────────┐      ┌────────────┐     │
│  │ NameServer │      │   Broker   │     │
│  └────────────┘      └────────────┘     │
└────────────┬─────────────────────────────┘
             │
     ┌───────┴────────┐
     │                │
     ▼                ▼
┌──────────┐    ┌──────────┐
│ Switch   │    │ Firewall │
│ Worker   │    │ Worker   │
└──────────┘    └──────────┘

┌─────────────────────────────────┐
│          Redis                  │
│  - AZ注册信息                    │
│  - 任务状态                      │
│  - 任务链信息                    │
└─────────────────────────────────┘
```

## 功能特性

### 多层级架构

- 🌐 **Top NSP** - 统一入口，服务注册中心，跨AZ任务编排
- 📍 **AZ NSP** - 区域级服务，处理本AZ的VPC/子网创建
- 🔄 **自动注册** - AZ NSP启动时自动向Top NSP注册
- 💓 **心跳机制** - 定期心跳保持服务在线状态

### Workflow编排

- 🔗 **Chain任务链**: 任务顺序执行，`VRF → VLAN → Firewall`
- 🔀 **Group任务组**: 支持并行任务执行
- 🎯 **自动触发**: Worker完成任务后自动触发链中下一个任务

### 任务类型

**VPC创建流程** (3步链式任务):
1. **创建VRF** - Virtual Routing and Forwarding (Switch Worker)
2. **创建VLAN子接口** - 虚拟局域网配置 (Switch Worker)
3. **创建防火墙安全区域** - 安全策略配置 (Firewall Worker)

**子网创建流程** (2步链式任务):
1. **创建子网** - 子网配置 (Switch Worker)
2. **配置路由** - 路由规则 (Switch Worker)

### 技术实现

- **RocketMQ**: 高性能消息队列，支持Topic自动创建
- **分布式执行**: Worker按Topic订阅，多实例负载均衡
- **状态管理**: Redis存储任务状态和链信息
- **RESTful API**: 统一的HTTP接口
- **Docker部署**: 容器化部署，服务编排

## 目录结构

```
workflow_qoder/
├── cmd/                       # 可执行程序
│   ├── top_nsp/              # Top NSP服务器
│   ├── az_nsp/               # AZ NSP服务器
│   ├── switch_worker/        # 交换机Worker
│   └── firewall_worker/      # 防火墙Worker
├── internal/                 # 内部包
│   ├── top/                  # Top NSP逻辑
│   │   ├── api/              # API服务
│   │   ├── registry/         # AZ注册中心
│   │   └── orchestrator/     # 任务编排器
│   ├── az/                   # AZ NSP逻辑
│   │   └── api/              # AZ API服务
│   ├── rocketmq/             # RocketMQ客户端封装
│   ├── config/               # 配置管理
│   ├── models/               # 数据模型
│   └── client/               # 客户端工具
├── tasks/                    # 任务定义
│   ├── vpc_tasks.go          # VPC任务
│   └── subnet_tasks.go       # 子网任务
├── deployments/docker/       # Docker部署
│   ├── docker-compose.yml    # 服务编排
│   ├── Dockerfile.*          # 各服务镜像
│   ├── build-rocketmq.sh     # 构建脚本
│   └── rocketmq/             # RocketMQ配置
└── examples/                 # 示例代码
```

## 前置条件

1. **Docker & Docker Compose** - 容器化部署
2. **Go 1.22+** - 本地开发环境（可选）

## 快速开始

### 1. 启动所有服务

```bash
cd deployments/docker
docker-compose up -d
```

这将启动:
- **RocketMQ NameServer** (端口: 9876)
- **RocketMQ Broker** (端口: 10911)
- **Redis** (端口: 6379)
- **Top NSP** (端口: 8080) - 统一入口
- **AZ NSP - cn-beijing-1a** (内部服务)
- **Switch Worker** - 交换机任务处理
- **Firewall Worker** - 防火墙任务处理

### 2. 查看服务状态

```bash
docker-compose ps
```

### 3. 测试API

**健康检查**:
```bash
curl http://localhost:8080/api/v1/health
```

**创建VPC** (通过Top NSP入口):
```bash
curl -X POST http://localhost:8080/api/v1/vpc \
  -H 'Content-Type: application/json' \
  -d '{
    "vpc_name": "test-vpc-001",
    "region": "cn-beijing",
    "az": "cn-beijing-1a",
    "vrf_name": "vrf-test-001",
    "vlan_id": 100,
    "firewall_zone": "dmz"
  }'
```

**创建子网**:
```bash
curl -X POST http://localhost:8080/api/v1/subnet \
  -H 'Content-Type: application/json' \
  -d '{
    "subnet_name": "subnet-001",
    "vpc_name": "test-vpc-001",
    "region": "cn-beijing",
    "az": "cn-beijing-1a",
    "cidr": "192.168.1.0/24"
  }'
```

### 4. 查看日志

```bash
# Top NSP日志
docker logs -f top-nsp

# AZ NSP日志
docker logs -f az-nsp-cn-beijing-1a

# Switch Worker日志
docker logs -f switch-worker-cn-beijing-1a

# Firewall Worker日志
docker logs -f firewall-worker-cn-beijing-1a

# RocketMQ Broker日志
docker logs -f rmqbroker
```

### 5. 停止服务

```bash
docker-compose down
```

## API接口

### 健康检查

```
GET /api/v1/health
```

响应:
```json
{
  "status": "ok"
}
```

### 创建VPC (Region级)

```
POST /api/v1/vpc
```

请求体:
```json
{
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "az": "cn-beijing-1a",
  "vrf_name": "vrf-test-001",
  "vlan_id": 100,
  "firewall_zone": "dmz"
}
```

响应:
```json
{
  "success": true,
  "message": "VPC已在1个AZ中成功创建",
  "vpc_id": "uuid",
  "az_results": {
    "cn-beijing-1a": "workflow_id"
  }
}
```

### 创建子网 (AZ级)

```
POST /api/v1/subnet
```

请求体:
```json
{
  "subnet_name": "subnet-001",
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "az": "cn-beijing-1a",
  "cidr": "192.168.1.0/24"
}
```

响应:
```json
{
  "success": true,
  "message": "子网创建工作流已启动",
  "subnet_id": "uuid",
  "workflow_id": "uuid"
}
```

### 查询Region列表

```
GET /api/v1/regions
```

### 查询AZ列表

```
GET /api/v1/regions/:region/azs
```

## 工作流程

### 完整架构流程

1. **用户请求** → Top NSP (localhost:8080)
2. **Top NSP** 查找目标Region/AZ，将请求转发给 AZ NSP
3. **AZ NSP** 创建任务链并发送到 RocketMQ
4. **RocketMQ** 按Topic分发任务给对应的Workers:
   - **Task 1**: Switch Worker 创建VRF
   - **Task 2**: Switch Worker 创建VLAN子接口 (VRF完成后自动触发)
   - **Task 3**: Firewall Worker 创建安全区域 (VLAN完成后自动触发)
5. **Worker** 执行完成后将状态写入Redis
6. **链式触发**: Worker完成任务后从 Redis 获取链信息，自动发送下一个任务

### VPC创建示例

```bash
# 1. 用户发起请求
curl -X POST http://localhost:8080/api/v1/vpc ...

# 2. Top NSP 转发给 AZ NSP (cn-beijing-1a)
# 3. AZ NSP 发送任务链到 RocketMQ Topic: vpc_tasks_cn-beijing_cn-beijing-1a

# 4. Worker执行流程:
[Switch Worker] 收到Task1 → 创建VRF → 完成 → 触发Task2
[Switch Worker] 收到Task2 → 创建VLAN → 完成 → 触发Task3
[Firewall Worker] 收到Task3 → 创建防火墙区域 → 完成

# 5. 工作流完成
```

### 关键特点

✅ **顺序保证**: 任务链确保任务严格按顺序执行  
✅ **异步处理**: API立即返回，不阻塞请求  
✅ **解耦架构**: 服务间通过RocketMQ通信  
✅ **高可用**: RocketMQ支持集群部署，消息持久化  
✅ **水平扩展**: Worker可多实例部署，自动负载均衡  
✅ **多区域**: 支持多个Region/AZ，AZ自动注册  

## 扩展说明

### 添加新的AZ

1. 在 `docker-compose.yml` 中添加新的AZ NSP和Worker服务
2. 设置环境变量 `REGION` 和 `AZ`
3. AZ NSP启动时会自动注册到Top NSP
4. 无需修改代码，支持动态扩展

### 添加新的任务类型

1. 在 `tasks/vpc_tasks.go` 或 `tasks/subnet_tasks.go` 中添加新任务函数
2. 在Worker的 `main.go` 中注册新任务处理器
3. 在AZ NSP的API中添加到任务链
4. 重新构建镜像并部署

### 配置修改

修改 `internal/config/config.go` 中的配置:
- **RocketMQ地址**: `ROCKETMQ_NAME_SERVERS`
- **Redis地址**: `REDIS_ADDR`
- **Topic名称**: `ROCKETMQ_VPC_TOPIC`, `ROCKETMQ_SUBNET_TOPIC`
- **重试次数**: `ROCKETMQ_RETRY_TIMES`

### 性能优化

1. **Worker扩容**: 增加Worker实例数，RocketMQ自动负载均衡
2. **RocketMQ调优**: 调整Broker配置，增加队列数量
3. **Redis优化**: 使用Redis集群提升可用性

## 注意事项

- 本项目使用RocketMQ 5.1.4，生产环境可配置集群模式
- 任务执行器只打印日志，实际使用需要集成设备SDK
- RocketMQ Go SDK v2.0.0已解决hostname解析问题
- 支持多区域、多可用区部署，动态扩展

## 分支说明

- **rocketmq** - 基于RocketMQ的实现（当前分支）
- **go-machinery** - 基于go-machinery的实现
- **mysql** - 基于MySQL的实现
- **main** - 主分支

## 许可证

MIT
