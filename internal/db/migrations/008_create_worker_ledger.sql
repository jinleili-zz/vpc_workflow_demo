-- Worker effectively-once coordination and simulated ensure state.

CREATE TABLE IF NOT EXISTS worker_operations (
    operation_key VARCHAR(512) PRIMARY KEY,
    root_operation_id VARCHAR(64) NOT NULL,
    operation_id VARCHAR(64) NOT NULL,
    workflow_id VARCHAR(64) NOT NULL,
    task_id VARCHAR(64) NOT NULL,
    resource_id VARCHAR(64) NOT NULL,
    generation BIGINT NOT NULL,
    device_type VARCHAR(32) NOT NULL,
    target_key VARCHAR(256) NOT NULL,
    action VARCHAR(128) NOT NULL,
    desired_hash CHAR(64) NOT NULL,
    status VARCHAR(24) NOT NULL,
    result_payload JSONB,
    error_code VARCHAR(64),
    error_message TEXT,
    lease_owner VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_worker_operations_recovery
    ON worker_operations (status, lease_expires_at, updated_at)
    WHERE status = 'running';

-- Demo handlers have no real device SDK. This table is their queryable
-- simulated device state, allowing takeover to compare/ensure rather than
-- blindly repeat an external mutation.
CREATE TABLE IF NOT EXISTS worker_device_state (
    operation_key VARCHAR(512) PRIMARY KEY,
    device_type VARCHAR(32) NOT NULL,
    target_key VARCHAR(256) NOT NULL,
    desired_hash CHAR(64) NOT NULL,
    result_payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
