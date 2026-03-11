-- PostgreSQL Migration Script for VPC Workflow Demo
-- Converted from MySQL to PostgreSQL

-- =====================================================
-- PART 1: Enum Types
-- =====================================================

DO $$ BEGIN
    CREATE TYPE resource_status AS ENUM ('pending', 'creating', 'running', 'failed', 'deleting', 'deleted');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

DO $$ BEGIN
    CREATE TYPE task_status AS ENUM ('pending', 'queued', 'running', 'completed', 'failed', 'cancelled');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

DO $$ BEGIN
    CREATE TYPE resource_type AS ENUM ('vpc', 'subnet', 'firewall_policy');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- =====================================================
-- PART 2: Helper Function for auto-update updated_at
-- =====================================================

CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- =====================================================
-- PART 3: AZ Level Tables (每个 AZ 独立数据库)
-- =====================================================

-- VPC 资源表
CREATE TABLE IF NOT EXISTS vpc_resources (
    id VARCHAR(64) PRIMARY KEY,
    vpc_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    
    vrf_name VARCHAR(128),
    vlan_id INT,
    firewall_zone VARCHAR(128),
    
    status resource_status NOT NULL DEFAULT 'pending',
    error_message TEXT,
    
    total_tasks INT DEFAULT 0,
    completed_tasks INT DEFAULT 0,
    failed_tasks INT DEFAULT 0,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT uk_vpc_name_az UNIQUE (vpc_name, az)
);

CREATE INDEX IF NOT EXISTS idx_vpc_status ON vpc_resources(status);
CREATE INDEX IF NOT EXISTS idx_vpc_az ON vpc_resources(az);
CREATE INDEX IF NOT EXISTS idx_vpc_created_at ON vpc_resources(created_at);

DROP TRIGGER IF EXISTS update_vpc_resources_updated_at ON vpc_resources;
CREATE TRIGGER update_vpc_resources_updated_at
    BEFORE UPDATE ON vpc_resources
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE vpc_resources IS 'VPC资源表';
COMMENT ON COLUMN vpc_resources.id IS '资源ID (UUID)';
COMMENT ON COLUMN vpc_resources.vpc_name IS 'VPC名称';
COMMENT ON COLUMN vpc_resources.region IS '区域';
COMMENT ON COLUMN vpc_resources.az IS '可用区';
COMMENT ON COLUMN vpc_resources.status IS '资源状态';

-- Subnet 资源表
CREATE TABLE IF NOT EXISTS subnet_resources (
    id VARCHAR(64) PRIMARY KEY,
    subnet_name VARCHAR(128) NOT NULL,
    vpc_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    cidr VARCHAR(32) NOT NULL,
    
    status resource_status NOT NULL DEFAULT 'pending',
    error_message TEXT,
    
    total_tasks INT DEFAULT 0,
    completed_tasks INT DEFAULT 0,
    failed_tasks INT DEFAULT 0,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT uk_subnet_name_az UNIQUE (subnet_name, az)
);

CREATE INDEX IF NOT EXISTS idx_subnet_vpc_name ON subnet_resources(vpc_name);
CREATE INDEX IF NOT EXISTS idx_subnet_status ON subnet_resources(status);
CREATE INDEX IF NOT EXISTS idx_subnet_az ON subnet_resources(az);

DROP TRIGGER IF EXISTS update_subnet_resources_updated_at ON subnet_resources;
CREATE TRIGGER update_subnet_resources_updated_at
    BEFORE UPDATE ON subnet_resources
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE subnet_resources IS '子网资源表';

-- Tasks 任务表
CREATE TABLE IF NOT EXISTS tasks (
    id VARCHAR(64) PRIMARY KEY,
    
    resource_type resource_type NOT NULL,
    resource_id VARCHAR(64) NOT NULL,
    
    task_type VARCHAR(64) NOT NULL,
    task_name VARCHAR(128) NOT NULL,
    task_order INT NOT NULL,
    
    task_params JSONB NOT NULL,
    
    status task_status NOT NULL DEFAULT 'pending',
    priority INT DEFAULT 3,
    device_type VARCHAR(32) DEFAULT 'switch',
    asynq_task_id VARCHAR(128),
    
    result JSONB,
    error_message TEXT,
    retry_count INT DEFAULT 0,
    max_retries INT DEFAULT 3,
    
    az VARCHAR(64) NOT NULL,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    queued_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_resource ON tasks(resource_type, resource_id);
CREATE INDEX IF NOT EXISTS idx_task_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_task_order ON tasks(resource_id, task_order);
CREATE INDEX IF NOT EXISTS idx_task_asynq_id ON tasks(asynq_task_id);
CREATE INDEX IF NOT EXISTS idx_task_az ON tasks(az);
CREATE INDEX IF NOT EXISTS idx_task_device_type ON tasks(device_type);
CREATE INDEX IF NOT EXISTS idx_task_priority ON tasks(priority);

DROP TRIGGER IF EXISTS update_tasks_updated_at ON tasks;
CREATE TRIGGER update_tasks_updated_at
    BEFORE UPDATE ON tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE tasks IS '任务表';
COMMENT ON COLUMN tasks.priority IS '任务优先级: 1=低, 3=普通, 6=高, 9=紧急';
COMMENT ON COLUMN tasks.device_type IS '设备类型: switch, loadbalancer, firewall';

-- =====================================================
-- PART 4: Top Level Tables (Top 层数据库)
-- =====================================================

-- VPC 注册表 (Top层拓扑)
CREATE TABLE IF NOT EXISTS vpc_registry (
    id VARCHAR(64) PRIMARY KEY,
    vpc_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    az_vpc_id VARCHAR(64),
    
    vrf_name VARCHAR(128),
    vlan_id INT,
    firewall_zone VARCHAR(128),
    
    status VARCHAR(32) DEFAULT 'pending',
    saga_tx_id VARCHAR(64) DEFAULT '',
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT uk_vpc_registry_name_az UNIQUE (vpc_name, az)
);

CREATE INDEX IF NOT EXISTS idx_vpc_registry_region ON vpc_registry(region);
CREATE INDEX IF NOT EXISTS idx_vpc_registry_zone ON vpc_registry(firewall_zone);
CREATE INDEX IF NOT EXISTS idx_vpc_registry_saga_tx_id ON vpc_registry(saga_tx_id);

DROP TRIGGER IF EXISTS update_vpc_registry_updated_at ON vpc_registry;
CREATE TRIGGER update_vpc_registry_updated_at
    BEFORE UPDATE ON vpc_registry
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE vpc_registry IS 'Top层VPC拓扑注册表';

-- Subnet 注册表 (Top层拓扑)
CREATE TABLE IF NOT EXISTS subnet_registry (
    id VARCHAR(64) PRIMARY KEY,
    subnet_name VARCHAR(128) NOT NULL,
    vpc_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    az_subnet_id VARCHAR(64),
    cidr VARCHAR(32) NOT NULL,
    firewall_zone VARCHAR(128),
    
    status VARCHAR(32) DEFAULT 'pending',
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT uk_subnet_registry_name_az UNIQUE (subnet_name, az)
);

CREATE INDEX IF NOT EXISTS idx_subnet_registry_vpc ON subnet_registry(vpc_name);
CREATE INDEX IF NOT EXISTS idx_subnet_registry_region ON subnet_registry(region);

DROP TRIGGER IF EXISTS update_subnet_registry_updated_at ON subnet_registry;
CREATE TRIGGER update_subnet_registry_updated_at
    BEFORE UPDATE ON subnet_registry
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE subnet_registry IS 'Top层子网拓扑注册表';

-- CIDR Zone 映射表 (用于IP查找Zone)
CREATE TABLE IF NOT EXISTS cidr_zone_mapping (
    id VARCHAR(64) PRIMARY KEY,
    cidr VARCHAR(32) NOT NULL,
    cidr_start BIGINT NOT NULL,
    cidr_end BIGINT NOT NULL,
    
    vpc_name VARCHAR(128) NOT NULL,
    subnet_name VARCHAR(128) NOT NULL,
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    firewall_zone VARCHAR(128),
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT uk_cidr_az UNIQUE (cidr, az)
);

CREATE INDEX IF NOT EXISTS idx_cidr_range ON cidr_zone_mapping(cidr_start, cidr_end);

DROP TRIGGER IF EXISTS update_cidr_zone_mapping_updated_at ON cidr_zone_mapping;
CREATE TRIGGER update_cidr_zone_mapping_updated_at
    BEFORE UPDATE ON cidr_zone_mapping
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE cidr_zone_mapping IS 'CIDR与Zone映射表，用于IP反查';

-- =====================================================
-- PART 5: VFW (防火墙) Tables
-- =====================================================

-- Policy 注册表 (Top层)
CREATE TABLE IF NOT EXISTS policy_registry (
    id VARCHAR(64) PRIMARY KEY,
    policy_name VARCHAR(128) NOT NULL UNIQUE,
    
    source_ip VARCHAR(64) NOT NULL,
    dest_ip VARCHAR(64) NOT NULL,
    source_port VARCHAR(32),
    dest_port VARCHAR(32),
    protocol VARCHAR(16) NOT NULL,
    action VARCHAR(16) NOT NULL,
    description TEXT,
    
    source_vpc VARCHAR(128),
    dest_vpc VARCHAR(128),
    source_zone VARCHAR(128),
    dest_zone VARCHAR(128),
    source_region VARCHAR(64),
    dest_region VARCHAR(64),
    source_az VARCHAR(64),
    dest_az VARCHAR(64),
    
    status VARCHAR(32) DEFAULT 'pending',
    error_message TEXT,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_policy_source_zone ON policy_registry(source_zone);
CREATE INDEX IF NOT EXISTS idx_policy_dest_zone ON policy_registry(dest_zone);
CREATE INDEX IF NOT EXISTS idx_policy_status ON policy_registry(status);

DROP TRIGGER IF EXISTS update_policy_registry_updated_at ON policy_registry;
CREATE TRIGGER update_policy_registry_updated_at
    BEFORE UPDATE ON policy_registry
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE policy_registry IS '防火墙策略注册表';

-- Policy AZ 记录表
CREATE TABLE IF NOT EXISTS policy_az_records (
    id VARCHAR(64) PRIMARY KEY,
    policy_id VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    az_policy_id VARCHAR(64),
    
    status VARCHAR(32) DEFAULT 'pending',
    error_message TEXT,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT uk_policy_az UNIQUE (policy_id, az),
    CONSTRAINT fk_policy_id FOREIGN KEY (policy_id) REFERENCES policy_registry(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_policy_az_policy_id ON policy_az_records(policy_id);

DROP TRIGGER IF EXISTS update_policy_az_records_updated_at ON policy_az_records;
CREATE TRIGGER update_policy_az_records_updated_at
    BEFORE UPDATE ON policy_az_records
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE policy_az_records IS '策略在各AZ的执行记录';

-- =====================================================
-- PART 6: AZ Level VFW Tables (每个 AZ 独立数据库)
-- =====================================================

-- AZ 层防火墙策略表
CREATE TABLE IF NOT EXISTS firewall_policies (
    id VARCHAR(64) PRIMARY KEY,
    policy_name VARCHAR(128) NOT NULL,
    
    source_ip VARCHAR(64) NOT NULL,
    dest_ip VARCHAR(64) NOT NULL,
    source_port VARCHAR(32),
    dest_port VARCHAR(32),
    protocol VARCHAR(16) NOT NULL,
    action VARCHAR(16) NOT NULL,
    description TEXT,
    
    source_zone VARCHAR(128),
    dest_zone VARCHAR(128),
    
    region VARCHAR(64),
    az VARCHAR(64),
    
    status resource_status NOT NULL DEFAULT 'pending',
    error_message TEXT,
    
    total_tasks INT DEFAULT 0,
    completed_tasks INT DEFAULT 0,
    failed_tasks INT DEFAULT 0,
    
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    
    CONSTRAINT uk_fw_policy_name UNIQUE (policy_name)
);

CREATE INDEX IF NOT EXISTS idx_fw_policy_status ON firewall_policies(status);
CREATE INDEX IF NOT EXISTS idx_fw_policy_source_zone ON firewall_policies(source_zone);
CREATE INDEX IF NOT EXISTS idx_fw_policy_dest_zone ON firewall_policies(dest_zone);

DROP TRIGGER IF EXISTS update_firewall_policies_updated_at ON firewall_policies;
CREATE TRIGGER update_firewall_policies_updated_at
    BEFORE UPDATE ON firewall_policies
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE firewall_policies IS 'AZ层防火墙策略表';
