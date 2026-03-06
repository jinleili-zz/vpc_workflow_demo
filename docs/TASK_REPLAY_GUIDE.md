# 任务重做(Task Replay)功能使用指南

## 功能概述

任务重做功能允许运维人员对失败的任务进行重新执行。当任务因为临时性错误（如数据冲突、网络问题等）失败时，运维人员可以手动清理问题数据，然后通过API重新触发该任务。

## 使用场景

典型使用场景：创建子网时因为CIDR已存在而失败，运维人员可以：
1. 手动清理冲突的CIDR数据
2. 调用任务重做API
3. 任务重新执行并成功完成

## API接口

### 1. Top NSP API (跨AZ查询)

**端点**: `POST /api/v1/task/replay/:task_id`

**说明**: Top NSP会遍历所有AZ NSP，找到对应的任务并触发重做

**示例**:
```bash
curl -X POST "http://localhost:8080/api/v1/task/replay/<task_id>"
```

### 2. AZ NSP API (直接访问)

**端点**: `POST /api/v1/task/replay/:task_id`

**说明**: 直接访问指定AZ的NSP进行任务重做

**示例**:
```bash
# 需要知道AZ NSP的具体地址
curl -X POST "http://az-nsp-cn-beijing-1a:8080/api/v1/task/replay/<task_id>"
```

### 3. 查询任务详情

**端点**: `GET /api/v1/task/:task_id`

**示例**:
```bash
curl "http://az-nsp-cn-beijing-1a:8080/api/v1/task/<task_id>"
```

## 完整测试流程

### 步骤1: 创建VPC

```bash
curl -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-vpc-replay",
    "region": "cn-beijing",
    "vrf_name": "VRF-REPLAY",
    "vlan_id": 200,
    "firewall_zone": "replay-zone"
  }'
```

等待VPC创建完成（约10秒）。

### 步骤2: 创建子网触发失败

特殊CIDR `10.0.99.0/24` 会触发失败（模拟CIDR冲突）：

```bash
curl -X POST http://localhost:8080/api/v1/subnet \
  -H "Content-Type: application/json" \
  -d '{
    "subnet_name": "test-subnet-fail",
    "vpc_name": "test-vpc-replay",
    "region": "cn-beijing",
    "az": "cn-beijing-1a",
    "cidr": "10.0.99.0/24"
  }'
```

**响应示例**:
```json
{
  "success": true,
  "message": "子网创建工作流已启动",
  "subnet_id": "cb87b3c5-e005-42b6-a9ac-dfd351c85f39",
  "workflow_id": "cb87b3c5-e005-42b6-a9ac-dfd351c85f39"
}
```

记录返回的 `subnet_id`。

### 步骤3: 查询子网状态

```bash
curl "http://localhost:8080/api/v1/subnet/id/cb87b3c5-e005-42b6-a9ac-dfd351c85f39"
```

**预期响应**（任务失败）:
```json
{
  "success": true,
  "subnet": {
    "status": "failed",
    "error_message": "..."
  }
}
```

### 步骤4: 查询失败的任务ID

从数据库或日志中获取失败任务的ID：

```bash
docker exec nsp-mysql mysql -uroot -proot123456 nsp_cn_beijing_1a \
  -e "SELECT id, task_type, status, error_message FROM tasks WHERE resource_id='cb87b3c5-e005-42b6-a9ac-dfd351c85f39'"
```

**输出示例**:
```
id                                  task_type                   status  error_message
a25db66e-ad16-499b-b407-7fb9530dcdb9  create_subnet_on_switch   failed  CIDR冲突: 10.0.99.0/24 在VPC test-vpc-replay 中已存在
7db2b781-722e-4b83-8bb0-dcf6b52a01d7  configure_subnet_routing  pending NULL
```

记录失败任务的 `id` (例如: `a25db66e-ad16-499b-b407-7fb9530dcdb9`)。

### 步骤5: 模拟运维清理

在实际场景中，运维人员需要手动清理导致冲突的数据。在这个测试Demo中，我们通过修改数据库来模拟清理：

```bash
# 删除冲突的CIDR记录（实际场景中是清理网络设备上的配置）
docker exec nsp-mysql mysql -uroot -proot123456 nsp_cn_beijing_1a \
  -e "DELETE FROM subnet_cidrs WHERE cidr='10.0.99.0/24'"
```

**注意**: 这只是测试Demo，实际生产环境中需要清理网络设备上的实际配置。

### 步骤6: 重做失败的任务

使用Top NSP API重做任务：

```bash
curl -X POST "http://localhost:8080/api/v1/task/replay/a25db66e-ad16-499b-b407-7fb9530dcdb9"
```

**成功响应**:
```json
{
  "success": true,
  "message": "任务已重新入队",
  "task_id": "a25db66e-ad16-499b-b407-7fb9530dcdb9"
}
```

### 步骤7: 验证任务重新执行

等待5秒后，再次查询任务状态：

```bash
docker exec nsp-mysql mysql -uroot -proot123456 nsp_cn_beijing_1a \
  -e "SELECT id, task_type, status FROM tasks WHERE resource_id='cb87b3c5-e005-42b6-a9ac-dfd351c85f39'"
```

**预期输出**（任务成功）:
```
id                                  task_type                   status
a25db66e-ad16-499b-b407-7fb9530dcdb9  create_subnet_on_switch   completed
7db2b781-722e-4b83-8bb0-dcf6b52a01d7  configure_subnet_routing  completed
```

### 步骤8: 验证子网状态

```bash
curl "http://localhost:8080/api/v1/subnet/id/cb87b3c5-e005-42b6-a9ac-dfd351c85f39"
```

**预期响应**（子网状态正常）:
```json
{
  "success": true,
  "subnet": {
    "status": "running",
    "completed_tasks": 2,
    "failed_tasks": 0
  }
}
```

## 实现细节

### 任务状态流转

```
pending -> queued -> processing -> completed/failed
                                       ↓
                                    (replay)
                                       ↓
                                    pending -> ...
```

### 重做限制

- 只有状态为 `failed` 的任务才能被重做
- 重做时会将任务状态重置为 `pending` 并重新入队
- 重做不会改变任务的其他属性（如参数、优先级等）

### 架构说明

1. **Top NSP**: 提供统一的重做入口，自动路由到正确的AZ NSP
2. **AZ NSP**: 执行具体的任务重做逻辑，包括状态更新和任务入队
3. **Worker**: 从队列中获取任务并执行，无需知道任务是否为重做

## 故障排查

### 问题1: 任务重做失败 - 任务不存在

**原因**: 使用了错误的task_id或任务不在该AZ中

**解决**: 
- 确认task_id正确
- 使用Top NSP API（会自动查找所有AZ）

### 问题2: 任务重做失败 - 任务状态不是failed

**原因**: 任务状态不是 `failed`

**解决**: 
- 检查任务当前状态
- 只有失败的任务才能重做

### 问题3: 任务重做后仍然失败

**原因**: 导致失败的根本问题未解决

**解决**:
- 检查error_message确认失败原因
- 确保已清理导致失败的数据/配置
- 查看worker日志了解详细错误信息

## 生产环境注意事项

1. **权限控制**: 任务重做API应该受到严格的权限控制，只允许运维人员访问
2. **审计日志**: 建议记录所有任务重做操作，包括操作人、时间、原因等
3. **限流保护**: 对重做API进行限流，防止批量重做导致系统过载
4. **通知机制**: 任务重做成功/失败后应发送通知给相关人员
5. **根因分析**: 每次任务重做前应分析失败根因，避免重复失败

## 监控指标

建议监控以下指标：
- 任务失败率
- 任务重做次数
- 任务重做成功率
- 从失败到重做的平均时间间隔

## 常见CIDR冲突场景

在生产环境中，CIDR冲突通常由以下原因导致：
1. 数据库中存在残留数据但网络设备已清理
2. 网络设备配置存在但数据库中无记录
3. 并发创建导致的竞态条件
4. IP地址规划冲突

运维人员需要根据具体情况判断应该清理哪一侧的数据。
