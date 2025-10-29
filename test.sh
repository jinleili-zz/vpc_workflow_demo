#!/bin/bash

# 测试脚本 - 创建VPC

echo "========================================="
echo "测试VPC创建工作流"
echo "========================================="

# 1. 健康检查
echo ""
echo "1. 健康检查..."
curl -s http://localhost:8080/api/v1/health | python3 -m json.tool 2>/dev/null || curl -s http://localhost:8080/api/v1/health

# 2. 创建VPC
echo ""
echo "2. 发送创建VPC请求..."
RESPONSE=$(curl -s -X POST http://localhost:8080/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "test-vpc-001",
    "vrf_name": "VRF-TEST-001",
    "vlan_id": 100,
    "firewall_zone": "trust-zone-001"
  }')

echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"

# 提取workflow_id
WORKFLOW_ID=$(echo "$RESPONSE" | python3 -c "import sys, json; print(json.load(sys.stdin)['workflow_id'])" 2>/dev/null)

echo ""
echo "========================================="
echo "工作流已创建！"
if [ -n "$WORKFLOW_ID" ]; then
    echo "WorkflowID: $WORKFLOW_ID"
fi
echo "========================================="
echo ""
echo "等待任务执行 (8秒)..."
sleep 8
echo ""
echo "=== 最新任务执行日志 ==="
echo ""
echo "[交换机Worker]"
tail -20 logs/switch_worker.log | grep -E "(开始创建|成功创建)" | tail -5
echo ""
echo "[防火墙Worker]"
tail -20 logs/firewall_worker.log | grep -E "(开始创建|成功创建)" | tail -3
echo ""
echo "========================================="
echo "完整日志查看:"
echo "  tail -f logs/switch_worker.log"
echo "  tail -f logs/firewall_worker.log"
echo "========================================="
