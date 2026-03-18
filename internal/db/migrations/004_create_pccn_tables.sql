-- PostgreSQL Migration Script for PCCN (Private Cloud Connection Network)
-- Creates tables for cross-VPC network connectivity

-- =====================================================
-- PART 1: Add 'pccn' to resource_type enum
-- =====================================================

DO $$ BEGIN
    ALTER TYPE resource_type ADD VALUE 'pccn';
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- =====================================================
-- PART 2: AZ Level PCCN Resources Table
-- =====================================================

-- PCCN资源表 (AZ层) - 每个AZ一条记录
CREATE TABLE IF NOT EXISTS pccn_resources (
    id VARCHAR(64) PRIMARY KEY,
    pccn_name VARCHAR(128) NOT NULL,
    vpc_name VARCHAR(128) NOT NULL,
    vpc_region VARCHAR(64) NOT NULL,
    peer_vpc_name VARCHAR(128) NOT NULL,
    peer_vpc_region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,

    status resource_status NOT NULL DEFAULT 'pending',
    subnets JSONB DEFAULT '[]',
    error_message TEXT,

    total_tasks INT DEFAULT 0,
    completed_tasks INT DEFAULT 0,
    failed_tasks INT DEFAULT 0,

    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),

    CONSTRAINT uk_pccn_name_az UNIQUE (pccn_name, az)
);

CREATE INDEX IF NOT EXISTS idx_pccn_resources_status ON pccn_resources(status);
CREATE INDEX IF NOT EXISTS idx_pccn_resources_az ON pccn_resources(az);
CREATE INDEX IF NOT EXISTS idx_pccn_resources_vpc ON pccn_resources(vpc_name, vpc_region);
CREATE INDEX IF NOT EXISTS idx_pccn_resources_peer_vpc ON pccn_resources(peer_vpc_name, peer_vpc_region);

DROP TRIGGER IF EXISTS update_pccn_resources_updated_at ON pccn_resources;
CREATE TRIGGER update_pccn_resources_updated_at
    BEFORE UPDATE ON pccn_resources
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE pccn_resources IS 'PCCN资源表 (AZ层) - 存储PCCN在各AZ的执行状态';
COMMENT ON COLUMN pccn_resources.id IS 'PCCN ID (由Top层生成)';
COMMENT ON COLUMN pccn_resources.pccn_name IS 'PCCN名称';
COMMENT ON COLUMN pccn_resources.vpc_name IS '本地VPC名称';
COMMENT ON COLUMN pccn_resources.vpc_region IS '本地VPC所属Region';
COMMENT ON COLUMN pccn_resources.peer_vpc_name IS '对端VPC名称';
COMMENT ON COLUMN pccn_resources.peer_vpc_region IS '对端VPC所属Region';
COMMENT ON COLUMN pccn_resources.subnets IS '本地VPC子网CIDR列表 (JSONB数组)';

-- =====================================================
-- PART 3: Top Level PCCN Registry Table
-- =====================================================

-- PCCN注册表 (Top层) - 一个PCCN一条记录，per-VPC详情存于vpc_details JSONB
CREATE TABLE IF NOT EXISTS pccn_registry (
    id VARCHAR(64) PRIMARY KEY,
    pccn_name VARCHAR(128) NOT NULL UNIQUE,

    vpc1_name VARCHAR(128) NOT NULL,
    vpc1_region VARCHAR(64) NOT NULL,
    vpc2_name VARCHAR(128) NOT NULL,
    vpc2_region VARCHAR(64) NOT NULL,

    status VARCHAR(32) DEFAULT 'creating',
    saga_tx_id VARCHAR(64) DEFAULT '',
    vpc_details JSONB DEFAULT '{}',

    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pccn_registry_status ON pccn_registry(status);
CREATE INDEX IF NOT EXISTS idx_pccn_registry_vpc1 ON pccn_registry(vpc1_name, vpc1_region);
CREATE INDEX IF NOT EXISTS idx_pccn_registry_vpc2 ON pccn_registry(vpc2_name, vpc2_region);
CREATE INDEX IF NOT EXISTS idx_pccn_registry_saga_tx_id ON pccn_registry(saga_tx_id);

DROP TRIGGER IF EXISTS update_pccn_registry_updated_at ON pccn_registry;
CREATE TRIGGER update_pccn_registry_updated_at
    BEFORE UPDATE ON pccn_registry
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE pccn_registry IS 'PCCN注册表 (Top层) - 存储PCCN全局拓扑信息';
COMMENT ON COLUMN pccn_registry.id IS 'PCCN ID';
COMMENT ON COLUMN pccn_registry.pccn_name IS 'PCCN名称 (全局唯一)';
COMMENT ON COLUMN pccn_registry.vpc_details IS 'Per-VPC详情 (JSONB), key格式: "{region}/{vpc_name}"';
