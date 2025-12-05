CREATE TABLE IF NOT EXISTS vpc_resources (
    id VARCHAR(64) PRIMARY KEY COMMENT '资源ID (UUID)',
    vpc_name VARCHAR(128) NOT NULL COMMENT 'VPC名称',
    region VARCHAR(64) NOT NULL COMMENT '区域',
    az VARCHAR(64) NOT NULL COMMENT '可用区',
    
    vrf_name VARCHAR(128) COMMENT 'VRF名称',
    vlan_id INT COMMENT 'VLAN ID',
    firewall_zone VARCHAR(128) COMMENT '防火墙安全区域',
    
    status ENUM('pending', 'creating', 'running', 'failed', 'deleting', 'deleted') NOT NULL DEFAULT 'pending' COMMENT '资源状态',
    error_message TEXT COMMENT '错误信息',
    
    total_tasks INT DEFAULT 0 COMMENT '总任务数',
    completed_tasks INT DEFAULT 0 COMMENT '已完成任务数',
    failed_tasks INT DEFAULT 0 COMMENT '失败任务数',
    
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    
    UNIQUE KEY uk_vpc_name_az (vpc_name, az),
    INDEX idx_status (status),
    INDEX idx_az (az),
    INDEX idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='VPC资源表';

CREATE TABLE IF NOT EXISTS subnet_resources (
    id VARCHAR(64) PRIMARY KEY COMMENT '资源ID (UUID)',
    subnet_name VARCHAR(128) NOT NULL COMMENT '子网名称',
    vpc_name VARCHAR(128) NOT NULL COMMENT '所属VPC名称',
    region VARCHAR(64) NOT NULL COMMENT '区域',
    az VARCHAR(64) NOT NULL COMMENT '可用区',
    cidr VARCHAR(32) NOT NULL COMMENT 'CIDR地址段',
    
    status ENUM('pending', 'creating', 'running', 'failed', 'deleting', 'deleted') NOT NULL DEFAULT 'pending' COMMENT '资源状态',
    error_message TEXT COMMENT '错误信息',
    
    total_tasks INT DEFAULT 0 COMMENT '总任务数',
    completed_tasks INT DEFAULT 0 COMMENT '已完成任务数',
    failed_tasks INT DEFAULT 0 COMMENT '失败任务数',
    
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    
    UNIQUE KEY uk_subnet_name_az (subnet_name, az),
    INDEX idx_vpc_name (vpc_name),
    INDEX idx_status (status),
    INDEX idx_az (az)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='子网资源表';

CREATE TABLE IF NOT EXISTS tasks (
    id VARCHAR(64) PRIMARY KEY COMMENT '任务ID (UUID)',
    
    resource_type ENUM('vpc', 'subnet') NOT NULL COMMENT '资源类型',
    resource_id VARCHAR(64) NOT NULL COMMENT '关联资源ID',
    
    task_type VARCHAR(64) NOT NULL COMMENT '任务类型',
    task_name VARCHAR(128) NOT NULL COMMENT '任务名称',
    task_order INT NOT NULL COMMENT '执行顺序',
    
    task_params JSON NOT NULL COMMENT '任务参数',
    
    status ENUM('pending', 'queued', 'running', 'completed', 'failed', 'cancelled') NOT NULL DEFAULT 'pending' COMMENT '任务状态',
    asynq_task_id VARCHAR(128) COMMENT 'Asynq任务ID',
    
    result JSON COMMENT '执行结果',
    error_message TEXT COMMENT '错误信息',
    retry_count INT DEFAULT 0 COMMENT '重试次数',
    max_retries INT DEFAULT 3 COMMENT '最大重试次数',
    
    az VARCHAR(64) NOT NULL COMMENT '可用区',
    
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    queued_at TIMESTAMP NULL COMMENT '入队时间',
    started_at TIMESTAMP NULL COMMENT '开始执行时间',
    completed_at TIMESTAMP NULL COMMENT '完成时间',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    
    INDEX idx_resource (resource_type, resource_id),
    INDEX idx_status (status),
    INDEX idx_task_order (resource_id, task_order),
    INDEX idx_asynq_task_id (asynq_task_id),
    INDEX idx_az (az)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='任务表';