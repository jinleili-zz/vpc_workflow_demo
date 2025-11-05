# VPC分布式任务工作流系统 - 基于消息队列的Workflow

## 系统概述

本系统是一个基于 **go-machinery** 消息队列框架实现的分布式VPC创建工作流系统。
核心特性是通过 **Redis消息队列** 实现任务的顺序编排和分布式执行。

### 当前运行状态
✅ API服务器正在运行 (端口: 8080)
✅ 交换机Worker正在运行 (监听消息队列)
✅ 防火墙Worker正在运行 (监听消息队列)
✅ Redis消息队列运行中

---

## 快速演示

### 1. 创建VPC工作流

运行测试脚本:
```bash
./test.sh
```

或手动发送请求:
```bash
curl -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "my-vpc",
    "vrf_name": "VRF-MY-VPC",
    "vlan_id": 100,
    "firewall_zone": "my-zone"
  }'
```

### 2. 查看任务执行日志

**交换机Worker日志:**
```bash
tail -f logs/switch_worker.log | grep "交换机任务"
```

**防火墙Worker日志:**
```bash
tail -f logs/firewall_worker.log | grep "防火墙任务"
```

### 3. 查看所有日志
```bash
# API服务器
tail -f logs/api_server.log

# 交换机Worker
tail -f logs/switch_worker.log

# 防火墙Worker
tail -f logs/firewall_worker.log
```

---

## 工作流程演示 (Chain模式)

当你发送一个创建VPC的请求后，系统会基于**消息队列**自动执行：

### 执行流程

```
1. [API] 接收请求，构建任务链
           ↓
2. [API] 将Chain发送到Redis消息队列
           ↓
3. [队列] Redis存储任务链信息
           ↓
4. [Worker] 交换机Worker从队列获取 Task1
           ↓ (Step 1: 创建VRF)
5. [Worker] VRF创建完成，结果写入Redis
           ↓
6. [队列] 自动触发 Task2
           ↓
7. [Worker] 交换机Worker获取 Task2
           ↓ (Step 2: 创建VLAN)
8. [Worker] VLAN创建完成，结果写入Redis
           ↓
9. [队列] 自动触发 Task3
           ↓
10. [Worker] 防火墙Worker获取 Task3
           ↓ (Step 3: 创建安全区域)
11. [Worker] 防火墙配置完成，整个Workflow结束
```

### 关键特性

✅ **任务顺序保证**: Chain确保严格按序执行
✅ **消息队列解耦**: 通过Redis实现组件间通信
✅ **分布式执行**: Worker可以部署在不同机器
✅ **状态持久化**: 任务状态和结果存储在Redis
✅ **失败重试**: 支持任务失败后自动重试
✅ **结果查询**: 可通过workflow_id查询执行状态

### 示例日志输出 (Chain顺序执行)

```
[API] 创建VPC工作流: VPC=test-vpc-chain-001, WorkflowID=abc-123
[API] 任务链已发送到消息队列: VRF -> VLAN -> Firewall

[Workflow-Step1] [交换机任务] 开始创建VRF: VRF-CHAIN-001 (VPC: test-vpc-chain-001)
[Workflow-Step1] ✓ 交换机上成功创建VRF: VRF-CHAIN-001, 配置命令: ip vrf VRF-CHAIN-001

[Workflow-Step2] [交换机任务] 开始创建VLAN子接口: VLAN 100 (VPC: test-vpc-chain-001)
[Workflow-Step2] ✓ 交换机上成功创建VLAN子接口: VLAN 100, 接口配置: interface Vlan100

[Workflow-Step3] [防火墙任务] 开始创建安全区域: trust-zone-chain-001 (VPC: test-vpc-chain-001)
[Workflow-Step3] ✓ 防火墙上成功创建安全区域: trust-zone-chain-001
[Workflow-Complete] ✓✓✓ VPC test-vpc-chain-001 创建工作流全部完成 ✓✓✓
```

**注意**: 观察日志中的 `Workflow-Step1/2/3` 标记，确认任务严格按顺序执行！

---

## 系统管理

### 停止服务
```bash
./stop.sh
```

### 重启服务
```bash
./stop.sh
./start.sh
```

### 健康检查
```bash
curl http://localhost:8080/api/v1/health
```

---

## 系统架构 (基于消息队列)

```
┌─────────────────┐
│   HTTP请求       │
└────────┬────────┘
         │
         ▼
┌─────────────────────────────┐
│     API服务器 (Gin)          │
│  - 构建任务链(Chain)          │
│  - 发送到消息队列             │
└────────┬────────────────────┘
         │ SendChain()
         ▼
┌─────────────────────────────┐
│   Redis 消息队列 (Broker)    │
│  - 存储任务链                │
│  - 管理任务顺序              │  ◄─── Backend (结果存储)
│  - 分发任务给Worker          │
└────┬────────────────────┬───┘
     │ (监听队列)          │
     ▼                    ▼
┌──────────────┐    ┌──────────────┐
│ 交换机Worker  │    │ 防火墙Worker  │
│ - VRF任务    │    │ - Zone任务   │
│ - VLAN任务   │    │              │
│ (并发数: 2)  │    │ (并发数: 2)  │
└──────────────┘    └──────────────┘
     │                    │
     └─────────┬──────────┘
               ▼
        执行设备配置
        (当前为模拟)
```

### 消息队列特性

- **解耦**: API服务器与Worker完全解耦
- **异步**: 请求立即返回，任务后台执行
- **持久化**: 任务存储在Redis，重启不丢失
- **可扩展**: 可以动态增加Worker数量
- **负载均衡**: 多个Worker自动分担任务

---

## API接口

### 健康检查
- **URL**: `GET /api/v1/health`
- **响应**: `{"status": "ok", "service": "vpc-workflow-api"}`

### 创建VPC
- **URL**: `POST /api/v1/vpc`
- **请求体**:
  ```json
  {
    "vpc_name": "vpc名称",
    "vrf_name": "VRF名称",
    "vlan_id": VLAN编号,
    "firewall_zone": "防火墙区域名称"
  }
  ```
- **响应**:
  ```json
  {
    "success": true,
    "message": "VPC创建工作流已启动",
    "vpc_id": "生成的VPC ID",
    "workflow_id": "工作流ID"
  }
  ```

---

## 技术栈

- **Go 1.22** - 编程语言
- **go-machinery v1.10.6** - 分布式任务队列框架 ⭐
- **Gin v1.9.1** - Web框架
- **Redis** - 消息队列Broker和结果Backend
- **UUID** - 唯一标识符生成

### go-machinery 核心概念

- **Server**: 任务服务器，管理任务注册和发送
- **Worker**: 任务执行器，从队列获取任务并执行
- **Broker**: 消息队列 (Redis/AMQP等)
- **Backend**: 结果存储 (Redis/Memcache等)
- **Signature**: 任务签名，定义任务名称和参数
- **Chain**: 任务链，顺序执行多个任务
- **Group**: 任务组，并行执行多个任务
- **Chord**: 混合模式，先并行后回调

---

## 特性

### Workflow特性 (基于消息队列)
✅ **Chain模式**: 任务顺序执行，前一个完成后执行下一个
✅ **状态查询**: 通过workflow_id查询任务执行状态
✅ **消息队列**: 基于Redis实现异步任务处理
✅ **任务持久化**: 任务和结果存储在Redis中
✅ **分布式执行**: Worker可分布在多台机器
✅ **失败重试**: 支持任务失败后自动重试(可配置)
✅ **并发控制**: 每个Worker可配置并发数

### 系统特性
✅ RESTful API接口
✅ 任务日志记录
✅ 简单易用的启停脚本
✅ 健康检查接口

---

## 注意事项

- 这是一个**演示Demo**，任务执行器只打印日志
- 生产环境需要集成真实的设备SDK
- Redis必须运行才能使用系统
- 所有日志保存在 `logs/` 目录下

---

## 下一步扩展

### Workflow模式
1. **实现Chord模式**: 部分任务并行，完成后执行回调
   - 示例: `(VRF || VLAN) -> Firewall`
2. **实现Group模式**: 所有任务并行执行
   - 示例: `VRF || VLAN || Firewall`
3. **延迟任务**: 定时执行任务
4. **任务优先级**: 高优先级任务优先执行

### 功能增强
1. **添加更多设备类型**: 创建路由器Worker、负载均衡器Worker等
2. **集成真实设备**: 替换模拟为真实设备SDK调用
3. **任务监控**: 集成Prometheus监控任务执行情况
4. **Web管理界面**: 可视化查看任务执行状态
5. **任务回滚**: 失败时自动回滚已执行的任务
6. **任务审批**: 重要任务需要人工审批后执行

### 高级特性
1. **动态Workflow**: 根据条件动态调整任务执行流程
2. **分支执行**: 根据任务结果选择不同的执行分支
3. **子Workflow**: 复杂流程拆分为多个子工作流
4. **事件驱动**: 基于事件触发Workflow执行

---

## 相关文件

- `api/server.go` - API服务器和Chain实现
- `config/machinery.go` - 消息队列配置
- `tasks/vpc_tasks.go` - 任务定义和执行逻辑
- `examples/workflow_patterns.go` - 各种Workflow模式示例
- `cmd/*/main.go` - Worker启动程序

---

**祝使用愉快！** 🎉
