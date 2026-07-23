-- 005_create_orchestration_operations.sql
-- 幂等改造（设计文档 docs/architecture/idempotency-analysis-and-design.md 第 10.1 节）：
-- 统一 Operation 表。Top NSP 与 AZ NSP 各自数据库均创建本表，
-- 以 (owner_service, caller_scope, route_scope, idempotency_key) 作为并发线性化点。

CREATE TABLE IF NOT EXISTS orchestration_operations (
    operation_id        VARCHAR(64) PRIMARY KEY,
    root_operation_id   VARCHAR(64) NOT NULL,
    parent_operation_id VARCHAR(64),
    owner_service       VARCHAR(64) NOT NULL,
    caller_scope        VARCHAR(128) NOT NULL,
    route_scope         VARCHAR(256) NOT NULL,
    operation_type      VARCHAR(64) NOT NULL,
    idempotency_key     VARCHAR(256) NOT NULL,
    request_hash        CHAR(64) NOT NULL,
    request_payload     JSONB NOT NULL,
    status              VARCHAR(32) NOT NULL DEFAULT 'running',
    response_code       INT,
    response_payload    JSONB,
    error_code          VARCHAR(64),
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    CONSTRAINT uq_operation_idempotency
        UNIQUE (owner_service, caller_scope, route_scope, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_operations_reconcile
    ON orchestration_operations (owner_service, status, updated_at);

CREATE INDEX IF NOT EXISTS idx_operations_root
    ON orchestration_operations (root_operation_id, owner_service);
