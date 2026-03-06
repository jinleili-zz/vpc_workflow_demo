#!/bin/bash

echo "Creating databases for Top NSP and AZ NSP..."

mysql -uroot -p${MYSQL_ROOT_PASSWORD} <<-EOSQL
    CREATE DATABASE IF NOT EXISTS top_nsp_vpc CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS top_nsp_vfw CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1a_vpc CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1a_vfw CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1b_vpc CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1b_vfw CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_shanghai_1a_vpc CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_shanghai_1a_vfw CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    
    GRANT ALL PRIVILEGES ON top_nsp_vpc.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON top_nsp_vfw.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1a_vpc.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1a_vfw.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1b_vpc.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1b_vfw.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_shanghai_1a_vpc.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_shanghai_1a_vfw.* TO 'nsp_user'@'%';
    
    FLUSH PRIVILEGES;
EOSQL

echo "Creating tables for top_nsp_vpc..."
mysql -uroot -p${MYSQL_ROOT_PASSWORD} top_nsp_vpc <<-EOSQL
    CREATE TABLE IF NOT EXISTS vpc_registry (
        id VARCHAR(36) PRIMARY KEY,
        vpc_name VARCHAR(255) NOT NULL,
        region VARCHAR(50) NOT NULL,
        az VARCHAR(50) NOT NULL,
        az_vpc_id VARCHAR(36) NOT NULL,
        vrf_name VARCHAR(255) NOT NULL,
        vlan_id INT NOT NULL,
        firewall_zone VARCHAR(255) NOT NULL,
        status VARCHAR(20) NOT NULL,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE KEY uk_vpc_az (vpc_name, az),
        INDEX idx_region (region),
        INDEX idx_zone (firewall_zone)
    );

    CREATE TABLE IF NOT EXISTS subnet_registry (
        id VARCHAR(36) PRIMARY KEY,
        subnet_name VARCHAR(255) NOT NULL,
        vpc_name VARCHAR(255) NOT NULL,
        region VARCHAR(50) NOT NULL,
        az VARCHAR(50) NOT NULL,
        az_subnet_id VARCHAR(36) NOT NULL,
        cidr VARCHAR(50) NOT NULL,
        firewall_zone VARCHAR(255) NOT NULL,
        status VARCHAR(20) NOT NULL,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE KEY uk_subnet_az (subnet_name, az),
        INDEX idx_vpc (vpc_name),
        INDEX idx_cidr (cidr),
        INDEX idx_zone (firewall_zone)
    );

    CREATE TABLE IF NOT EXISTS cidr_zone_mapping (
        id VARCHAR(36) PRIMARY KEY,
        cidr VARCHAR(50) NOT NULL,
        cidr_start BIGINT UNSIGNED NOT NULL,
        cidr_end BIGINT UNSIGNED NOT NULL,
        vpc_name VARCHAR(255) NOT NULL,
        subnet_name VARCHAR(255) NOT NULL,
        region VARCHAR(50) NOT NULL,
        az VARCHAR(50) NOT NULL,
        firewall_zone VARCHAR(255) NOT NULL,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        INDEX idx_cidr_range (cidr_start, cidr_end),
        INDEX idx_zone (firewall_zone)
    );
EOSQL

echo "Creating tables for top_nsp_vfw..."
mysql -uroot -p${MYSQL_ROOT_PASSWORD} top_nsp_vfw <<-EOSQL
    CREATE TABLE IF NOT EXISTS policy_registry (
        id VARCHAR(36) PRIMARY KEY,
        policy_name VARCHAR(255) NOT NULL UNIQUE,
        source_ip VARCHAR(255) NOT NULL,
        dest_ip VARCHAR(255) NOT NULL,
        source_port VARCHAR(50) NOT NULL,
        dest_port VARCHAR(50) NOT NULL,
        protocol VARCHAR(20) NOT NULL,
        action VARCHAR(20) NOT NULL,
        description TEXT,
        source_vpc VARCHAR(255),
        dest_vpc VARCHAR(255),
        source_zone VARCHAR(255),
        dest_zone VARCHAR(255),
        source_region VARCHAR(50),
        dest_region VARCHAR(50),
        source_az VARCHAR(50),
        dest_az VARCHAR(50),
        status VARCHAR(20) NOT NULL,
        error_message TEXT,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        INDEX idx_status (status),
        INDEX idx_zones (source_zone, dest_zone)
    );

    CREATE TABLE IF NOT EXISTS policy_az_records (
        id VARCHAR(36) PRIMARY KEY,
        policy_id VARCHAR(36) NOT NULL,
        az VARCHAR(50) NOT NULL,
        az_policy_id VARCHAR(36),
        status VARCHAR(20) NOT NULL,
        error_message TEXT,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
        UNIQUE KEY uk_policy_az (policy_id, az),
        INDEX idx_policy (policy_id)
    );
EOSQL

create_az_vpc_tables() {
    local db_name=$1
    echo "Creating tables for ${db_name}..."
    mysql -uroot -p${MYSQL_ROOT_PASSWORD} ${db_name} -e "
        CREATE TABLE IF NOT EXISTS vpc_resources (
            id VARCHAR(36) PRIMARY KEY,
            vpc_name VARCHAR(255) NOT NULL,
            region VARCHAR(50) NOT NULL,
            az VARCHAR(50) NOT NULL,
            vrf_name VARCHAR(255) NOT NULL,
            vlan_id INT NOT NULL,
            firewall_zone VARCHAR(255) NOT NULL,
            status VARCHAR(20) NOT NULL,
            error_message TEXT,
            total_tasks INT DEFAULT 0,
            completed_tasks INT DEFAULT 0,
            failed_tasks INT DEFAULT 0,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
            UNIQUE KEY uk_vpc_name_az (vpc_name, az),
            INDEX idx_status (status)
        );

        CREATE TABLE IF NOT EXISTS subnet_resources (
            id VARCHAR(36) PRIMARY KEY,
            subnet_name VARCHAR(255) NOT NULL,
            vpc_name VARCHAR(255) NOT NULL,
            region VARCHAR(50) NOT NULL,
            az VARCHAR(50) NOT NULL,
            cidr VARCHAR(50) NOT NULL,
            status VARCHAR(20) NOT NULL,
            error_message TEXT,
            total_tasks INT DEFAULT 0,
            completed_tasks INT DEFAULT 0,
            failed_tasks INT DEFAULT 0,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
            UNIQUE KEY uk_subnet_name_az (subnet_name, az),
            INDEX idx_vpc_name (vpc_name),
            INDEX idx_status (status)
        );

        CREATE TABLE IF NOT EXISTS tasks (
            id VARCHAR(36) PRIMARY KEY,
            resource_type VARCHAR(50) NOT NULL,
            resource_id VARCHAR(36) NOT NULL,
            task_type VARCHAR(100) NOT NULL,
            task_name VARCHAR(255) NOT NULL,
            task_order INT NOT NULL,
            task_params TEXT,
            status VARCHAR(20) NOT NULL,
            priority INT DEFAULT 3,
            device_type VARCHAR(50),
            asynq_task_id VARCHAR(100),
            result TEXT,
            error_message TEXT,
            retry_count INT DEFAULT 0,
            max_retries INT DEFAULT 3,
            az VARCHAR(50) NOT NULL,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            queued_at TIMESTAMP NULL,
            started_at TIMESTAMP NULL,
            completed_at TIMESTAMP NULL,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
            INDEX idx_resource (resource_id),
            INDEX idx_status (status),
            INDEX idx_task_type (task_type)
        );
    "
}

create_az_vfw_tables() {
    local db_name=$1
    echo "Creating tables for ${db_name}..."
    mysql -uroot -p${MYSQL_ROOT_PASSWORD} ${db_name} -e "
        CREATE TABLE IF NOT EXISTS firewall_policies (
            id VARCHAR(36) PRIMARY KEY,
            policy_name VARCHAR(255) NOT NULL,
            source_zone VARCHAR(255) NOT NULL,
            dest_zone VARCHAR(255) NOT NULL,
            source_ip VARCHAR(255) NOT NULL,
            dest_ip VARCHAR(255) NOT NULL,
            source_port VARCHAR(50) NOT NULL,
            dest_port VARCHAR(50) NOT NULL,
            protocol VARCHAR(20) NOT NULL,
            action VARCHAR(20) NOT NULL,
            description TEXT,
            status VARCHAR(20) NOT NULL,
            error_message TEXT,
            total_tasks INT DEFAULT 0,
            completed_tasks INT DEFAULT 0,
            failed_tasks INT DEFAULT 0,
            region VARCHAR(50) NOT NULL,
            az VARCHAR(50) NOT NULL,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
            INDEX idx_policy_name (policy_name),
            INDEX idx_status (status),
            INDEX idx_zones (source_zone, dest_zone)
        );

        CREATE TABLE IF NOT EXISTS tasks (
            id VARCHAR(36) PRIMARY KEY,
            resource_type VARCHAR(50) NOT NULL,
            resource_id VARCHAR(36) NOT NULL,
            task_type VARCHAR(100) NOT NULL,
            task_name VARCHAR(255) NOT NULL,
            task_order INT NOT NULL,
            task_params TEXT,
            status VARCHAR(20) NOT NULL,
            priority INT DEFAULT 3,
            device_type VARCHAR(50),
            asynq_task_id VARCHAR(100),
            result TEXT,
            error_message TEXT,
            retry_count INT DEFAULT 0,
            max_retries INT DEFAULT 3,
            az VARCHAR(50) NOT NULL,
            created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            queued_at TIMESTAMP NULL,
            started_at TIMESTAMP NULL,
            completed_at TIMESTAMP NULL,
            updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
            INDEX idx_resource (resource_id),
            INDEX idx_status (status),
            INDEX idx_task_type (task_type)
        );
    "
}

create_az_vpc_tables "nsp_cn_beijing_1a_vpc"
create_az_vpc_tables "nsp_cn_beijing_1b_vpc"
create_az_vpc_tables "nsp_cn_shanghai_1a_vpc"

create_az_vfw_tables "nsp_cn_beijing_1a_vfw"
create_az_vfw_tables "nsp_cn_beijing_1b_vfw"
create_az_vfw_tables "nsp_cn_shanghai_1a_vfw"

echo "All databases and tables created successfully!"