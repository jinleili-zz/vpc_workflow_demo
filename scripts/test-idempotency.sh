#!/bin/bash
# 幂等业务测试一键运行脚本。
# 流程：docker 启动单实例 PostgreSQL + Redis -> 构建并启动单实例
# top-nsp-vpc / az-nsp-vpc / worker(switch,firewall) -> 运行 tests/idempotency。
#
# 用法: scripts/test-idempotency.sh [--keep-deps]
#   --keep-deps  测试结束后保留 docker 依赖容器（默认也会保留，方便复跑；
#                使用 docker compose -f tests/idempotency/docker-compose.yml down 手动清理）
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

COMPOSE="docker compose"
if ! docker compose version >/dev/null 2>&1; then
    COMPOSE="docker-compose"
fi

echo "==> [1/5] 启动 PostgreSQL / Redis (docker)"
# 清理可能残留的上一抡服务进程（top_nsp 的 HTTP 服务不响应 SIGTERM，需强制清理）
pkill -x top_nsp 2>/dev/null || true
pkill -x az_nsp 2>/dev/null || true
pkill -x worker 2>/dev/null || true
# 重建依赖容器，保证数据库/队列为干净状态
$COMPOSE -f tests/idempotency/docker-compose.yml down 2>/dev/null || true
$COMPOSE -f tests/idempotency/docker-compose.yml up -d

echo "==> 等待 PostgreSQL 就绪"
for i in $(seq 1 60); do
    if docker exec idem-postgres pg_isready -U nsp_user -d nsp_top >/dev/null 2>&1; then
        break
    fi
    sleep 1
done
# init 脚本在建库后执行 migration，再额外等待其完成
for i in $(seq 1 60); do
    if docker exec idem-postgres psql -U nsp_user -d top_nsp_vpc -tAc \
        "SELECT 1 FROM information_schema.tables WHERE table_name='orchestration_operations'" 2>/dev/null | grep -q 1; then
        break
    fi
    sleep 1
done

echo "==> [2/5] 构建二进制"
go build -o bin/top_nsp ./cmd/top_nsp
go build -o bin/az_nsp ./cmd/az_nsp
go build -o bin/worker ./cmd/worker

export REGION=region-test
export REDIS_ADDR=127.0.0.1:16379
export REDIS_BROKER_DB=1
export POSTGRES_HOST=127.0.0.1
export POSTGRES_PORT=15433
export POSTGRES_USER=nsp_user
export POSTGRES_PASSWORD=nsp_password

PIDS=()
cleanup() {
    echo "==> 停止服务进程"
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    sleep 2
    for pid in "${PIDS[@]}"; do
        kill -9 "$pid" 2>/dev/null || true
    done
}
trap cleanup EXIT

mkdir -p logs

echo "==> [3/5] 启动 top-nsp-vpc (单实例, :19080)"
PORT=19080 ./bin/top_nsp > logs/idem_top_nsp.log 2>&1 &
PIDS+=($!)
for i in $(seq 1 60); do
    curl -sf http://127.0.0.1:19080/api/v1/health >/dev/null 2>&1 && break
    sleep 1
done

echo "==> [4/5] 启动 az-nsp-vpc (:19081) 与 worker (switch/firewall 各1)"
AZ=test-az1 PORT=19081 TOP_NSP_ADDR=http://127.0.0.1:19080 NSP_ADDR=http://127.0.0.1:19081 \
    ./bin/az_nsp > logs/idem_az_nsp.log 2>&1 &
PIDS+=($!)
AZ=test-az1 WORKER_TYPE=switch WORKER_COUNT=1 ./bin/worker > logs/idem_worker_switch.log 2>&1 &
PIDS+=($!)
AZ=test-az1 WORKER_TYPE=firewall WORKER_COUNT=1 ./bin/worker > logs/idem_worker_firewall.log 2>&1 &
PIDS+=($!)

for i in $(seq 1 60); do
    curl -sf http://127.0.0.1:19081/api/v1/health >/dev/null 2>&1 && break
    sleep 1
done

echo "==> 等待 AZ 注册到 Top"
registered=0
for i in $(seq 1 30); do
    if curl -sf http://127.0.0.1:19080/api/v1/azs 2>/dev/null | grep -q test-az1; then
        registered=1
        break
    fi
    sleep 1
done
if [ "$registered" != "1" ]; then
    echo "AZ 注册失败，日志如下："
    tail -20 logs/idem_top_nsp.log logs/idem_az_nsp.log
    exit 1
fi

echo "==> [5/5] 运行幂等测试"
go test -v -count=1 -timeout 15m ./tests/idempotency/...

echo "==> 完成。日志位于 logs/idem_*.log；依赖容器: idem-postgres / idem-redis"
echo "    清理依赖: $COMPOSE -f tests/idempotency/docker-compose.yml down -v"
