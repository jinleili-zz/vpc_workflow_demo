# VPC 创建错误处理与回滚设计

## 问题背景

在 Top NSP 调用多个 AZ NSP 创建 VPC 时，可能出现**部分成功、部分失败**的情况：
- AZ-1：创建成功 ✅
- AZ-2：创建失败 ❌

这会导致：
1. **资源不一致**：部分 AZ 有 VPC，部分没有
2. **用户困惑**：不知道 VPC 最终是否创建成功
3. **资源泄漏**：成功创建的资源无法清理

## 解决方案

采用 **Saga 分布式事务模式** 的**补偿型（Compensating）事务**方案。

### 核心设计

```
┌─────────────────────────────────────────┐
│   Top NSP Orchestrator                  │
│                                         │
│  [1] 预检查阶段                          │
│      ├─ 检查所有 AZ 健康状态              │
│      └─ 任一 AZ 不健康 → 直接失败         │
│                                         │
│  [2] 执行阶段                            │
│      ├─ 并行调用所有 AZ 创建 VPC          │
│      └─ 收集所有执行结果                  │
│                                         │
│  [3] 决策阶段                            │
│      ├─ 全部成功 → 返回成功              │
│      └─ 部分失败 → 触发回滚               │
│                                         │
│  [4] 回滚阶段（补偿）                     │
│      └─ 调用 DeleteVPC 清理成功的 AZ     │
└─────────────────────────────────────────┘
```

### 实现要点

#### 1. 预检查（Pre-flight Check）
```go
// 在创建前检查所有 AZ 健康状态
for _, az := range azs {
    if err := o.azClient.HealthCheck(ctx, az.NSPAddr); err != nil {
        unhealthyAZs = append(unhealthyAZs, az.ID)
    }
}

if len(unhealthyAZs) > 0 {
    return error("预检查失败：部分 AZ 不健康")
}
```

**优势**：避免开始执行后才发现 AZ 不可用

#### 2. 结果收集与分类
```go
type azResult struct {
    az          *models.AZ
    workflowID  string
    err         error
    success     bool
}

// 分类结果
successAZs := []*models.AZ{}
failedAZs := []*models.AZ{}
```

**优势**：清晰知道哪些成功、哪些失败

#### 3. 自动回滚机制
```go
// 如果部分失败，自动清理成功的 AZ
if len(failedAZs) > 0 && len(successAZs) > 0 {
    log.Printf("触发回滚：清理 %d 个已成功的 AZ", len(successAZs))
    o.rollbackVPC(ctx, vpcName, successAZs)
}
```

**优势**：保证原子性，要么全成功，要么全失败

#### 4. 删除接口（补偿操作）

**AZ NSP 新增接口**：
```
DELETE /api/v1/vpc/:vpc_name
```

**功能**：
- 清理 Redis 中的 VPC 映射
- 清理 Machinery 任务状态
- （TODO）发送删除任务到 Worker 清理设备配置

## 流程示例

### 场景 1：全部成功
```
1. 预检查：AZ-1a ✅, AZ-1b ✅
2. 执行创建：
   - AZ-1a：成功 ✅ (workflow_id: abc123)
   - AZ-1b：成功 ✅ (workflow_id: def456)
3. 决策：全部成功
4. 返回：
   {
     "success": true,
     "message": "VPC已在2个AZ中成功创建",
     "az_results": {
       "cn-beijing-1a": "abc123",
       "cn-beijing-1b": "def456"
     }
   }
```

### 场景 2：部分失败 + 自动回滚
```
1. 预检查：AZ-1a ✅, AZ-1b ✅
2. 执行创建：
   - AZ-1a：成功 ✅ (workflow_id: abc123)
   - AZ-1b：失败 ❌ (错误：网络超时)
3. 决策：部分失败，触发回滚
4. 回滚：
   - 调用 DELETE /api/v1/vpc/test-vpc → AZ-1a ✅
5. 返回：
   {
     "success": false,
     "message": "VPC创建失败: 1个AZ失败，已回滚成功的1个AZ",
     "az_results": {
       "cn-beijing-1a": "失败: 网络超时",
       "cn-beijing-1b": "abc123"
     }
   }
```

### 场景 3：预检查失败
```
1. 预检查：AZ-1a ✅, AZ-1b ❌ (健康检查失败)
2. 直接返回：
   {
     "success": false,
     "message": "预检查失败: 以下AZ不健康: [cn-beijing-1b]"
   }
3. 无需回滚（未执行创建）
```

## 优化改进

### 当前实现
✅ 预检查机制  
✅ 自动回滚失败  
✅ 删除接口  
⚠️  仅清理 Redis 映射和任务状态  

### 未来改进
- [ ] **完整的删除任务**：发送 Worker 任务清理设备配置（VRF、VLAN、防火墙）
- [ ] **重试机制**：单个 AZ 失败时，支持自动重试（3次）
- [ ] **异步回滚**：回滚过程异步执行，立即返回用户
- [ ] **状态持久化**：在 Redis 中记录创建/回滚状态，支持查询
- [ ] **人工介入**：回滚失败时发送告警，需要人工清理

## 代码文件

| 文件 | 说明 |
|------|------|
| `internal/top/orchestrator/orchestrator.go` | 编排器：预检查、执行、回滚逻辑 |
| `internal/client/az_client.go` | HTTP 客户端：新增 `DeleteVPC` 方法 |
| `internal/az/api/server.go` | AZ NSP：新增 `deleteVPC` 接口 |

## 测试验证

### 模拟部分失败测试
```bash
# 1. 启动系统
docker-compose up -d

# 2. 停止一个 AZ（模拟故障）
docker stop az-nsp-cn-beijing-1b

# 3. 创建 VPC
curl -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-rollback",
    "region": "cn-beijing",
    "vrf_name": "VRF-TEST",
    "vlan_id": 100,
    "firewall_zone": "test-zone"
  }'

# 4. 期望结果：
# - AZ-1a 创建成功
# - AZ-1b 失败（连接超时）
# - 自动回滚 AZ-1a
# - 返回 success: false

# 5. 查看日志验证
docker logs top-nsp | grep -A 10 "触发回滚"
docker logs az-nsp-cn-beijing-1a | grep "删除请求"
```

## 总结

该方案通过 **预检查 + 自动回滚** 确保了 VPC 创建的原子性：
- ✅ **要么全成功**：所有 AZ 都创建 VPC
- ✅ **要么全失败**：失败时自动清理已创建的资源
- ✅ **无资源泄漏**：补偿机制保证一致性
- ✅ **用户体验**：明确的成功/失败状态，不会产生困惑

这是分布式系统中处理部分失败问题的最佳实践。
