-- File: migrations/saga.sql
-- SAGA 分布式事务模块数据库建表脚本

-- ============================================================================
-- 表一：saga_transactions（全局事务表）
-- ============================================================================
CREATE TABLE IF NOT EXISTS saga_transactions (
    id              VARCHAR(64)  PRIMARY KEY,
    status          VARCHAR(20)  NOT NULL,
    payload         JSONB,
    current_step    INT          NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    finished_at     TIMESTAMPTZ,
    timeout_at      TIMESTAMPTZ,
    retry_count     INT          NOT NULL DEFAULT 0,
    last_error      TEXT
);

-- status 合法值: pending / running / compensating / succeeded / failed

COMMENT ON TABLE saga_transactions IS 'SAGA 全局事务表';
COMMENT ON COLUMN saga_transactions.id IS '全局事务 ID (UUID)';
COMMENT ON COLUMN saga_transactions.status IS '事务状态: pending/running/compensating/succeeded/failed';
COMMENT ON COLUMN saga_transactions.payload IS '事务全局负载数据 (JSON)';
COMMENT ON COLUMN saga_transactions.current_step IS '当前执行到的步骤索引';
COMMENT ON COLUMN saga_transactions.created_at IS '创建时间';
COMMENT ON COLUMN saga_transactions.updated_at IS '最后更新时间';
COMMENT ON COLUMN saga_transactions.finished_at IS '完成时间';
COMMENT ON COLUMN saga_transactions.timeout_at IS '超时时间';
COMMENT ON COLUMN saga_transactions.retry_count IS '重试次数';
COMMENT ON COLUMN saga_transactions.last_error IS '最后一次错误信息';

CREATE INDEX IF NOT EXISTS idx_saga_tx_status ON saga_transactions(status);
CREATE INDEX IF NOT EXISTS idx_saga_tx_timeout ON saga_transactions(timeout_at)
    WHERE status IN ('running', 'compensating');

-- ============================================================================
-- 表二：saga_steps（子事务步骤表）
-- ============================================================================
CREATE TABLE IF NOT EXISTS saga_steps (
    id                  VARCHAR(64)  PRIMARY KEY,
    transaction_id      VARCHAR(64)  NOT NULL REFERENCES saga_transactions(id) ON DELETE CASCADE,
    step_index          INT          NOT NULL,
    name                VARCHAR(128) NOT NULL,
    step_type           VARCHAR(20)  NOT NULL,       -- sync / async
    status              VARCHAR(20)  NOT NULL,

    -- 正向操作
    action_method       VARCHAR(10)  NOT NULL,
    action_url          TEXT         NOT NULL,
    action_payload      JSONB,
    action_response     JSONB,

    -- 补偿操作
    compensate_method   VARCHAR(10)  NOT NULL,
    compensate_url      TEXT         NOT NULL,
    compensate_payload  JSONB,

    -- 轮询配置 (仅 async 类型步骤使用)
    poll_url            TEXT,
    poll_method         VARCHAR(10)  DEFAULT 'GET',
    poll_interval_sec   INT          DEFAULT 5,
    poll_max_times      INT          DEFAULT 60,
    poll_count          INT          NOT NULL DEFAULT 0,
    poll_success_path   TEXT,
    poll_success_value  TEXT,
    poll_failure_path   TEXT,
    poll_failure_value  TEXT,
    next_poll_at        TIMESTAMPTZ,

    -- 重试与错误
    retry_count         INT          NOT NULL DEFAULT 0,
    max_retry           INT          NOT NULL DEFAULT 3,
    last_error          TEXT,
    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ,

    UNIQUE (transaction_id, step_index)
);

-- status 合法值: pending / running / polling / succeeded / failed / compensating / compensated / skipped

COMMENT ON TABLE saga_steps IS 'SAGA 子事务步骤表';
COMMENT ON COLUMN saga_steps.id IS '步骤 ID (UUID)';
COMMENT ON COLUMN saga_steps.transaction_id IS '所属事务 ID';
COMMENT ON COLUMN saga_steps.step_index IS '步骤索引（从 0 开始）';
COMMENT ON COLUMN saga_steps.name IS '步骤名称';
COMMENT ON COLUMN saga_steps.step_type IS '步骤类型: sync/async';
COMMENT ON COLUMN saga_steps.status IS '步骤状态: pending/running/polling/succeeded/failed/compensating/compensated/skipped';
COMMENT ON COLUMN saga_steps.action_method IS '正向操作 HTTP 方法';
COMMENT ON COLUMN saga_steps.action_url IS '正向操作 URL';
COMMENT ON COLUMN saga_steps.action_payload IS '正向操作请求体';
COMMENT ON COLUMN saga_steps.action_response IS '正向操作响应体';
COMMENT ON COLUMN saga_steps.compensate_method IS '补偿操作 HTTP 方法';
COMMENT ON COLUMN saga_steps.compensate_url IS '补偿操作 URL';
COMMENT ON COLUMN saga_steps.compensate_payload IS '补偿操作请求体';
COMMENT ON COLUMN saga_steps.poll_url IS '轮询 URL（async 步骤使用）';
COMMENT ON COLUMN saga_steps.poll_method IS '轮询 HTTP 方法，默认 GET';
COMMENT ON COLUMN saga_steps.poll_interval_sec IS '轮询间隔秒数';
COMMENT ON COLUMN saga_steps.poll_max_times IS '最大轮询次数';
COMMENT ON COLUMN saga_steps.poll_count IS '当前轮询次数';
COMMENT ON COLUMN saga_steps.poll_success_path IS '轮询成功判断的 JSONPath';
COMMENT ON COLUMN saga_steps.poll_success_value IS '轮询成功的期望值';
COMMENT ON COLUMN saga_steps.poll_failure_path IS '轮询失败判断的 JSONPath';
COMMENT ON COLUMN saga_steps.poll_failure_value IS '轮询失败的期望值';
COMMENT ON COLUMN saga_steps.next_poll_at IS '下次轮询时间';
COMMENT ON COLUMN saga_steps.retry_count IS '当前重试次数';
COMMENT ON COLUMN saga_steps.max_retry IS '最大重试次数';
COMMENT ON COLUMN saga_steps.last_error IS '最后一次错误信息';
COMMENT ON COLUMN saga_steps.started_at IS '开始执行时间';
COMMENT ON COLUMN saga_steps.finished_at IS '执行完成时间';

CREATE INDEX IF NOT EXISTS idx_saga_steps_tx ON saga_steps(transaction_id, step_index);
CREATE INDEX IF NOT EXISTS idx_saga_steps_poll ON saga_steps(next_poll_at)
    WHERE status = 'polling';

-- ============================================================================
-- 表三：saga_poll_tasks（轮询任务表）
-- ============================================================================
CREATE TABLE IF NOT EXISTS saga_poll_tasks (
    id              BIGSERIAL    PRIMARY KEY,
    step_id         VARCHAR(64)  NOT NULL REFERENCES saga_steps(id) ON DELETE CASCADE,
    transaction_id  VARCHAR(64)  NOT NULL,
    next_poll_at    TIMESTAMPTZ  NOT NULL,
    locked_until    TIMESTAMPTZ,
    locked_by       VARCHAR(64),
    UNIQUE (step_id)
);

COMMENT ON TABLE saga_poll_tasks IS 'SAGA 轮询任务表';
COMMENT ON COLUMN saga_poll_tasks.id IS '自增主键';
COMMENT ON COLUMN saga_poll_tasks.step_id IS '关联的步骤 ID';
COMMENT ON COLUMN saga_poll_tasks.transaction_id IS '关联的事务 ID';
COMMENT ON COLUMN saga_poll_tasks.next_poll_at IS '下次轮询时间';
COMMENT ON COLUMN saga_poll_tasks.locked_until IS '锁定截止时间（分布式锁）';
COMMENT ON COLUMN saga_poll_tasks.locked_by IS '锁定者实例 ID';

CREATE INDEX IF NOT EXISTS idx_poll_tasks_next ON saga_poll_tasks(next_poll_at)
    WHERE locked_until IS NULL OR locked_until < NOW();
