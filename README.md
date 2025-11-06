# VPC分布式任务工作流系统

基于 **asynq** 消息队列框架实现的分布式VPC创建工作流系统。

## 🚀 核心特性

✅ **基于消息队列的Workflow**: 使用Redis实现异步任务编排  
✅ **Chain模式**: 任务顺序执行，VRF → VLAN → Firewall  
✅ **状态查询**: 通过workflow_id查询任务执行状态  
✅ **分布式执行**: Worker可跨服务器部署  
✅ **任务持久化**: 任务和结果存储在Redis  
✅ **失败重试**: 支持任务失败后自动重试  
✅ **NSP架构**: 支持多Region/AZ部署，Top NSP协调，AZ NSP执行

## 系统架构

```
┌─────────────────────────────────────────────────────────────┐
│                         Top NSP                             │
│                   (全局协调器/控制器)                        │
└─────────────┬─────────────────────────┬─────────────────────┘
              │                         │
        HTTP API调用              HTTP API调用
              │                         │
              ▼                         ▼
┌──────────────────────┐    ┌──────────────────────┐
│     北京 Region       │    │     上海 Region       │
│  AZ-1a    │   AZ-1b   │    │  AZ-1a (独立部署)     │
│           │           │    │                       │
│  ┌────────┴──┐     ┌──┴───┐│  ┌─────────────────┐  │
│  │ AZ NSP    │     │ AZ NSP││  │   AZ NSP        │  │
│  │ (Worker)  │     │(Worker)││  │  (Worker)       │  │
│  └───────────┘     └───────┘│  └─────────────────┘  │
└─────────────────────────────┘  └─────────────────────┘
              │                         │
              ▼                         ▼
       ┌─────────────┐          ┌─────────────┐
       │   Redis     │          │   Redis     │
       │ (消息队列)   │          │ (消息队列)   │
       └─────────────┘          └─────────────┘
```

## 功能特性

### Workflow编排

- 🔗 **Chain模式** (当前使用): 任务顺序执行，`VRF → VLAN → Firewall`
- 🔀 **Group模式** (示例代码): 所有任务并行执行
- 🎶 **Chord模式** (示例代码): `(VRF || VLAN) → Firewall`

### 任务类型

1. **在交换机上创建VRF** - Virtual Routing and Forwarding
2. **创建VLAN子接口** - 虚拟局域网配置
3. **在防火墙上创建安全区域** - 安全策略配置
4. **创建子网** - 在指定AZ创建子网
5. **配置子网路由** - 配置子网路由信息

### 技术实现

- **分布式执行**: 不同Worker处理不同设备类型的任务
- **状态查询**: 通过workflow_id查询任务执行状态
- **RESTful API**: 提供HTTP接口接收VPC创建请求
- **NSP架构**: Top NSP负责协调，AZ NSP负责执行
- **队列隔离**: 每个AZ使用独立队列，避免任务竞争

## 目录结构

```
workflow_qoder/
├── api/                    # API服务
│   └── server.go
├── cmd/                    # 可执行程序
│   ├── api_server/         # API服务器
│   ├── switch_worker/      # 交换机Worker
│   └── firewall_worker/    # 防火墙Worker
├── config/                 # 配置
│   └── machinery.go
├── deployments/            # 部署配置
│   └── docker/             # Docker部署
│       ├── docker-compose.yml
│       ├── Dockerfile.top
│       ├── Dockerfile.az
│       └── build-images.sh
├── internal/               # 内部模块
│   ├── top/                # Top NSP (全局协调器)
│   │   ├── api/            # Top NSP API
│   │   ├── orchestrator/   # 工作流编排
│   │   └── registry/       # AZ注册与管理
│   ├── az/                 # AZ NSP (区域服务)
│   │   └── api/            # AZ NSP API
│   ├── client/             # 客户端
│   ├── config/             # 配置
│   └── models/             # 数据模型
├── tasks/                  # 任务定义
│   ├── vpc_tasks.go
│   └── subnet_tasks.go
├── scripts/                # 脚本
│   ├── start-nsp-local.sh
│   └── test-e2e-local.sh
├── start.sh               # 启动脚本
├── stop.sh                # 停止脚本
└── test.sh                # 测试脚本
```

## 前置条件

1. **Go 1.22+**
2. **Docker & Docker Compose** (推荐使用容器化部署)
3. **Redis** (消息队列 Broker + 结果 Backend)

## 快速开始 (容器化部署 - 推荐)

### 1. 构建Docker镜像

```bash
cd deployments/docker
./build-images.sh
```

### 2. 启动服务

```bash
docker-compose up -d
```

这将启动:
- Top NSP (全局协调器)
- 3个AZ NSP (北京-1a, 北京-1b, 上海-1a)
- Redis (消息队列和数据存储)

### 3. 测试API

运行端到端测试:
```bash
./test-e2e.sh
```

或手动发送请求:
```bash
# 健康检查
curl http://localhost:8080/api/v1/health

# 查看Region列表
curl http://localhost:8080/api/v1/regions

# 查看北京Region的AZ列表
curl http://localhost:8080/api/v1/regions/cn-beijing/azs

# 创建Region级VPC (在所有北京AZ并行创建)
curl -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-vpc-001",
    "region": "cn-beijing",
    "vrf_name": "VRF-TEST-001",
    "vlan_id": 100,
    "firewall_zone": "trust-zone-001"
  }'

# 创建AZ级子网 (在指定AZ创建)
curl -X POST http://localhost:8080/api/v1/subnet \
  -H "Content-Type: application/json" \
  -d '{
    "subnet_name": "test-subnet-001",
    "vpc_name": "test-vpc-001",
    "region": "cn-beijing",
    "az": "cn-beijing-1a",
    "cidr": "10.0.1.0/24"
  }'
```

### 4. 查看日志

```bash
# 查看所有容器日志
docker-compose logs -f

# 查看特定容器日志
docker logs -f top-nsp
docker logs -f az-nsp-cn-beijing-1a
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
  "status": "ok",
  "service": "top-nsp"
}
```

### 查看Region列表

```
GET /api/v1/regions
```

响应:
```json
{
  "success": true,
  "regions": ["cn-beijing", "cn-shanghai"]
}
```

### 查看Region下的AZ列表

```
GET /api/v1/regions/:region/azs
```

响应:
```json
{
  "success": true,
  "azs": [
    {
      "id": "cn-beijing-1a",
      "region": "cn-beijing",
      "name": "cn-beijing-1a",
      "nsp_addr": "http://az-nsp-cn-beijing-1a:8080",
      "status": "online"
    }
  ]
}
```

### 创建Region级VPC

```
POST /api/v1/vpc
```

请求体:
```json
{
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "vrf_name": "VRF-TEST-001",
  "vlan_id": 100,
  "firewall_zone": "trust-zone-001"
}
```

响应:
```json
{
  "success": true,
  "message": "VPC已在2个AZ中成功创建",
  "vpc_id": "uuid",
  "az_results": {
    "cn-beijing-1a": "workflow_id_1",
    "cn-beijing-1b": "workflow_id_2"
  }
}
```

### 创建AZ级子网

```
POST /api/v1/subnet
```

请求体:
```json
{
  "subnet_name": "test-subnet-001",
  "vpc_name": "test-vpc-001",
  "region": "cn-beijing",
  "az": "cn-beijing-1a",
  "cidr": "10.0.1.0/24"
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

### 查询VPC状态

```
GET /api/v1/vpc/:vpc_name/status
```

响应:
```json
{
  "az": "cn-beijing-1a",
  "vpc_name": "test-vpc-001",
  "workflow_id": "workflow_id",
  "state": "COMPLETED",
  "status": "completed",
  "message": "工作流执行成功"
}
```

### 查询子网状态

```
GET /api/v1/subnet/:subnet_name/status
```

响应:
```json
{
  "az": "cn-beijing-1a",
  "subnet_name": "test-subnet-001",
  "workflow_id": "workflow_id",
  "state": "COMPLETED",
  "status": "completed",
  "message": "工作流执行成功"
}
```

## 工作流程 (基于消息队列的Chain模式)

1. **客户端** 发送创建VPC请求到Top NSP
2. **Top NSP** 协调Region下的所有AZ NSP并行创建VPC
3. **AZ NSP** 创建任务链(Chain)并发送到Redis消息队列
4. **Redis** 存储任务链并按顺序分发任务:
   - **Task 1**: 交换机Worker从队列获取并创建VRF
   - **Task 2**: VRF完成后，交换机Worker创建VLAN子接口  
   - **Task 3**: VLAN完成后，防火墙Worker创建安全区域
5. **Worker** 执行完成后将结果写入Redis
6. **客户端** 可通过VPC名称查询执行状态

### 关键特点

✅ **顺序保证**: Chain确保任务严格按顺序执行  
✅ **异步处理**: API立即返回，不阻塞请求  
✅ **解耦架构**: Top NSP和AZ NSP通过HTTP通信，AZ NSP和Worker通过消息队列通信  
✅ **持久化**: 任务和结果存储在Redis，重启不丢失  
✅ **队列隔离**: 每个AZ使用独立队列 `vpc_tasks_{region}_{az}`，避免跨AZ任务竞争  
✅ **NSP架构**: 支持动态添加Region和AZ，自动注册与心跳

## 扩展说明

### Workflow模式扩展

查看 `examples/workflow_patterns.go` 了解其他编排模式:

1. **Chain模式** (当前使用): `VRF → VLAN → Firewall`
2. **Group模式**: `VRF || VLAN || Firewall` (并行)
3. **Chord模式**: `(VRF || VLAN) → Firewall`
4. **带重试的任务**: 失败后自动重试
5. **延迟任务**: 定时执行
6. **复杂编排**: 自定义执行顺序

### 添加新的设备类型

1. 在 `tasks/vpc_tasks.go` 或 `tasks/subnet_tasks.go` 中添加新的任务函数
2. 在 `cmd/az_nsp/main.go` 中注册新的任务处理器
3. 在任务链中添加新任务
4. 更新Docker镜像

### 配置修改

修改 `config/machinery.go` 中的配置:
- Redis连接地址: `GetRedisAddr()`
- Redis DB编号: `GetRedisBrokerDB()`
- Worker并发数: 通过环境变量 `WORKER_COUNT` 配置

## 注意事项

- 本系统使用Redis作为消息队列，生产环境可考虑更稳定的部署方案
- 任务执行器只打印日志，实际使用需要集成设备SDK
- 系统支持动态扩展Region和AZ，通过环境变量配置
- 每个AZ使用独立队列，确保任务不会跨AZ竞争执行

## 许可证

MIT