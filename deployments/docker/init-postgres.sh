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
PCCN_FILE="/migrations/004_create_pccn_tables.sql"
OPERATION_FILE="/migrations/005_create_operations.sql"
OUTBOX_INBOX_FILE="/migrations/006_create_outbox_inbox.sql"
TOP_SAGA_SUBMISSION_FILE="/migrations/007_create_top_saga_submissions.sql"
WORKER_LEDGER_FILE="/migrations/008_create_worker_ledger.sql"

for DB in top_nsp_vpc top_nsp_vfw nsp_cn_beijing_1a_vpc nsp_cn_beijing_1a_vfw nsp_cn_beijing_1b_vpc nsp_cn_beijing_1b_vfw nsp_cn_shanghai_1a_vpc nsp_cn_shanghai_1a_vfw; do
    echo "Running migrations on database: $DB"
    if [ -f "$SAGA_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$SAGA_FILE" || true
    fi
    if [ -f "$MIGRATION_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$MIGRATION_FILE"
    fi
    if [ -f "$PCCN_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$PCCN_FILE"
    fi
    if [ -f "$OPERATION_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$OPERATION_FILE"
    fi
    if [ -f "$OUTBOX_INBOX_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$OUTBOX_INBOX_FILE"
    fi
    if [ "$DB" = "top_nsp_vpc" ] && [ -f "$TOP_SAGA_SUBMISSION_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$TOP_SAGA_SUBMISSION_FILE"
    fi
    if [[ "$DB" == nsp_* ]] && [ -f "$WORKER_LEDGER_FILE" ]; then
        psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$DB" -f "$WORKER_LEDGER_FILE"
    fi
done

echo "All databases initialized successfully."
