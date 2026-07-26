# Idempotency rollout runbook

Apply the durable idempotency schema to existing PostgreSQL volumes before deploying v2 producers or consumers:

```bash
cd deployments/docker
./run-idempotency-migrations.sh
```

For the E2E compose file:

```bash
./run-idempotency-migrations.sh docker-compose.e2e.yml
```

The runner discovers every non-template database and skips databases without an NSP schema. It applies migration `005` to Top and AZ service databases, migrations `006` and `008` only where the AZ `tasks` table exists, and migration `007` only where both `saga_transactions` and the Top VPC `vpc_registry` table exist. It uses `ON_ERROR_STOP`; all four migrations are repeatable, and a failure stops the rollout before any application container should be upgraded.

The v1 workflow protocol is disabled by default. It may only be enabled temporarily with `NSP_WORKFLOW_V1_REPLY_ENABLED=true` while draining legacy queues. Keep the flag enabled for no longer than the broker's maximum retry and retention window, confirm that no v1 messages remain, then return it to `false` before enabling v2-only producers.

Do not roll a v2 producer back to a v1-only consumer. Before any Outbox rollback, stop dispatchers and reconcile all `pending`, `retry`, and `publishing` rows to prevent double publication.
