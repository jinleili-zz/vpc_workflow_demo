#!/usr/bin/env bash
set -euo pipefail

: "${NSP_TEST_POSTGRES_DSN:?NSP_TEST_POSTGRES_DSN is required}"
: "${NSP_TEST_REDIS_ADDR:?NSP_TEST_REDIS_ADDR is required}"

go test \
    ./internal/operation \
    ./internal/db/dao \
    ./internal/az/api \
    ./internal/az/vfw/api \
    -count=1
