-- Idempotent external submission keys for Top NSP Saga orchestration.

CREATE TABLE IF NOT EXISTS top_saga_submissions (
    external_key VARCHAR(256) PRIMARY KEY,
    operation_id VARCHAR(64) NOT NULL REFERENCES orchestration_operations(operation_id) ON DELETE CASCADE,
    definition_hash CHAR(64) NOT NULL,
    definition_payload JSONB,
    saga_transaction_id VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE top_saga_submissions
    ADD COLUMN IF NOT EXISTS definition_payload JSONB;

CREATE UNIQUE INDEX IF NOT EXISTS uq_saga_transactions_external_key
    ON saga_transactions ((payload->>'_external_key'))
    WHERE payload ? '_external_key';

CREATE INDEX IF NOT EXISTS idx_top_saga_submissions_operation
    ON top_saga_submissions (operation_id);
