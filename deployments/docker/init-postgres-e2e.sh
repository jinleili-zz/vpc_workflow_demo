#!/bin/bash
set -e

# Create databases needed by E2E services
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE top_nsp_vpc;
    CREATE DATABASE top_nsp_vfw;
    CREATE DATABASE nsp_cn_beijing_1a_vpc;
    CREATE DATABASE nsp_cn_beijing_1a_vfw;
    CREATE DATABASE nsp_demo;
EOSQL

# Create the functional test user
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE USER nsp WITH PASSWORD 'nsptest123';
    GRANT ALL PRIVILEGES ON DATABASE nsp_demo TO nsp;
EOSQL

# Grant schema permissions for functional test user
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "nsp_demo" <<-EOSQL
    GRANT ALL ON SCHEMA public TO nsp;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO nsp;
EOSQL

# Run migrations on all databases that need them
MIGRATION_FILE="/migrations/001_init_postgresql.sql"
SAGA_FILE="/migrations/saga.sql"

for DB in top_nsp_vpc top_nsp_vfw nsp_cn_beijing_1a_vpc nsp_cn_beijing_1a_vfw nsp_demo; do
    echo "Running migrations on database: $DB"
    if [ -f "$SAGA_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$SAGA_FILE" || true
    fi
    if [ -f "$MIGRATION_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$MIGRATION_FILE" || true
    fi
done

echo "All databases initialized successfully."
