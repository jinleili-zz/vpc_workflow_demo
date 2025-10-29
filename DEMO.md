# VPC分布式任务工作流系统 - 演示说明

## 系统已成功启动并运行！

### 当前运行状态
✅ API服务器正在运行 (端口: 8080)
✅ 交换机Worker正在运行
✅ 防火墙Worker正在运行

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

## 工作流程演示

当你发送一个创建VPC的请求后，系统会自动：

1. **API服务器** 接收请求并创建3个任务
2. **交换机Worker** 处理:
   - 创建VRF (Virtual Routing and Forwarding)
   - 创建VLAN子接口
3. **防火墙Worker** 处理:
   - 创建防火墙安全区域

### 示例日志输出

```
[交换机任务] 开始创建VRF: VRF-TEST-001 (VPC: test-vpc-001)
[交换机任务] 交换机上成功创建VRF: VRF-TEST-001, 配置命令: ip vrf VRF-TEST-001

[交换机任务] 开始创建VLAN子接口: VLAN 100 (VPC: test-vpc-001)
[交换机任务] 交换机上成功创建VLAN子接口: VLAN 100, 接口配置: interface Vlan100, ip vrf forwarding VRF-TEST-001

[防火墙任务] 开始创建安全区域: trust-zone-001 (VPC: test-vpc-001)
[防火墙任务] 防火墙上成功创建安全区域: trust-zone-001, 配置命令: security-zone name trust-zone-001, set priority 100
```

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

## 系统架构

```
┌─────────────────┐
│   用户请求       │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  API服务器       │ (RESTful API, 端口8080)
│  创建任务        │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Redis队列       │ (消息队列)
└────────┬────────┘
         │
         ├──────────────┬──────────────┐
         │              │              │
         ▼              ▼              ▼
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│ 交换机       │  │ 交换机       │  │ 防火墙       │
│ Worker      │  │ Worker      │  │ Worker      │
│ (创建VRF)   │  │ (创建VLAN)  │  │ (创建Zone)  │
└─────────────┘  └─────────────┘  └─────────────┘
```

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

- **Go 1.19**
- **go-machinery v1.10.6** - 分布式任务队列
- **Gin v1.9.1** - Web框架
- **Redis** - 消息队列和结果存储
- **UUID** - 唯一标识符生成

---

## 特性

✅ RESTful API接口
✅ 分布式任务处理
✅ 异步任务执行
✅ 多Worker并发处理
✅ 任务日志记录
✅ 简单易用的启停脚本

---

## 注意事项

- 这是一个**演示Demo**，任务执行器只打印日志
- 生产环境需要集成真实的设备SDK
- Redis必须运行才能使用系统
- 所有日志保存在 `logs/` 目录下

---

## 下一步

如果需要扩展系统:

1. **添加更多设备类型**: 创建新的Worker (如路由器Worker)
2. **添加任务状态查询**: 实现通过workflow_id查询任务状态的API
3. **集成真实设备**: 替换打印日志为真实的设备操作
4. **添加任务重试**: 配置失败任务的重试策略
5. **添加监控**: 集成Prometheus等监控工具

---

**祝使用愉快！** 🎉
