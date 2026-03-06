#!/bin/bash

echo "========================================="
echo "本地启动 NSP 测试环境"
echo "========================================="

# 停止之前的进程
pkill -f "top-nsp" 2>/dev/null
pkill -f "az-nsp" 2>/dev/null
sleep 1

# 确保 Redis 运行
if ! pgrep -x "redis-server" > /dev/null; then
    echo "启动 Redis..."
    redis-server --daemonize yes
    sleep 2
fi

# 启动 Top NSP
echo "启动 Top NSP (端口 9080)..."
cd /root/workspace/nsp/workflow_qoder
SERVICE_TYPE=top \
  PORT=9080 \
  REDIS_ADDR=localhost:6379 \
  REDIS_DATA_DB=0 \
  REDIS_BROKER_DB=1 \
  ./top-nsp > logs/top_nsp.log 2>&1 &

sleep 3

# 启动 AZ NSP - cn-beijing-1a
echo "启动 AZ NSP cn-beijing-1a (端口 9081)..."
SERVICE_TYPE=az \
  REGION=cn-beijing \
  AZ=cn-beijing-1a \
  PORT=9081 \
  REDIS_ADDR=localhost:6379 \
  REDIS_DATA_DB=0 \
  REDIS_BROKER_DB=1 \
  TOP_NSP_ADDR=http://localhost:9080 \
  NSP_ADDR=http://localhost:9081 \
  ./az-nsp > logs/az_nsp_1a.log 2>&1 &

sleep 2

# 启动 AZ NSP - cn-beijing-1b
echo "启动 AZ NSP cn-beijing-1b (端口 9082)..."
SERVICE_TYPE=az \
  REGION=cn-beijing \
  AZ=cn-beijing-1b \
  PORT=9082 \
  REDIS_ADDR=localhost:6379 \
  REDIS_DATA_DB=0 \
  REDIS_BROKER_DB=1 \
  TOP_NSP_ADDR=http://localhost:9080 \
  NSP_ADDR=http://localhost:9082 \
  ./az-nsp > logs/az_nsp_1b.log 2>&1 &

sleep 2

# 启动 AZ NSP - cn-shanghai-1a
echo "启动 AZ NSP cn-shanghai-1a (端口 9083)..."
SERVICE_TYPE=az \
  REGION=cn-shanghai \
  AZ=cn-shanghai-1a \
  PORT=9083 \
  REDIS_ADDR=localhost:6379 \
  REDIS_DATA_DB=0 \
  REDIS_BROKER_DB=1 \
  TOP_NSP_ADDR=http://localhost:9080 \
  NSP_ADDR=http://localhost:9083 \
  ./az-nsp > logs/az_nsp_sh_1a.log 2>&1 &

echo ""
echo "========================================="
echo "所有服务已启动！"
echo "========================================="
echo ""
echo "服务地址："
echo "  Top NSP:    http://localhost:9080"
echo "  AZ NSP 1a:  http://localhost:9081"
echo "  AZ NSP 1b:  http://localhost:9082"
echo "  AZ NSP sh:  http://localhost:9083"
echo ""
echo "查看日志："
echo "  tail -f logs/top_nsp.log"
echo "  tail -f logs/az_nsp_1a.log"
echo "  tail -f logs/az_nsp_1b.log"
echo "  tail -f logs/az_nsp_sh_1a.log"
echo ""
echo "停止服务："
echo "  pkill -f nsp"
echo "========================================="
