# VPC Workflow API 使用说明

## API 端点

### 1. 创建 VPC
```bash
POST /api/v1/vpc
```

**请求示例：**
```bash
curl -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "my-vpc-001",
    "vrf_name": "VRF-001",
    "vlan_id": 100,
    "firewall_zone": "trust-zone-001"
  }'
```

**响应示例：**
```json
{
  "success": true,
  "message": "VPC创建工作流已启动",
  "vpc_id": "9b1f9e2e-8b32-4b1d-80fc-db999bd04827",
  "workflow_id": "4143233b-4179-476a-b43f-96dc142095d0"
}
```

### 2. 查询 VPC 状态（通过 VPC 名字）
```bash
GET /api/v1/vpc/{vpc_name}/status
```

**请求示例：**
```bash
curl http://localhost:8080/api/v1/vpc/my-vpc-001/status
```

**响应示例：**
```json
{
  "vpc_name": "my-vpc-001",
  "workflow_id": "4143233b-4179-476a-b43f-96dc142095d0",
  "task_name": "create_vrf_on_switch",
  "state": "SUCCESS",
  "status": "completed",
  "message": "工作流执行成功",
  "results": [...]
}
```

**状态说明：**
- `pending`: 工作流执行中
- `completed`: 工作流执行成功（所有任务完成）
- `failed`: 工作流执行失败

### 3. 健康检查
```bash
GET /api/v1/health
```

## 优化说明

### 用户友好性改进
✅ **使用 VPC 名字查询状态**
- 用户无需关心内部的 workflow_id
- 查询接口更加直观：`/api/v1/vpc/{vpc_name}/status`
- VPC 名字到 workflow_id 的映射自动存储在 Redis 中（24小时过期）

### 技术实现
- 使用 Redis 客户端存储 VPC 名字到 workflow_id 的映射
- 映射键格式：`vpc_mapping:{vpc_name}`
- 自动过期时间：24 小时
- 查询流程：VPC 名字 → workflow_id → 任务状态

## 测试

运行测试脚本：
```bash
bash test.sh
```

测试脚本会自动：
1. 创建 VPC（使用 VPC 名字：`test-vpc-chain-001`）
2. 等待任务执行
3. 通过 VPC 名字查询状态
