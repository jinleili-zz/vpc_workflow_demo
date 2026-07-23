-- Durable task identity plus transactional Outbox/Inbox.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'tasks'
          AND column_name = 'status'
          AND data_type = 'USER-DEFINED'
    ) THEN
        ALTER TABLE tasks ALTER COLUMN status DROP DEFAULT;
        ALTER TABLE tasks ALTER COLUMN status TYPE VARCHAR(24) USING status::text;
        ALTER TABLE tasks ALTER COLUMN status SET DEFAULT 'pending';
    END IF;
END $$;

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS operation_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS root_operation_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS workflow_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS step_name VARCHAR(128),
    ADD COLUMN IF NOT EXISTS attempt INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_event_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS protocol_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS operation_required BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS destination VARCHAR(256),
    ADD COLUMN IF NOT EXISTS reply_queue VARCHAR(256);

UPDATE tasks
SET operation_id = COALESCE(NULLIF(operation_id, ''), resource_id),
    root_operation_id = COALESCE(NULLIF(root_operation_id, ''), resource_id),
    workflow_id = COALESCE(NULLIF(workflow_id, ''), resource_id),
    step_name = COALESCE(NULLIF(step_name, ''), task_type)
WHERE operation_id IS NULL OR operation_id = ''
   OR root_operation_id IS NULL OR root_operation_id = ''
   OR workflow_id IS NULL OR workflow_id = ''
   OR step_name IS NULL OR step_name = '';

ALTER TABLE tasks
    ALTER COLUMN operation_id SET NOT NULL,
    ALTER COLUMN root_operation_id SET NOT NULL,
    ALTER COLUMN workflow_id SET NOT NULL,
    ALTER COLUMN step_name SET NOT NULL;

-- Legacy v1 rows are deliberately excluded: pre-v2 deployments did not have
-- a stable workflow identity and may contain legitimate replay duplicates.
DROP INDEX IF EXISTS uq_tasks_workflow_step;
CREATE UNIQUE INDEX uq_tasks_workflow_step
    ON tasks (workflow_id, generation, step_name, task_order)
    WHERE protocol_version >= 2;

CREATE INDEX IF NOT EXISTS idx_tasks_recovery
    ON tasks (status, updated_at)
    WHERE status IN ('pending', 'queued', 'running', 'retrying');

ALTER TABLE vpc_resources
    ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS current_operation_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

ALTER TABLE subnet_resources
    ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS current_operation_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

ALTER TABLE pccn_resources
    ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS current_operation_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

ALTER TABLE firewall_policies
    ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS current_operation_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS outbox_events (
    event_id VARCHAR(64) PRIMARY KEY,
    event_key VARCHAR(512) NOT NULL UNIQUE,
    owner_service VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(64) NOT NULL,
    aggregate_id VARCHAR(128) NOT NULL,
    event_type VARCHAR(128) NOT NULL,
    destination VARCHAR(256) NOT NULL,
    payload JSONB NOT NULL,
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    publish_attempts INT NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_at TIMESTAMPTZ,
    locked_by VARCHAR(128),
    published_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_outbox_dispatch
    ON outbox_events (owner_service, available_at, created_at)
    WHERE status IN ('pending', 'retry');

CREATE INDEX IF NOT EXISTS idx_outbox_lease
    ON outbox_events (owner_service, locked_at)
    WHERE status = 'publishing';

CREATE TABLE IF NOT EXISTS inbox_events (
    consumer_name VARCHAR(128) NOT NULL,
    event_id VARCHAR(64) NOT NULL,
    payload_hash CHAR(64) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    result_code VARCHAR(64),
    PRIMARY KEY (consumer_name, event_id)
);
