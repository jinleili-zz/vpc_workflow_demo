#!/bin/bash
set -e

# Create all databases needed by NSP services
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE top_nsp_vpc;
    CREATE DATABASE top_nsp_vfw;
    CREATE DATABASE nsp_cn_beijing_1a_vpc;
    CREATE DATABASE nsp_cn_beijing_1a_vfw;
    CREATE DATABASE nsp_cn_beijing_1b_vpc;
    CREATE DATABASE nsp_cn_beijing_1b_vfw;
    CREATE DATABASE nsp_cn_shanghai_1a_vpc;
    CREATE DATABASE nsp_cn_shanghai_1a_vfw;
EOSQL

# Run SAGA migrations on all databases
SAGA_FILE="/migrations/saga.sql"
MIGRATION_FILE="/migrations/001_init_postgresql.sql"

for DB in top_nsp_vpc top_nsp_vfw nsp_cn_beijing_1a_vpc nsp_cn_beijing_1a_vfw nsp_cn_beijing_1b_vpc nsp_cn_beijing_1b_vfw nsp_cn_shanghai_1a_vpc nsp_cn_shanghai_1a_vfw; do
    echo "Running migrations on database: $DB"
    if [ -f "$SAGA_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$SAGA_FILE" || true
    fi
    if [ -f "$MIGRATION_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$MIGRATION_FILE" || true
    fi
done

echo "All databases initialized successfully."
