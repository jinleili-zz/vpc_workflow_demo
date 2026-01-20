#!/bin/bash

# MySQL初始化脚本 - 创建各个AZ的数据库和DTM数据库

echo "Creating databases for each AZ and DTM..."

mysql -uroot -p${MYSQL_ROOT_PASSWORD} <<-EOSQL
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1a CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1b CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_shanghai_1a CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS dtm_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1a.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1b.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_shanghai_1a.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON dtm_db.* TO 'nsp_user'@'%';
    
    FLUSH PRIVILEGES;
EOSQL

# 创建DTM所需的表
echo "Creating DTM tables in dtm_db..."
mysql -uroot -p${MYSQL_ROOT_PASSWORD} dtm_db <<-EOSQL
    CREATE TABLE IF NOT EXISTS trans_global (
      gid VARCHAR(128) NOT NULL,
      trans_type VARCHAR(45) NOT NULL,
      status VARCHAR(12) NOT NULL,
      query_prepared VARCHAR(2048) NOT NULL DEFAULT '',
      protocol VARCHAR(45) NOT NULL DEFAULT '',
      create_time DATETIME DEFAULT CURRENT_TIMESTAMP,
      update_time DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      finish_time DATETIME DEFAULT NULL,
      rollback_time DATETIME DEFAULT NULL,
      options VARCHAR(1024) NOT NULL DEFAULT '',
      custom_data VARCHAR(1024) NOT NULL DEFAULT '',
      next_cron_interval INT DEFAULT -1,
      next_cron_time DATETIME DEFAULT NULL,
      owner VARCHAR(128) NOT NULL DEFAULT '',
      ext_data TEXT,
      result VARCHAR(1024) NOT NULL DEFAULT '',
      rollback_reason VARCHAR(1024) NOT NULL DEFAULT '',
      PRIMARY KEY (gid),
      KEY idx_owner (owner),
      KEY idx_status_next_cron_time (status, next_cron_time)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS trans_branch (
      id INT AUTO_INCREMENT PRIMARY KEY,
      gid VARCHAR(128) NOT NULL,
      url VARCHAR(2048) NOT NULL,
      data TEXT,
      branch_id VARCHAR(128) NOT NULL,
      op VARCHAR(45) NOT NULL,
      status VARCHAR(45) NOT NULL,
      finish_time DATETIME DEFAULT NULL,
      rollback_time DATETIME DEFAULT NULL,
      create_time DATETIME DEFAULT CURRENT_TIMESTAMP,
      update_time DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      KEY idx_gid (gid)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

    CREATE TABLE IF NOT EXISTS kv (
      id INT AUTO_INCREMENT PRIMARY KEY,
      cat VARCHAR(45) NOT NULL,
      k VARCHAR(128) NOT NULL,
      v TEXT,
      create_time DATETIME DEFAULT CURRENT_TIMESTAMP,
      update_time DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      UNIQUE KEY idx_cat_k (cat, k)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
EOSQL

# 为每个AZ数据库创建DTM Barrier表
for db in nsp_cn_beijing_1a nsp_cn_beijing_1b nsp_cn_shanghai_1a; do
    echo "Creating DTM Barrier table in ${db}..."
    mysql -uroot -p${MYSQL_ROOT_PASSWORD} ${db} <<-EOSQL
        CREATE TABLE IF NOT EXISTS dtm_barrier (
            trans_type VARCHAR(45) NOT NULL,
            gid VARCHAR(128) NOT NULL,
            branch_id VARCHAR(128) NOT NULL,
            op VARCHAR(45) NOT NULL,
            barrier_id VARCHAR(128) NOT NULL,
            reason VARCHAR(255),
            create_time DATETIME DEFAULT CURRENT_TIMESTAMP,
            update_time DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
            PRIMARY KEY (trans_type, gid, branch_id, op)
        ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
        
        CREATE UNIQUE INDEX IF NOT EXISTS idx_barrier_id ON dtm_barrier (barrier_id);
EOSQL
done

echo "Databases, DTM tables, and DTM Barrier tables created successfully!"
