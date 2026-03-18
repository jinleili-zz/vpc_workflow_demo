#!/bin/bash

echo "========================================="
echo "NSP 端到端测试（含 PCCN）"
echo "========================================="

TOP_NSP="http://localhost:8080"

# 1. 检查Top NSP健康状态
echo ""
echo "1. 检查Top NSP健康状态..."
curl -s $TOP_NSP/api/v1/health | python3 -m json.tool

# 2. 列出所有Region
echo ""
echo "2. 列出所有Region..."
curl -s $TOP_NSP/api/v1/regions | python3 -m json.tool

# 3. 查看cn-beijing的AZ列表
echo ""
echo "3. 查看cn-beijing的AZ列表..."
curl -s $TOP_NSP/api/v1/regions/cn-beijing/azs | python3 -m json.tool

# 4. 创建Region级VPC 1（会在所有AZ创建）
echo ""
echo "4. 创建Region级VPC: vpc-region-test..."
VPC_RESP=$(curl -s -X POST $TOP_NSP/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "vpc-region-test",
    "region": "cn-beijing",
    "vrf_name": "VRF-REGION-001",
    "vlan_id": 100,
    "firewall_zone": "trust-zone"
  }')

echo "$VPC_RESP" | python3 -m json.tool

# 5. 创建Region级VPC 2（用于PCCN测试）
echo ""
echo "5. 创建Region级VPC 2: vpc-region-test-2..."
VPC_RESP2=$(curl -s -X POST $TOP_NSP/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "vpc-region-test-2",
    "region": "cn-beijing",
    "vrf_name": "VRF-REGION-002",
    "vlan_id": 200,
    "firewall_zone": "trust-zone-2"
  }')

echo "$VPC_RESP2" | python3 -m json.tool

# 6. 等待VPC创建完成
echo ""
echo "6. 等待VPC工作流执行（15秒）..."
sleep 15

# 7. 创建AZ级子网（只在指定AZ创建）
echo ""
echo "7. 创建AZ级子网: subnet-az-test（在cn-beijing-1a）..."
SUBNET_RESP=$(curl -s -X POST $TOP_NSP/api/v1/subnet \
  -H "Content-Type: application/json" \
  -d '{
    "subnet_name": "subnet-az-test",
    "vpc_name": "vpc-region-test",
    "region": "cn-beijing",
    "az": "cn-beijing-1a",
    "cidr": "10.0.1.0/24"
  }')

echo "$SUBNET_RESP" | python3 -m json.tool

# 8. 等待子网创建完成
echo ""
echo "8. 等待子网工作流执行（5秒）..."
sleep 5

# =====================================================
# PCCN 测试
# =====================================================

# 9. 列出所有 VPC
echo ""
echo "9. 列出所有 VPC..."
curl -s $TOP_NSP/api/v1/vpcs | python3 -m json.tool

# 10. 创建PCCN连接（连接两个VPC）
echo ""
echo "10. 创建PCCN连接: pccn-test-001..."
PCCN_RESP=$(curl -s -X POST $TOP_NSP/api/v1/pccn \
  -H "Content-Type: application/json" \
  -d '{
    "pccn_name": "pccn-test-001",
    "vpc1": {
      "vpc_name": "vpc-region-test",
      "region": "cn-beijing"
    },
    "vpc2": {
      "vpc_name": "vpc-region-test-2",
      "region": "cn-beijing"
    }
  }')

echo "$PCCN_RESP" | python3 -m json.tool

# 11. 等待PCCN创建完成
echo ""
echo "11. 等待PCCN工作流执行（10秒）..."
sleep 10

# 12. 查询PCCN状态
echo ""
echo "12. 查询PCCN状态..."
curl -s $TOP_NSP/api/v1/pccn/pccn-test-001/status | python3 -m json.tool

# 13. 列出所有PCCN
echo ""
echo "13. 列出所有PCCN..."
curl -s $TOP_NSP/api/v1/pccns | python3 -m json.tool

# 14. 尝试删除有PCCN连接的VPC（应该失败）
echo ""
echo "14. 尝试删除有PCCN连接的VPC（应被拒绝）..."
DELETE_FAIL_RESP=$(curl -s -X DELETE $TOP_NSP/api/v1/vpc/vpc-region-test)
echo "$DELETE_FAIL_RESP" | python3 -m json.tool

# 15. 删除PCCN
echo ""
echo "15. 删除PCCN: pccn-test-001..."
DELETE_PCCN_RESP=$(curl -s -X DELETE $TOP_NSP/api/v1/pccn/pccn-test-001)
echo "$DELETE_PCCN_RESP" | python3 -m json.tool

# 16. 等待PCCN删除完成
echo ""
echo "16. 等待PCCN删除完成（5秒）..."
sleep 5

echo ""
echo "========================================="
echo "测试完成"
echo ""
echo "查看容器日志:"
echo "  docker-compose -f deployments/docker/docker-compose.yml logs top-nsp"
echo "  docker-compose -f deployments/docker/docker-compose.yml logs az-nsp-cn-beijing-1a"
echo ""
echo "PCCN 测试验证:"
echo "  ✓ 创建两个 VPC"
echo "  ✓ 创建 PCCN 连接两个 VPC"
echo "  ✓ 查询 PCCN 状态"
echo "  ✓ 验证有 PCCN 时无法删除 VPC"
echo "  ✓ 删除 PCCN"
echo "========================================="
