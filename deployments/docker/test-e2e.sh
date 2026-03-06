#!/bin/bash

echo "========================================="
echo "NSP 端到端测试"
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

# 4. 创建Region级VPC（会在所有AZ创建）
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

# 5. 等待VPC创建完成
echo ""
echo "5. 等待VPC工作流执行（8秒）..."
sleep 8

# 6. 创建AZ级子网（只在指定AZ创建）
echo ""
echo "6. 创建AZ级子网: subnet-az-test（在cn-beijing-1a）..."
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

# 7. 等待子网创建完成
echo ""
echo "7. 等待子网工作流执行（5秒）..."
sleep 5

echo ""
echo "========================================="
echo "测试完成"
echo ""
echo "查看容器日志:"
echo "  docker-compose -f deployments/docker/docker-compose.yml logs top-nsp"
echo "  docker-compose -f deployments/docker/docker-compose.yml logs az-nsp-cn-beijing-1a"
echo "========================================="
