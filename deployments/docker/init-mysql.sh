#!/bin/bash

# MySQL初始化脚本 - 创建各个AZ的数据库

echo "Creating databases for each AZ..."

mysql -uroot -p${MYSQL_ROOT_PASSWORD} <<-EOSQL
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1a CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_beijing_1b CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    CREATE DATABASE IF NOT EXISTS nsp_cn_shanghai_1a CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
    
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1a.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_beijing_1b.* TO 'nsp_user'@'%';
    GRANT ALL PRIVILEGES ON nsp_cn_shanghai_1a.* TO 'nsp_user'@'%';
    
    FLUSH PRIVILEGES;
EOSQL

echo "Databases created successfully!"
