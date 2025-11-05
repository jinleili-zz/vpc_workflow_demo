#!/bin/bash

# 测试脚本 - 基于消息队列的Workflow功能

echo "========================================="
 echo "测试VPC创建工作流 (Chain模式)"
echo "========================================="

# 1. 健康检查
echo ""
echo "1. 健康检查..."
curl -s http://localhost:8080/api/v1/health | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/api/v1/health

# 2. 创建VPC (Chain模式 - 顺序执行)
echo ""
echo "2. 发送创建VPC请求 (Chain模式: VRF -> VLAN -> Firewall)..."
RESPONSE=$(curl -s -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-vpc-chain-001",
    "vrf_name": "VRF-CHAIN-001",
    "vlan_id": 100,
    "firewall_zone": "trust-zone-chain-001"
  }')

echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"

# 提取vpc_name和workflow_id
VPC_NAME="test-vpc-chain-001"
WORKFLOW_ID=$(echo "$RESPONSE" | python3 -c "import sys, json; print(json.load(sys.stdin)['workflow_id'])" 2>/dev/null)

echo ""
echo "========================================="
echo "工作流已创建！"
if [ -n "$WORKFLOW_ID" ]; then
    echo "WorkflowID: $WORKFLOW_ID"
fi
echo "========================================="
echo ""
echo "等待任务顺序执行 (8秒)..."
sleep 8
echo ""

# 3. 查询工作流状态（使用VPC名字）
if [ -n "$VPC_NAME" ]; then
    echo "3. 查询VPC状态 (VPC名字: $VPC_NAME)..."
    curl -s http://localhost:8080/api/v1/vpc/$VPC_NAME/status | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/api/v1/vpc/$VPC_NAME/status
    echo ""
fi

echo ""
echo "=== 最新任务执行日志 ==="
echo ""
echo "[交换机Worker - Chain执行顺序]"
tail -30 logs/switch_worker.log 2>/dev/null | grep -E "(Workflow-Step|\u2713)" | tail -10
echo ""
echo "[防火墙Worker - Chain最后一步]"
tail -30 logs/firewall_worker.log 2>/dev/null | grep -E "(Workflow-Step|Workflow-Complete|\u2713)" | tail -5
echo ""
echo "========================================="
echo "Workflow模式说明:"
echo "  - Chain模式: 任务顺序执行 (VRF -> VLAN -> Firewall)"
echo "  - 每个任务完成后才执行下一个"
echo "  - 通过消息队列(Redis)传递任务数据"
echo ""
echo "完整日志查看:"
echo "  tail -f logs/api_server.log"
echo "  tail -f logs/switch_worker.log"
echo "  tail -f logs/firewall_worker.log"
echo "========================================="
