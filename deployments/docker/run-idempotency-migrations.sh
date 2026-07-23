#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
compose_file=${1:-docker-compose.yml}

cd "$script_dir"

docker compose -f "$compose_file" exec -T postgres sh -ceu '
  databases=$(psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc \
    "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")
  for database in $databases; do
    has_nsp_schema=$(psql -U "$POSTGRES_USER" -d "$database" -Atc \
      "SELECT to_regclass('public.vpc_resources') IS NOT NULL
           OR to_regclass('public.vpc_registry') IS NOT NULL
           OR to_regclass('public.firewall_policies') IS NOT NULL
           OR to_regclass('public.policy_registry') IS NOT NULL")
    if [ "$has_nsp_schema" != "t" ]; then
      echo "skipping $database: no NSP schema"
      continue
    fi
    echo "applying operation migration to $database"
    psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$database" -f /migrations/005_create_operations.sql
    has_tasks=$(psql -U "$POSTGRES_USER" -d "$database" -Atc \
      "SELECT to_regclass('public.tasks') IS NOT NULL")
    if [ "$has_tasks" = "t" ]; then
      echo "applying AZ Outbox/Inbox migration to $database"
      psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$database" -f /migrations/006_create_outbox_inbox.sql
    else
      echo "skipping AZ Outbox/Inbox migration for $database: no tasks table"
    fi
  done
'
