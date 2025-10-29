#!/bin/bash

# 启动脚本 - VPC工作流系统

echo "========================================="
echo "VPC分布式任务工作流系统"
echo "========================================="

# 检查Redis是否运行
echo "检查Redis连接..."
if ! redis-cli ping > /dev/null 2>&1; then
    echo "错误: Redis未运行，请先启动Redis"
    echo "可以运行: docker run -d -p 6379:6379 redis:alpine"
    exit 1
fi
echo "✓ Redis连接正常"

# 编译项目
echo ""
echo "编译项目..."
go build -o bin/api_server ./cmd/api_server
go build -o bin/switch_worker ./cmd/switch_worker
go build -o bin/firewall_worker ./cmd/firewall_worker

if [ $? -ne 0 ]; then
    echo "编译失败"
    exit 1
fi
echo "✓ 编译完成"

# 创建日志目录
mkdir -p logs

echo ""
echo "启动服务..."
echo "========================================="

# 启动API服务器
echo "启动API服务器 (端口: 8080)..."
nohup ./bin/api_server > logs/api_server.log 2>&1 &
API_PID=$!
echo "✓ API服务器已启动 (PID: $API_PID)"

# 等待API服务器启动
sleep 2

# 启动交换机Worker
echo "启动交换机Worker..."
nohup ./bin/switch_worker > logs/switch_worker.log 2>&1 &
SWITCH_PID=$!
echo "✓ 交换机Worker已启动 (PID: $SWITCH_PID)"

# 启动防火墙Worker
echo "启动防火墙Worker..."
nohup ./bin/firewall_worker > logs/firewall_worker.log 2>&1 &
FIREWALL_PID=$!
echo "✓ 防火墙Worker已启动 (PID: $FIREWALL_PID)"

# 保存PID
echo $API_PID > logs/api_server.pid
echo $SWITCH_PID > logs/switch_worker.pid
echo $FIREWALL_PID > logs/firewall_worker.pid

echo ""
echo "========================================="
echo "所有服务已启动！"
echo "========================================="
echo "API服务器: http://localhost:8080"
echo "健康检查: curl http://localhost:8080/api/v1/health"
echo ""
echo "查看日志:"
echo "  API服务器:    tail -f logs/api_server.log"
echo "  交换机Worker: tail -f logs/switch_worker.log"
echo "  防火墙Worker: tail -f logs/firewall_worker.log"
echo ""
echo "停止服务: ./stop.sh"
echo "========================================="
