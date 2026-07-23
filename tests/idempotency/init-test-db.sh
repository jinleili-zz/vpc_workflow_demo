#!/bin/bash
set -e

# 幂等测试数据库：一个 Top 库 + 一个 AZ 库（单 AZ 单实例）
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE top_nsp_vpc;
    CREATE DATABASE nsp_test_az1_vpc;
EOSQL

for DB in top_nsp_vpc nsp_test_az1_vpc; do
    echo "Running migrations on database: $DB"
    psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f /migrations/saga.sql || true
    psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f /migrations/001_init_postgresql.sql || true
    psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f /migrations/004_create_pccn_tables.sql || true
    psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f /migrations/005_create_orchestration_operations.sql || true
    psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f /migrations/006_add_tasks_resource_order_unique.sql || true
    # saga.sql 的 ALTER 可能静默失败，确保 lease 列存在
    psql -v ON_ERROR_STOP=0 --username "$POSTGRES_USER" --dbname "$DB" -c "ALTER TABLE saga_transactions ADD COLUMN IF NOT EXISTS locked_by VARCHAR(128), ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;" 2>/dev/null || true
done

echo "Idempotency test databases initialized."
