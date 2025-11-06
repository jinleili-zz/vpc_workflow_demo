# Worker与AZ-NSP分离架构设计（go-machinery）

## 🎯 设计目标

将 AZ NSP 的**编排职责**和**执行职责**分离，实现：
- **AZ NSP**：只负责任务编排，创建任务链并发送到消息队列
- **Worker**：独立运行，从队列消费任务并执行实际的硬件配置

## 📐 架构对比

### 旧架构
```
┌─────────────┐
│   AZ NSP    │
│ (编排+执行)  │
│  同一进程    │
└─────────────┘
```

### 新架构（go-machinery）
```
┌─────────────┐     ┌─────────┐     ┌──────────────┐
│   AZ NSP    │────→│  Redis  │────→│    Worker    │
│  (仅编排)   │     │  Queue  │     │   (仅执行)    │
└─────────────┘     └─────────┘     └──────────────┘
                                     ├─ Switch Worker
                                     └─ Firewall Worker
```

## 🔑 核心组件

### 1. Top NSP (`cmd/top_nsp/main.go`)
- **职责**：Region级编排、AZ注册管理
- **功能**：协调多个AZ的VPC创建、健康检查、自动回滚

### 2. AZ NSP (`cmd/az_nsp/main.go`)
- **职责**：任务编排（不执行）
- **流程**：
  1. 接收VPC/Subnet创建请求
  2. 构造任务链（Chain）
  3. 发送到对应AZ队列
  4. 返回WorkflowID

### 3. Switch Worker (`cmd/switch_worker/main.go`)
- **职责**：执行交换机配置任务
- **任务类型**：
  - `create_vrf_on_switch` - 创建VRF
  - `create_vlan_subinterface` - 创建VLAN子接口
  - `create_subnet_on_switch` - 创建子网
  - `configure_subnet_routing` - 配置路由

### 4. Firewall Worker (`cmd/firewall_worker/main.go`)
- **职责**：执行防火墙配置任务
- **任务类型**：
  - `create_firewall_zone` - 创建安全区域

## 📊 队列设计

每个AZ使用独立队列，格式：`vpc_tasks_<region>_<az>`

示例：
- `vpc_tasks_cn-beijing_cn-beijing-1a`
- `vpc_tasks_cn-beijing_cn-beijing-1b`
- `vpc_tasks_cn-shanghai_cn-shanghai-1a`

**优势**：
- ✅ 任务隔离，避免跨AZ干扰
- ✅ 独立扩展，每个AZ可配置不同Worker数量
- ✅ 故障隔离，单AZ问题不影响其他AZ

## 🚀 快速开始

### 本地编译
```bash
# 编译所有组件
go build -o bin/top-nsp ./cmd/top_nsp
go build -o bin/az-nsp ./cmd/az_nsp
go build -o bin/switch-worker ./cmd/switch_worker
go build -o bin/firewall-worker ./cmd/firewall_worker
```

### Docker部署
```bash
# 构建镜像
cd deployments/docker
bash build-images.sh

# 启动所有服务
docker-compose up -d

# 查看日志
docker-compose logs -f az-nsp-cn-beijing-1a
docker-compose logs -f switch-worker-cn-beijing-1a
docker-compose logs -f firewall-worker-cn-beijing-1a
```

## 🔍 测试流程

### 1. 创建VPC
```bash
curl -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "vpc-test",
    "region": "cn-beijing",
    "vrf_name": "vrf-test",
    "vlan_id": 100,
    "firewall_zone": "zone-test"
  }'
```

### 2. 观察任务执行
```bash
# AZ NSP日志 - 查看任务编排
docker logs -f az-nsp-cn-beijing-1a

# Switch Worker日志 - 查看VRF和VLAN任务执行
docker logs -f switch-worker-cn-beijing-1a

# Firewall Worker日志 - 查看防火墙任务执行
docker logs -f firewall-worker-cn-beijing-1a
```

### 3. 查询VPC状态
```bash
curl http://localhost:8080/api/v1/vpc/vpc-test/status
```

## 📈 与asynq分支的对比

| 维度 | go-machinery | asynq |
|------|--------------|-------|
| **任务编排** | Chain/Group/Chord | 手动实现 |
| **重试机制** | 内置 | 内置 |
| **结果追踪** | Backend支持 | 需手动实现 |
| **复杂度** | 较高（功能丰富） | 较低（简洁） |
| **社区** | 成熟 | 新兴 |

## 🎓 学习要点

### go-machinery核心概念

1. **Task Signature**：任务定义
   ```go
   &machineryTasks.Signature{
       Name: "create_vrf_on_switch",
       RoutingKey: "vpc_tasks_cn-beijing_cn-beijing-1a",
       Args: []machineryTasks.Arg{...},
   }
   ```

2. **Chain**：顺序执行任务
   ```go
   chain, _ := machineryTasks.NewChain(task1, task2, task3)
   server.SendChain(chain)
   ```

3. **Worker注册**：
   ```go
   server.RegisterTasks(map[string]interface{}{
       "create_vrf_on_switch": tasks.CreateVRFOnSwitch,
   })
   worker := server.NewWorker("switch_worker", 2)
   worker.Launch()
   ```

## 🔧 配置说明

### 环境变量

**Top NSP**:
- `SERVICE_TYPE=top`
- `REDIS_ADDR=redis:6379`
- `REDIS_DATA_DB=0` (数据存储)
- `REDIS_BROKER_DB=1` (消息队列)

**AZ NSP**:
- `SERVICE_TYPE=az`
- `REGION=cn-beijing`
- `AZ=cn-beijing-1a`
- `TOP_NSP_ADDR=http://top-nsp:8080`

**Worker**:
- `SERVICE_TYPE=worker`
- `REGION=cn-beijing`
- `AZ=cn-beijing-1a`
- `WORKER_COUNT=2`

## 💡 最佳实践

1. **任务设计**：每个任务应该是幂等的
2. **错误处理**：任务失败应返回错误，由框架处理重试
3. **日志记录**：详细记录任务执行过程
4. **监控**：监控队列长度、Worker状态
5. **扩展**：根据负载动态调整Worker数量

---

📝 **备注**：本架构设计用于学习对比go-machinery和asynq两种任务队列框架的实现机制
