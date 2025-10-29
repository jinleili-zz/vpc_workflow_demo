# VPC分布式任务工作流系统

基于go-machinery的分布式任务框架Demo，实现VPC创建的工作流编排。

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

- **RESTful API**: 提供HTTP接口接收VPC创建请求
- **任务编排**: 将VPC创建分解为多个子任务
  - 在交换机上创建VRF
  - 创建VLAN子接口
  - 在防火墙上创建安全区域
- **分布式执行**: 不同Worker处理不同设备类型的任务
- **任务链**: 按顺序执行任务，确保依赖关系

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

1. Go 1.21+
2. Redis (消息队列)

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

### 查询VPC状态

```
GET /api/v1/vpc/:workflow_id
```

## 工作流程

1. 客户端发送创建VPC请求到API服务器
2. API服务器创建任务链并发送到Redis队列
3. 任务按顺序执行:
   - **Task 1**: 交换机Worker创建VRF
   - **Task 2**: 交换机Worker创建VLAN子接口
   - **Task 3**: 防火墙Worker创建安全区域
4. 每个任务完成后会打印日志（模拟设备配置）

## 扩展说明

### 添加新的设备类型

1. 在 `tasks/vpc_tasks.go` 中添加新的任务函数
2. 创建新的Worker程序 (如 `cmd/router_worker/main.go`)
3. 在任务链中添加新任务
4. 更新启动脚本

### 配置修改

修改 `config/machinery.go` 中的配置:
- Redis连接地址
- 队列名称
- 结果过期时间

## 注意事项

- 本Demo使用Redis作为消息队列，生产环境可考虑RabbitMQ
- 任务执行器只打印日志，实际使用需要集成设备SDK
- 未实现任务状态持久化，可根据需要添加数据库

## 许可证

MIT
