-- Durable idempotency operations and Top per-AZ execution facts.

CREATE TABLE IF NOT EXISTS orchestration_operations (
    operation_id VARCHAR(64) PRIMARY KEY,
    root_operation_id VARCHAR(64) NOT NULL,
    parent_operation_id VARCHAR(64),
    owner_service VARCHAR(64) NOT NULL,
    caller_scope VARCHAR(128) NOT NULL,
    route_scope VARCHAR(256) NOT NULL,
    operation_type VARCHAR(64) NOT NULL,
    target_scope VARCHAR(256) NOT NULL,
    idempotency_key VARCHAR(256) NOT NULL,
    request_hash_version SMALLINT NOT NULL DEFAULT 1,
    request_hash CHAR(64) NOT NULL,
    request_payload JSONB NOT NULL,
    resource_type VARCHAR(32) NOT NULL,
    resource_id VARCHAR(64),
    generation BIGINT NOT NULL DEFAULT 1,
    status VARCHAR(32) NOT NULL,
    response_code VARCHAR(64),
    response_payload JSONB,
    error_code VARCHAR(64),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT uq_operation_idempotency
        UNIQUE (owner_service, caller_scope, route_scope, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_operations_reconcile
    ON orchestration_operations (owner_service, status, updated_at)
    WHERE status IN ('accepted', 'dispatching', 'running', 'compensating');

CREATE INDEX IF NOT EXISTS idx_operations_root
    ON orchestration_operations (root_operation_id, owner_service);

CREATE INDEX IF NOT EXISTS idx_operations_target
    ON orchestration_operations (owner_service, operation_type, target_scope, created_at);

CREATE TABLE IF NOT EXISTS operation_az_executions (
    operation_id VARCHAR(64) NOT NULL REFERENCES orchestration_operations(operation_id) ON DELETE CASCADE,
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    child_operation_id VARCHAR(64),
    saga_transaction_id VARCHAR(128),
    status VARCHAR(32) NOT NULL,
    error_code VARCHAR(64),
    error_message TEXT,
    version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (operation_id, region, az)
);

CREATE INDEX IF NOT EXISTS idx_operation_az_reconcile
    ON operation_az_executions (status, updated_at)
    WHERE status IN ('accepted', 'dispatching', 'running', 'compensating');
