-- Migration: Add saga_tx_id column to vpc_registry table
-- This column links VPC records to their SAGA transaction for status tracking

-- Add saga_tx_id column to vpc_registry table
ALTER TABLE vpc_registry ADD COLUMN IF NOT EXISTS saga_tx_id VARCHAR(64) DEFAULT '';

-- Create index for efficient lookup by saga_tx_id
CREATE INDEX IF NOT EXISTS idx_vpc_registry_saga_tx_id ON vpc_registry(saga_tx_id);

COMMENT ON COLUMN vpc_registry.saga_tx_id IS 'SAGA transaction ID for tracking VPC creation status';
