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

ALTER TABLE orchestration_operations
    ADD COLUMN IF NOT EXISTS lease_owner VARCHAR(128),
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_operations_dispatch_lease
    ON orchestration_operations (owner_service, lease_until, updated_at)
    WHERE status IN ('accepted', 'dispatching') AND response_payload IS NULL;

CREATE TABLE IF NOT EXISTS orchestration_idempotency_aliases (
    owner_service VARCHAR(64) NOT NULL,
    caller_scope VARCHAR(128) NOT NULL,
    route_scope VARCHAR(256) NOT NULL,
    idempotency_key VARCHAR(256) NOT NULL,
    request_hash_version SMALLINT NOT NULL,
    request_hash CHAR(64) NOT NULL,
    operation_id VARCHAR(64) NOT NULL REFERENCES orchestration_operations(operation_id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (owner_service, caller_scope, route_scope, idempotency_key)
);

CREATE TABLE IF NOT EXISTS orchestration_target_claims (
    owner_service VARCHAR(64) NOT NULL,
    resource_type VARCHAR(32) NOT NULL,
    target_scope VARCHAR(256) NOT NULL,
    request_hash CHAR(64) NOT NULL,
    operation_id VARCHAR(64) NOT NULL REFERENCES orchestration_operations(operation_id),
    resource_id VARCHAR(64) NOT NULL,
    generation BIGINT NOT NULL DEFAULT 1,
    active BOOLEAN NOT NULL DEFAULT TRUE,
	retiring BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (owner_service, resource_type, target_scope)
);

ALTER TABLE orchestration_target_claims
    ADD COLUMN IF NOT EXISTS retiring BOOLEAN NOT NULL DEFAULT FALSE;

-- Upgrade safety: claim targets already represented by Operation rows before
-- this table was introduced. DISTINCT ON selects the newest live generation.
INSERT INTO orchestration_target_claims (
    owner_service, resource_type, target_scope, request_hash,
    operation_id, resource_id, generation, active
)
SELECT DISTINCT ON (owner_service, resource_type, target_scope)
    owner_service, resource_type, target_scope, request_hash,
    operation_id, resource_id, generation, TRUE
FROM orchestration_operations
WHERE resource_id IS NOT NULL
  AND owner_service NOT IN ('top-nsp-vpc', 'top-nsp-vfw')
  AND status NOT IN ('failed', 'cancelled', 'compensated', 'deleted', 'delete_failed')
ORDER BY owner_service, resource_type, target_scope, generation DESC, created_at DESC
ON CONFLICT (owner_service, resource_type, target_scope) DO NOTHING;

-- Resources created before Operation existed receive a conservative synthetic
-- claim. The sentinel hash intentionally conflicts with a new request: it is
-- safer to require explicit delete/recreate than to launch a duplicate create
-- against an already-present resource whose original request is unavailable.
DO $$
BEGIN
    IF to_regclass('public.vpc_registry') IS NOT NULL THEN
        INSERT INTO orchestration_operations (
            operation_id, root_operation_id, owner_service, caller_scope, route_scope,
            operation_type, target_scope, idempotency_key, request_hash_version,
            request_hash, request_payload, resource_type, resource_id, generation,
            status, response_code, completed_at
        )
        SELECT legacy_id, legacy_id, 'top-nsp-vpc', 'migration:legacy', 'POST /api/v1/vpc',
               'create_vpc', vpc_name, legacy_id, 1, legacy_hash,
               jsonb_build_object('legacy_resource', TRUE, 'vpc_name', vpc_name, 'region', region),
               'vpc', id, 1, 'succeeded', '0', NOW()
        FROM (
            SELECT v.*, 'legacy-vpc-' || md5(region || '/' || vpc_name) AS legacy_id,
                   md5('legacy-vpc:' || region || '/' || vpc_name) || md5('legacy-vpc-2:' || region || '/' || vpc_name) AS legacy_hash
            FROM vpc_registry v WHERE COALESCE(status, '') <> 'deleted'
        ) legacy
        ON CONFLICT DO NOTHING;

        INSERT INTO orchestration_target_claims (owner_service, resource_type, target_scope, request_hash, operation_id, resource_id, generation)
        SELECT 'top-nsp-vpc', 'vpc', vpc_name,
               md5('legacy-vpc:' || region || '/' || vpc_name) || md5('legacy-vpc-2:' || region || '/' || vpc_name),
               'legacy-vpc-' || md5(region || '/' || vpc_name), id, 1
        FROM vpc_registry WHERE COALESCE(status, '') <> 'deleted'
        ON CONFLICT DO NOTHING;
    END IF;

    IF to_regclass('public.subnet_registry') IS NOT NULL THEN
        INSERT INTO orchestration_operations (
            operation_id, root_operation_id, owner_service, caller_scope, route_scope,
            operation_type, target_scope, idempotency_key, request_hash_version,
            request_hash, request_payload, resource_type, resource_id, generation,
            status, response_code, completed_at
        )
        SELECT legacy_id, legacy_id, 'top-nsp-vpc', 'migration:legacy', 'POST /api/v1/subnet',
               'create_subnet', az || '/' || subnet_name, legacy_id, 1, legacy_hash,
               jsonb_build_object('legacy_resource', TRUE, 'subnet_name', subnet_name, 'region', region, 'az', az),
               'subnet', id, 1, 'succeeded', '0', NOW()
        FROM (
            SELECT s.*, 'legacy-subnet-' || md5(region || '/' || az || '/' || subnet_name) AS legacy_id,
                   md5('legacy-subnet:' || region || '/' || az || '/' || subnet_name) || md5('legacy-subnet-2:' || region || '/' || az || '/' || subnet_name) AS legacy_hash
            FROM subnet_registry s WHERE COALESCE(status, '') <> 'deleted'
        ) legacy
        ON CONFLICT DO NOTHING;

        INSERT INTO orchestration_target_claims (owner_service, resource_type, target_scope, request_hash, operation_id, resource_id, generation)
        SELECT 'top-nsp-vpc', 'subnet', az || '/' || subnet_name,
               md5('legacy-subnet:' || region || '/' || az || '/' || subnet_name) || md5('legacy-subnet-2:' || region || '/' || az || '/' || subnet_name),
               'legacy-subnet-' || md5(region || '/' || az || '/' || subnet_name), id, 1
        FROM subnet_registry WHERE COALESCE(status, '') <> 'deleted'
        ON CONFLICT DO NOTHING;
    END IF;

    IF to_regclass('public.pccn_registry') IS NOT NULL THEN
        INSERT INTO orchestration_operations (
            operation_id, root_operation_id, owner_service, caller_scope, route_scope,
            operation_type, target_scope, idempotency_key, request_hash_version,
            request_hash, request_payload, resource_type, resource_id, generation,
            status, response_code, completed_at
        )
        SELECT legacy_id, legacy_id, 'top-nsp-vpc', 'migration:legacy', 'POST /api/v1/pccn',
               'create_pccn', pccn_name, legacy_id, 1, legacy_hash,
               jsonb_build_object('legacy_resource', TRUE, 'pccn_name', pccn_name),
               'pccn', id, 1, 'succeeded', '0', NOW()
        FROM (
            SELECT p.*,
                   'legacy-pccn-' || md5(vpc1_region || '/' || vpc1_name || '/' || vpc2_region || '/' || vpc2_name || '/' || pccn_name) AS legacy_id,
                   md5('legacy-pccn:' || vpc1_region || '/' || vpc1_name || '/' || vpc2_region || '/' || vpc2_name || '/' || pccn_name) || md5('legacy-pccn-2:' || vpc1_region || '/' || vpc1_name || '/' || vpc2_region || '/' || vpc2_name || '/' || pccn_name) AS legacy_hash
            FROM pccn_registry p WHERE COALESCE(status, '') <> 'deleted'
        ) legacy
        ON CONFLICT DO NOTHING;

        INSERT INTO orchestration_target_claims (owner_service, resource_type, target_scope, request_hash, operation_id, resource_id, generation)
        SELECT 'top-nsp-vpc', 'pccn', pccn_name, legacy_hash, legacy_id, id, 1
        FROM (
            SELECT p.*,
                   'legacy-pccn-' || md5(vpc1_region || '/' || vpc1_name || '/' || vpc2_region || '/' || vpc2_name || '/' || pccn_name) AS legacy_id,
                   md5('legacy-pccn:' || vpc1_region || '/' || vpc1_name || '/' || vpc2_region || '/' || vpc2_name || '/' || pccn_name) || md5('legacy-pccn-2:' || vpc1_region || '/' || vpc1_name || '/' || vpc2_region || '/' || vpc2_name || '/' || pccn_name) AS legacy_hash
            FROM pccn_registry p WHERE COALESCE(status, '') <> 'deleted'
        ) legacy
        ON CONFLICT DO NOTHING;
    END IF;

    IF to_regclass('public.policy_registry') IS NOT NULL THEN
        INSERT INTO orchestration_operations (
            operation_id, root_operation_id, owner_service, caller_scope, route_scope,
            operation_type, target_scope, idempotency_key, request_hash_version,
            request_hash, request_payload, resource_type, resource_id, generation,
            status, response_code, completed_at
        )
        SELECT legacy_id, legacy_id, 'top-nsp-vfw', 'migration:legacy', 'POST /api/v1/firewall/policy',
               'apply_firewall_policy', policy_name, legacy_id, 1, legacy_hash,
               jsonb_build_object('legacy_resource', TRUE, 'policy_name', policy_name),
               'firewall_policy', id, 1, 'succeeded', '0', NOW()
        FROM (
            SELECT p.*, 'legacy-policy-' || md5(policy_name) AS legacy_id,
                   md5('legacy-policy:' || policy_name) || md5('legacy-policy-2:' || policy_name) AS legacy_hash
            FROM policy_registry p WHERE COALESCE(status, '') <> 'deleted'
        ) legacy
        ON CONFLICT DO NOTHING;

        INSERT INTO orchestration_target_claims (owner_service, resource_type, target_scope, request_hash, operation_id, resource_id, generation)
        SELECT 'top-nsp-vfw', 'firewall_policy', policy_name,
               md5('legacy-policy:' || policy_name) || md5('legacy-policy-2:' || policy_name),
               'legacy-policy-' || md5(policy_name), id, 1
        FROM policy_registry WHERE COALESCE(status, '') <> 'deleted'
        ON CONFLICT DO NOTHING;
    END IF;
END $$;

-- Top VFW child identity is Region + AZ. AZ labels are not globally unique.
DO $$
BEGIN
    IF to_regclass('public.policy_az_records') IS NOT NULL THEN
        ALTER TABLE policy_az_records ADD COLUMN IF NOT EXISTS region VARCHAR(64);
        UPDATE policy_az_records record
        SET region = CASE
            WHEN record.az = policy.source_az THEN COALESCE(policy.source_region, '')
            WHEN record.az = policy.dest_az THEN COALESCE(policy.dest_region, '')
            ELSE ''
        END
        FROM policy_registry policy
        WHERE record.policy_id = policy.id AND record.region IS NULL;
        UPDATE policy_az_records SET region = '' WHERE region IS NULL;
        ALTER TABLE policy_az_records ALTER COLUMN region SET NOT NULL;
        ALTER TABLE policy_az_records DROP CONSTRAINT IF EXISTS uk_policy_az;
        IF NOT EXISTS (
            SELECT 1 FROM pg_constraint
            WHERE conrelid = 'policy_az_records'::regclass
              AND conname = 'uk_policy_region_az'
        ) THEN
            ALTER TABLE policy_az_records
                ADD CONSTRAINT uk_policy_region_az UNIQUE (policy_id, region, az);
        END IF;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS operation_az_executions (
    operation_id VARCHAR(64) NOT NULL REFERENCES orchestration_operations(operation_id) ON DELETE CASCADE,
    region VARCHAR(64) NOT NULL,
    az VARCHAR(64) NOT NULL,
    child_operation_id VARCHAR(64),
    saga_transaction_id VARCHAR(128),
    status VARCHAR(32) NOT NULL,
    error_code VARCHAR(64),
    error_message TEXT,
	lease_owner VARCHAR(128),
	lease_until TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (operation_id, region, az)
);

ALTER TABLE operation_az_executions
    ADD COLUMN IF NOT EXISTS lease_owner VARCHAR(128),
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_operation_az_reconcile
    ON operation_az_executions (status, updated_at)
    WHERE status IN ('accepted', 'dispatching', 'running', 'compensating');

CREATE INDEX IF NOT EXISTS idx_operation_az_lease
    ON operation_az_executions (lease_until, updated_at)
    WHERE status IN ('accepted', 'dispatching', 'running', 'compensating');
