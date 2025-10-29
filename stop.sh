#!/bin/bash

# 停止脚本

echo "停止VPC工作流系统..."

# 读取PID并停止进程
if [ -f logs/api_server.pid ]; then
    PID=$(cat logs/api_server.pid)
    if ps -p $PID > /dev/null 2>&1; then
        kill $PID
        echo "✓ API服务器已停止 (PID: $PID)"
    fi
    rm logs/api_server.pid
fi

if [ -f logs/switch_worker.pid ]; then
    PID=$(cat logs/switch_worker.pid)
    if ps -p $PID > /dev/null 2>&1; then
        kill $PID
        echo "✓ 交换机Worker已停止 (PID: $PID)"
    fi
    rm logs/switch_worker.pid
fi

if [ -f logs/firewall_worker.pid ]; then
    PID=$(cat logs/firewall_worker.pid)
    if ps -p $PID > /dev/null 2>&1; then
        kill $PID
        echo "✓ 防火墙Worker已停止 (PID: $PID)"
    fi
    rm logs/firewall_worker.pid
fi

echo "所有服务已停止"
