# NSP系统架构升级设计文档

## 1. 概述

### 1.1 升级目标
将NSP系统从轻量级任务编排升级为基于MySQL持久化的任务管理系统，实现：
- 任务状态的持久化存储
- 细粒度的任务拆分与管理
- Worker通过asynq回调机制通知任务完成
- Region级资源的状态聚合查询

### 1.2 核心变更
| 变更项 | 升级前 | 升级后 |
|--------|--------|--------|
| 状态存储 | Redis (临时) | MySQL (持久化) |
| 任务粒度 | Workflow级 | Task级 (VRF、VLAN、Firewall) |
| Worker通信 | 单向(接收任务) | 双向(接收+回调) |
| 状态查询 | 简单状态 | 详细进度+任务列表 |

---

## 2. 数据模型设计

### 2.1 资源表结构

#### VPC资源表 (vpc_resources)
```sql
CREATE TABLE vpc_resources (
    id VARCHAR(64) PRIMARY KEY,           -- 资源UUID
    vpc_name VARCHAR(128) NOT NULL,       -- VPC名称
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    
    vrf_name VARCHAR(128),                -- VPC配置参数
    vlan_id INT,
    firewall_zone VARCHAR(128),
    
    status ENUM('pending', 'creating', 'running', 'failed', 'deleting', 'deleted'),
    error_message TEXT,
    
    total_tasks INT DEFAULT 0,            -- 任务统计
    completed_tasks INT DEFAULT 0,
    failed_tasks INT DEFAULT 0,
    
    created_at TIMESTAMP,
    updated_at TIMESTAMP,
    
    UNIQUE KEY uk_vpc_name_az (vpc_name, az)
);
```

#### 子网资源表 (subnet_resources)
```sql
CREATE TABLE subnet_resources (
    id VARCHAR(64) PRIMARY KEY,
    subnet_name VARCHAR(128) NOT NULL,
    vpc_name VARCHAR(128) NOT NULL,       -- 关联VPC
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    cidr VARCHAR(32) NOT NULL,
    
    status ENUM(...),                     -- 同VPC
    error_message TEXT,
    
    total_tasks INT DEFAULT 0,
    completed_tasks INT DEFAULT 0,
    failed_tasks INT DEFAULT 0,
    
    created_at TIMESTAMP,
    updated_at TIMESTAMP
);
```

#### 任务表 (tasks)
```sql
CREATE TABLE tasks (
    id VARCHAR(64) PRIMARY KEY,
    
    resource_type ENUM('vpc', 'subnet'),  -- 关联资源
    resource_id VARCHAR(64) NOT NULL,
    
    task_type VARCHAR(64) NOT NULL,       -- 任务定义
    task_name VARCHAR(128) NOT NULL,
    task_order INT NOT NULL,              -- 执行顺序
    
    task_params JSON NOT NULL,            -- 任务参数
    
    status ENUM('pending', 'queued', 'running', 'completed', 'failed', 'cancelled'),
    asynq_task_id VARCHAR(128),           -- Asynq任务ID
    
    result JSON,                          -- 执行结果
    error_message TEXT,
    retry_count INT DEFAULT 0,
    max_retries INT DEFAULT 3,
    
    az VARCHAR(64) NOT NULL,
    
    created_at TIMESTAMP,
    queued_at TIMESTAMP NULL,
    started_at TIMESTAMP NULL,
    completed_at TIMESTAMP NULL,
    updated_at TIMESTAMP,
    
    INDEX idx_resource (resource_type, resource_id),
    INDEX idx_task_order (resource_id, task_order)
);
```

### 2.2 状态机设计

#### 资源状态流转
```
pending → creating → running
   ↓         ↓          ↓
   └────→ failed     deleting → deleted
```

#### 任务状态流转
```
pending → queued → running → completed
   ↓         ↓         ↓          
   └─────────┴────→ failed → (retry) → running
```

---

## 3. 工作流程设计

### 3.1 VPC创建完整流程

```
┌─────────────────────────────────────────────────────────────┐
│ Phase 1: Top NSP接收请求                                     │
└─────────────────────────────────────────────────────────────┘
  用户 → POST /api/v1/vpc
    {
      "vpc_name": "test-vpc",
      "region": "cn-beijing",
      "vrf_name": "VRF-001",
      "vlan_id": 100,
      "firewall_zone": "trust-zone"
    }
  
  Top NSP:
  1. 查询Region下所有AZ (cn-beijing-1a, cn-beijing-1b)
  2. 预检查所有AZ健康状态
  3. 并行向所有AZ发送创建请求

┌─────────────────────────────────────────────────────────────┐
│ Phase 2: AZ NSP接收请求并持久化                              │
└─────────────────────────────────────────────────────────────┘
  AZ NSP (cn-beijing-1a):
  
  1. INSERT vpc_resources
     - id: uuid-1
     - vpc_name: "test-vpc"
     - status: 'pending'
     - total_tasks: 0
  
  2. 任务拆分:
     Task 1: create_vrf_on_switch (order=1)
     Task 2: create_vlan_subinterface (order=2)
     Task 3: create_firewall_zone (order=3)
  
  3. INSERT tasks (批量插入3条记录)
     - 所有任务 status='pending'
     - 携带 task_params (JSON)
  
  4. UPDATE vpc_resources
     - total_tasks = 3
     - status = 'creating'
  
  5. 将Task 1入队到Asynq
     - UPDATE tasks SET status='queued', asynq_task_id=xxx

┌─────────────────────────────────────────────────────────────┐
│ Phase 3: Worker执行任务                                      │
└─────────────────────────────────────────────────────────────┘
  Worker (Task 1: create_vrf_on_switch):
  
  1. 接收任务:
     payload = {
       "task_id": "task-uuid-1",
       "resource_id": "uuid-1",
       "task_params": "{\"vrf_name\":\"VRF-001\",...}"
     }
  
  2. 执行设备配置:
     - 模拟2秒延迟
     - 生成执行结果
  
  3. 回调AZ NSP:
     POST http://az-nsp-cn-beijing-1a:8080/internal/task-callback
     {
       "task_id": "task-uuid-1",
       "status": "completed",
       "result": {
         "message": "VRF创建成功",
         "vrf_name": "VRF-001"
       }
     }

┌─────────────────────────────────────────────────────────────┐
│ Phase 4: AZ NSP处理任务回调                                  │
└─────────────────────────────────────────────────────────────┘
  AZ NSP收到回调:
  
  1. UPDATE tasks
     - status = 'completed'
     - result = {...}
     - completed_at = NOW()
  
  2. UPDATE vpc_resources
     - completed_tasks = completed_tasks + 1
  
  3. 检查是否有下一个待执行任务:
     SELECT * FROM tasks 
     WHERE resource_id='uuid-1' AND status='pending'
     ORDER BY task_order LIMIT 1
     
     → 找到Task 2
  
  4. 将Task 2入队:
     - UPDATE tasks SET status='queued', asynq_task_id=yyy
     - asynqClient.Enqueue(Task 2)

┌─────────────────────────────────────────────────────────────┐
│ Phase 5: 循环执行直到完成                                    │
└─────────────────────────────────────────────────────────────┘
  重复Phase 3-4，直到：
  
  completed_tasks == total_tasks
  
  → UPDATE vpc_resources SET status='running'
  
  VPC创建完成！
```

### 3.2 任务回调机制

```
┌──────────┐                    ┌──────────┐
│  Worker  │                    │  AZ NSP  │
└────┬─────┘                    └────┬─────┘
     │                               │
     │ 1. 接收任务                   │
     │ <─────────────────────────────│
     │   (from Asynq)                │
     │                               │
     │ 2. 执行设备配置               │
     │   (2秒模拟)                   │
     │                               │
     │ 3. HTTP回调                   │
     │ ──────────────────────────────>
     │   POST /internal/task-callback│
     │   {task_id, status, result}   │
     │                               │
     │                               │ 4. 更新数据库
     │                               │    - 任务状态
     │                               │    - 资源统计
     │                               │
     │ <──────────────────────────────
     │   {"success": true}           │ 5. 入队下一任务
     │                               │    (如果有)
```

### 3.3 删除流程

```
用户 → DELETE /api/v1/vpc/test-vpc

AZ NSP:
1. 查询VPC状态
   - 必须是 'running' 状态

2. 检查关联子网
   SELECT COUNT(*) FROM subnet_resources 
   WHERE vpc_name='test-vpc' AND az='cn-beijing-1a' AND status!='deleted'
   
   → 如果 count > 0: 返回错误 "VPC下存在X个子网，无法删除"

3. 更新状态
   UPDATE vpc_resources SET status='deleting'

4. 返回成功
   (注：实际删除任务可扩展为反向任务链)
```

---

## 4. API接口设计

### 4.1 AZ NSP接口

#### 创建VPC
```http
POST /api/v1/vpc
Content-Type: application/json

{
  "vpc_name": "test-vpc-001",
  "vrf_name": "VRF-001",
  "vlan_id": 100,
  "firewall_zone": "trust-zone"
}

Response:
{
  "success": true,
  "message": "VPC创建工作流已启动",
  "vpc_id": "uuid",
  "workflow_id": "uuid"
}
```

#### 查询VPC状态
```http
GET /api/v1/vpc/test-vpc-001/status

Response:
{
  "vpc_id": "uuid",
  "vpc_name": "test-vpc-001",
  "az": "cn-beijing-1a",
  "status": "creating",
  "progress": {
    "total": 3,
    "completed": 2,
    "failed": 0,
    "pending": 1
  },
  "tasks": [
    {
      "id": "task-1",
      "task_type": "create_vrf_on_switch",
      "task_name": "创建VRF",
      "task_order": 1,
      "status": "completed",
      "started_at": "2025-12-05T10:00:00Z",
      "completed_at": "2025-12-05T10:00:02Z"
    },
    {
      "id": "task-2",
      "task_type": "create_vlan_subinterface",
      "task_name": "创建VLAN子接口",
      "task_order": 2,
      "status": "running",
      "started_at": "2025-12-05T10:00:03Z"
    },
    {
      "id": "task-3",
      "task_type": "create_firewall_zone",
      "task_name": "创建防火墙安全区域",
      "task_order": 3,
      "status": "pending"
    }
  ],
  "created_at": "2025-12-05T10:00:00Z",
  "updated_at": "2025-12-05T10:00:03Z"
}
```

#### 任务回调接口 (内部)
```http
POST /internal/task-callback
Content-Type: application/json

{
  "task_id": "task-uuid-1",
  "status": "completed",
  "result": {
    "message": "VRF创建成功",
    "vrf_name": "VRF-001"
  },
  "error_message": ""
}

Response:
{
  "success": true,
  "message": "任务回调处理成功"
}
```

### 4.2 Top NSP接口

#### 查询Region级VPC状态 (聚合)
```http
GET /api/v1/vpc/test-vpc-001/status

Response:
{
  "vpc_name": "test-vpc-001",
  "overall_status": "creating",
  "az_statuses": {
    "cn-beijing-1a": {
      "az": "cn-beijing-1a",
      "status": "running",
      "progress": {
        "total": 3,
        "completed": 3,
        "failed": 0,
        "pending": 0
      }
    },
    "cn-beijing-1b": {
      "az": "cn-beijing-1b",
      "status": "creating",
      "progress": {
        "total": 3,
        "completed": 2,
        "failed": 0,
        "pending": 1
      }
    }
  }
}
```

---

## 5. 数据库部署方案

### 5.1 MySQL容器配置
```yaml
mysql:
  image: mysql:8.0
  environment:
    - MYSQL_ROOT_PASSWORD=root123456
    - MYSQL_USER=nsp_user
    - MYSQL_PASSWORD=nsp_password
  ports:
    - "3306:3306"
  volumes:
    - mysql-data:/var/lib/mysql
    - ./init-mysql.sh:/docker-entrypoint-initdb.d/init-mysql.sh
```

### 5.2 数据库分区策略
每个AZ使用独立数据库，共享MySQL实例：

| AZ | 数据库名 |
|----|---------|
| cn-beijing-1a | nsp_cn_beijing_1a |
| cn-beijing-1b | nsp_cn_beijing_1b |
| cn-shanghai-1a | nsp_cn_shanghai_1a |

### 5.3 初始化脚本
```sql
CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1a;
CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1b;
CREATE DATABASE IF NOT EXISTS nsp_cn_shanghai_1a;

GRANT ALL PRIVILEGES ON nsp_cn_beijing_1a.* TO 'nsp_user'@'%';
GRANT ALL PRIVILEGES ON nsp_cn_beijing_1b.* TO 'nsp_user'@'%';
GRANT ALL PRIVILEGES ON nsp_cn_shanghai_1a.* TO 'nsp_user'@'%';
```

---

## 6. 关键技术实现

### 6.1 事务处理
```go
// 创建资源+任务必须在同一个事务中
tx, _ := db.Begin()
defer tx.Rollback()

// 1. 创建资源
tx.Exec("INSERT INTO vpc_resources ...")

// 2. 批量创建任务
for _, task := range tasks {
    tx.Exec("INSERT INTO tasks ...")
}

// 3. 更新资源的总任务数
tx.Exec("UPDATE vpc_resources SET total_tasks=? ...")

tx.Commit()
```

### 6.2 并发安全
```sql
-- 使用乐观锁更新资源计数
UPDATE vpc_resources 
SET completed_tasks = completed_tasks + 1,
    updated_at = NOW()
WHERE id = ? AND completed_tasks < total_tasks
```

### 6.3 任务链式执行
```go
func HandleTaskCallback(taskID, status string) {
    // 1. 更新任务状态
    UpdateTask(taskID, status)
    
    // 2. 更新资源统计
    if status == "completed" {
        IncrementCompletedTasks(resourceID)
    }
    
    // 3. 入队下一个任务
    nextTask := GetNextPendingTask(resourceID)
    if nextTask != nil {
        EnqueueTask(nextTask)
    } else {
        // 检查是否全部完成
        CheckAndCompleteResource(resourceID)
    }
}
```

---

## 7. 目录结构

```
internal/
├── db/
│   ├── mysql.go           # MySQL连接池
│   ├── migrations/
│   │   └── 001_init.sql   # 数据库初始化脚本
│   └── dao/
│       └── dao.go         # VPC/Subnet/Task DAO
├── models/
│   └── resource.go        # 资源和任务模型
└── az/
    ├── api/
    │   └── server.go      # AZ NSP API (含回调接口)
    └── orchestrator/
        └── orchestrator.go # AZ级任务编排器

tasks/
└── handlers.go            # Worker任务处理器 (含回调逻辑)

deployments/docker/
├── docker-compose.yml     # 含MySQL配置
└── init-mysql.sh          # MySQL初始化脚本
```

---

## 8. 升级影响分析

### 8.1 性能影响
| 指标 | 升级前 | 升级后 | 说明 |
|------|--------|--------|------|
| 创建VPC延迟 | ~6秒 | ~6秒 + 数据库IO | 数据库操作增加<50ms |
| 状态查询延迟 | <10ms | <50ms | MySQL查询 + JOIN |
| 并发能力 | 无限制 | 受MySQL连接池限制 | 默认25并发 |

### 8.2 可靠性提升
- ✅ 任务状态持久化，重启不丢失
- ✅ 细粒度状态追踪，便于故障排查
- ✅ 支持任务重试机制（最大3次）
- ✅ 数据库事务保证一致性

### 8.3 功能增强
- ✅ 详细的任务执行进度
- ✅ 完整的任务历史记录
- ✅ Region级状态聚合
- ✅ 删除前置检查（子网依赖）

---

## 9. 后续扩展方向

1. **任务重试优化**
   - 指数退避策略
   - 可配置的重试次数

2. **删除任务链**
   - 创建反向删除任务
   - 支持级联删除

3. **监控告警**
   - 任务失败率监控
   - 执行时长告警

4. **性能优化**
   - 读写分离
   - 任务状态缓存

---

文档版本：v1.0  
更新日期：2025-12-05  
作者：Qoder AI
