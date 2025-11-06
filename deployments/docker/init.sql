-- NSP Workflow System Database Schema
-- 创建数据库
CREATE DATABASE IF NOT EXISTS nsp_workflow DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

USE nsp_workflow;

-- 工作流主表
CREATE TABLE IF NOT EXISTS workflows (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    workflow_id VARCHAR(64) NOT NULL UNIQUE COMMENT '工作流唯一ID',
    resource_type ENUM('vpc', 'subnet') NOT NULL COMMENT '资源类型',
    resource_name VARCHAR(255) NOT NULL COMMENT '资源名称(VPC名/子网名)',
    resource_id VARCHAR(64) COMMENT '资源ID',
    region VARCHAR(64) NOT NULL COMMENT 'Region',
    az VARCHAR(64) COMMENT 'AZ(子网级别需要)',
    status ENUM('pending', 'running', 'completed', 'failed') NOT NULL DEFAULT 'pending' COMMENT '工作流状态',
    error_message TEXT COMMENT '错误信息',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_resource_name (resource_name),
    INDEX idx_workflow_id (workflow_id),
    INDEX idx_status (status),
    INDEX idx_region_az (region, az)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='工作流主表';

-- 任务表
CREATE TABLE IF NOT EXISTS tasks (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id VARCHAR(64) NOT NULL UNIQUE COMMENT '任务唯一ID',
    workflow_id VARCHAR(64) NOT NULL COMMENT '所属工作流ID',
    task_name VARCHAR(128) NOT NULL COMMENT '任务名称',
    task_type ENUM('switch', 'firewall') NOT NULL COMMENT '任务类型(设备类型)',
    sequence_order INT NOT NULL DEFAULT 0 COMMENT '执行顺序',
    status ENUM('pending', 'running', 'completed', 'failed') NOT NULL DEFAULT 'pending' COMMENT '任务状态',
    payload JSON NOT NULL COMMENT '任务参数(JSON格式)',
    result JSON COMMENT '执行结果(JSON格式)',
    error_message TEXT COMMENT '错误信息',
    retry_count INT NOT NULL DEFAULT 0 COMMENT '重试次数',
    max_retries INT NOT NULL DEFAULT 3 COMMENT '最大重试次数',
    worker_id VARCHAR(64) COMMENT '执行该任务的Worker ID',
    started_at TIMESTAMP NULL COMMENT '任务开始时间',
    completed_at TIMESTAMP NULL COMMENT '任务完成时间',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_workflow_id (workflow_id),
    INDEX idx_task_type_status (task_type, status),
    INDEX idx_status (status),
    INDEX idx_created_at (created_at),
    FOREIGN KEY (workflow_id) REFERENCES workflows(workflow_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='任务表';

-- AZ注册表
CREATE TABLE IF NOT EXISTS az_registry (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    az_id VARCHAR(64) NOT NULL UNIQUE COMMENT 'AZ唯一标识',
    region VARCHAR(64) NOT NULL COMMENT 'Region',
    az_name VARCHAR(128) NOT NULL COMMENT 'AZ名称',
    nsp_addr VARCHAR(255) NOT NULL COMMENT 'NSP服务地址',
    status ENUM('online', 'offline') NOT NULL DEFAULT 'online' COMMENT '状态',
    last_heartbeat TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '最后心跳时间',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_region (region),
    INDEX idx_status (status),
    INDEX idx_last_heartbeat (last_heartbeat)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='AZ注册表';

-- Worker注册表
CREATE TABLE IF NOT EXISTS worker_registry (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    worker_id VARCHAR(64) NOT NULL UNIQUE COMMENT 'Worker唯一标识',
    worker_type ENUM('switch', 'firewall') NOT NULL COMMENT 'Worker类型',
    region VARCHAR(64) NOT NULL COMMENT 'Region',
    az VARCHAR(64) NOT NULL COMMENT 'AZ',
    worker_addr VARCHAR(255) COMMENT 'Worker地址',
    status ENUM('online', 'offline') NOT NULL DEFAULT 'online' COMMENT '状态',
    max_concurrency INT NOT NULL DEFAULT 3 COMMENT '最大并发数',
    current_tasks INT NOT NULL DEFAULT 0 COMMENT '当前执行任务数',
    last_heartbeat TIMESTAMP DEFAULT CURRENT_TIMESTAMP COMMENT '最后心跳时间',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_worker_type_status (worker_type, status),
    INDEX idx_region_az (region, az),
    INDEX idx_last_heartbeat (last_heartbeat)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Worker注册表';

-- VPC映射表(用于通过VPC名查询)
CREATE TABLE IF NOT EXISTS resource_mappings (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    resource_type ENUM('vpc', 'subnet') NOT NULL COMMENT '资源类型',
    resource_name VARCHAR(255) NOT NULL COMMENT '资源名称',
    resource_id VARCHAR(64) NOT NULL COMMENT '资源ID',
    workflow_id VARCHAR(64) NOT NULL COMMENT '工作流ID',
    region VARCHAR(64) NOT NULL COMMENT 'Region',
    az VARCHAR(64) COMMENT 'AZ',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_resource (resource_type, resource_name),
    INDEX idx_workflow_id (workflow_id),
    INDEX idx_region_az (region, az)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='资源映射表';

-- 插入测试数据(可选)
-- INSERT INTO az_registry (az_id, region, az_name, nsp_addr, status) VALUES
-- ('cn-beijing-1a', 'cn-beijing', 'cn-beijing-1a', 'http://az-nsp-cn-beijing-1a:8080', 'online'),
-- ('cn-beijing-1b', 'cn-beijing', 'cn-beijing-1b', 'http://az-nsp-cn-beijing-1b:8080', 'online'),
-- ('cn-shanghai-1a', 'cn-shanghai', 'cn-shanghai-1a', 'http://az-nsp-cn-shanghai-1a:8080', 'online');
