# 并发测试脚本说明

本目录包含两个VPC并发创建测试脚本。

## 脚本列表

### 1. concurrent_test.sh (完整版)
**功能特性:**
- 支持GNU parallel加速并发
- 彩色终端输出
- 详细的性能指标统计
- 平均响应时间计算
- 更精细的错误处理

**依赖:**
- jq (JSON解析)
- bc (浮点数计算)
- GNU parallel (可选，用于更高效的并发)

**使用方法:**
```bash
cd /root/workspace/nsp/vpc_workflow_demo/scripts
./concurrent_test.sh
```

**参数配置 (编辑脚本顶部):**
```bash
TOP_NSP_ADDR="http://localhost:8080"  # Top NSP地址
VPC_COUNT=100                          # VPC数量
CONCURRENT_JOBS=20                     # 并发数
REGION="cn-beijing"                    # 区域
```

### 2. concurrent_test_simple.sh (简化版)
**功能特性:**
- 无外部依赖
- 使用bash后台进程实现并发
- 适用于任何Linux环境
- 基础的统计功能

**依赖:**
- 仅需bash和curl

**使用方法:**
```bash
cd /root/workspace/nsp/vpc_workflow_demo/scripts
./concurrent_test_simple.sh
```

**参数配置 (编辑脚本顶部):**
```bash
TOP_NSP_ADDR="http://localhost:8080"
VPC_COUNT=100
REGION="cn-beijing"
```

## 测试流程

两个脚本都执行以下测试流程:

1. **阶段1: 并发创建VPC**
   - 向Top NSP发送100个VPC创建请求
   - 并发执行，记录响应结果

2. **阶段2: 等待任务执行**
   - 等待10秒让Worker完成任务执行
   - 每个VPC有3个顺序任务，每个约2秒

3. **阶段3: 查询VPC状态**
   - 并发查询所有VPC的状态
   - 记录每个VPC的overall_status

4. **阶段4: 统计结果**
   - 成功率统计
   - 性能指标计算
   - 生成测试报告

## 输出结果

### 控制台输出
```
========================================
VPC并发创建测试
========================================
[阶段1] 并发创建 100 个VPC
✓ test-vpc-001 创建请求成功
✓ test-vpc-002 创建请求成功
...

[阶段3] 查询VPC状态
✓ test-vpc-001: running
✓ test-vpc-002: running
...

========================================
测试结果汇总
========================================
VPC状态分布:
  Running (完成): 100
  Creating (进行中): 0
  Failed (失败): 0
  Unknown (未知): 0

成功率: 100%

性能指标:
  创建耗时: 5s
  查询耗时: 2s
  创建吞吐量: 20 VPC/s
```

### 日志文件
测试会在当前目录生成日志目录: `test_logs_YYYYMMDD_HHMMSS/`

包含文件:
- `create_results.txt` - 创建请求结果
- `query_results.txt` - 状态查询结果
- `vpc_list.txt` - VPC名称列表

## 数据库验证

测试完成后，可以验证MySQL数据:

```bash
# 查看VPC总数
docker exec nsp-mysql mysql -unsp_user -pnsp_password nsp_cn_beijing_1a \
  -e "SELECT COUNT(*) as vpc_count FROM vpc_resources;"

# 查看状态分布
docker exec nsp-mysql mysql -unsp_user -pnsp_password nsp_cn_beijing_1a \
  -e "SELECT status, COUNT(*) as count FROM vpc_resources GROUP BY status;"

# 查看任务统计
docker exec nsp-mysql mysql -unsp_user -pnsp_password nsp_cn_beijing_1a \
  -e "SELECT COUNT(*) as task_count, status FROM tasks GROUP BY status;"

# 查看完成情况
docker exec nsp-mysql mysql -unsp_user -pnsp_password nsp_cn_beijing_1a \
  -e "SELECT vpc_name, status, total_tasks, completed_tasks, failed_tasks FROM vpc_resources LIMIT 10;"
```

## 性能调优建议

### 1. 增加Worker并发数
编辑 `docker-compose.yml`:
```yaml
worker-cn-beijing-1a:
  environment:
    - WORKER_COUNT=10  # 默认是2，可以增加到10
```

### 2. 增加MySQL连接池
编辑 `internal/db/mysql.go`:
```go
db.SetMaxOpenConns(50)  // 默认25
db.SetMaxIdleConns(10)  // 默认5
```

### 3. 增加Redis连接
如果Redis成为瓶颈，可以考虑使用Redis集群

### 4. 调整并发测试参数
- `VPC_COUNT`: 根据系统资源调整
- `CONCURRENT_JOBS`: 建议不超过50

## 故障排查

### 1. 创建请求失败
检查Top NSP日志:
```bash
docker logs top-nsp
```

### 2. 任务执行失败
检查Worker日志:
```bash
docker logs worker-cn-beijing-1a
docker logs worker-cn-beijing-1b
```

### 3. 数据库连接问题
检查AZ NSP日志:
```bash
docker logs az-nsp-cn-beijing-1a
```

### 4. VPC卡在creating状态
可能原因:
- Worker未启动或崩溃
- asynq队列阻塞
- 回调队列未正确配置

检查asynq队列状态:
```bash
docker exec nsp-redis redis-cli -n 1 KEYS "asynq:*"
```

## 清理测试数据

```bash
# 清理MySQL数据
docker exec nsp-mysql mysql -unsp_user -pnsp_password nsp_cn_beijing_1a \
  -e "DELETE FROM vpc_resources; DELETE FROM tasks;"

docker exec nsp-mysql mysql -unsp_user -pnsp_password nsp_cn_beijing_1b \
  -e "DELETE FROM vpc_resources; DELETE FROM tasks;"

# 清理Redis数据
docker exec nsp-redis redis-cli -n 0 FLUSHDB
docker exec nsp-redis redis-cli -n 1 FLUSHDB

# 清理测试日志
rm -rf test_logs_*
```

## 预期结果

在正常情况下:
- **创建成功率**: 100%
- **最终状态分布**: 100% running
- **创建吞吐量**: 15-25 VPC/s (取决于硬件)
- **平均API响应**: < 0.5s

如果成功率低于95%，建议检查系统资源和配置。
