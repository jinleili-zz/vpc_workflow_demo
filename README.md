# VPC分布式任务工作流系统

基于 **go-machinery** 消息队列框架实现的分布式VPC创建工作流系统。

## 🚀 核心特性

✅ **基于消息队列的Workflow**: 使用Redis实现异步任务编排  
✅ **Chain模式**: 任务顺序执行，VRF → VLAN → Firewall  
✅ **状态查询**: 通过workflow_id查询任务执行状态  
✅ **分布式执行**: Worker可跨服务器部署  
✅ **任务持久化**: 任务和结果存储在Redis  
✅ **失败重试**: 支持任务失败后自动重试  

## 系统架构

```
┌─────────────┐
│  API服务器   │ (RESTful API，接收VPC创建请求)
└──────┬──────┘
       │
       ▼
┌─────────────┐
│   Redis     │ (消息队列和结果存储)
└──────┬──────┘
       │
       ├──────────────┬──────────────┐
       │              │              │
       ▼              ▼              ▼
┌──────────┐   ┌──────────┐   ┌──────────┐
│ 交换机    │   │ 交换机    │   │ 防火墙    │
│ Worker   │   │ Worker   │   │ Worker   │
└──────────┘   └──────────┘   └──────────┘
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

### 技术实现

- **分布式执行**: 不同Worker处理不同设备类型的任务
- **状态查询**: 通过workflow_id查询任务执行状态
- **RESTful API**: 提供HTTP接口接收VPC创建请求

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
├── tasks/                  # 任务定义
│   └── vpc_tasks.go
├── start.sh               # 启动脚本
├── stop.sh                # 停止脚本
└── test.sh                # 测试脚本
```

## 前置条件

1. **Go 1.22+**
2. **Redis** (消息队列 Broker + 结果 Backend)

### 启动Redis

使用Docker快速启动Redis:
```bash
docker run -d -p 6379:6379 redis:alpine
```

或使用系统Redis服务:
```bash
# Ubuntu/Debian
sudo systemctl start redis

# macOS
brew services start redis
```

## 快速开始

### 1. 安装依赖

```bash
go mod download
```

### 2. 启动服务

```bash
chmod +x start.sh stop.sh test.sh
./start.sh
```

这将启动:
- API服务器 (端口: 8080)
- 交换机Worker
- 防火墙Worker

### 3. 测试API

运行测试脚本:
```bash
./test.sh
```

或手动发送请求:
```bash
# 健康检查
curl http://localhost:8080/api/v1/health

# 创建VPC
curl -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-vpc-001",
    "vrf_name": "VRF-TEST-001",
    "vlan_id": 100,
    "firewall_zone": "trust-zone-001"
  }'
```

### 4. 查看日志

```bash
# API服务器日志
tail -f logs/api_server.log

# 交换机Worker日志
tail -f logs/switch_worker.log

# 防火墙Worker日志
tail -f logs/firewall_worker.log
```

### 5. 停止服务

```bash
./stop.sh
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
  "service": "vpc-workflow-api"
}
```

### 创建VPC

```
POST /api/v1/vpc
```

请求体:
```json
{
  "vpc_name": "test-vpc-001",
  "vrf_name": "VRF-TEST-001",
  "vlan_id": 100,
  "firewall_zone": "trust-zone-001"
}
```

响应:
```json
{
  "success": true,
  "message": "VPC创建工作流已启动",
  "vpc_id": "uuid",
  "workflow_id": "uuid"
}
```

### 查询VPC状态 (新增)

```
GET /api/v1/vpc/:workflow_id
```

响应:
```json
{
  "workflow_id": "abc-123",
  "task_name": "create_vrf_on_switch",
  "state": "SUCCESS",
  "status": "completed",
  "message": "工作流执行成功",
  "results": [
    {"type": "string", "value": "..."}
  ]
}
```

**状态说明**:
- `pending`: 执行中
- `success`: 成功  
- `failed`: 失败
- `completed`: 全部完成

## 工作流程 (基于消息队列的Chain模式)

1. **客户端** 发送创建VPC请求到API服务器
2. **API服务器** 创建任务链(Chain)并发送到Redis消息队列
3. **Redis** 存储任务链并按顺序分发任务:
   - **Task 1**: 交换机Worker从队列获取并创建VRF
   - **Task 2**: VRF完成后，交换机Worker创建VLAN子接口  
   - **Task 3**: VLAN完成后，防火墙Worker创建安全区域
4. **Worker** 执行完成后将结果写入Redis
5. **客户端** 可通过workflow_id查询执行状态

### 关键特点

✅ **顺序保证**: Chain确保任务严格按顺序执行  
✅ **异步处理**: API立即返回，不阻塞请求  
✅ **解耦架构**: API和Worker通过消息队列通信  
✅ **持久化**: 任务和结果存储在Redis，重启不丢失  

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

1. 在 `tasks/vpc_tasks.go` 中添加新的任务函数
2. 创建新的Worker程序 (如 `cmd/router_worker/main.go`)
3. 在任务链中添加新任务
4. 更新启动脚本

### 配置修改

修改 `config/machinery.go` 中的配置:
- Redis连接地址: `Broker`
- 队列名称: `DefaultQueue`
- 结果过期时间: `ResultsExpireIn`
- Worker并发数: `MaxWorkers`
- 重试配置: `RetryCount`, `RetryTimeout`

## 注意事项

- 本Demo使用Redis作为消息队列，生产环境可考虑RabbitMQ
- 任务执行器只打印日志，实际使用需要集成设备SDK
- 未实现任务状态持久化，可根据需要添加数据库

## 许可证

MIT
