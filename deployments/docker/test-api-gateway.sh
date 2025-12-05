#!/bin/bash

echo "========================================="
echo "NSP API 测试 (通过Tyk Gateway)"
echo "========================================="

GATEWAY_URL="http://localhost:9696"

echo ""
echo "1. 健康检查..."
curl -s $GATEWAY_URL/api/v1/health && echo ""

echo ""
echo "2. 创建VPC: test-vpc-001..."
response=$(curl -s -X POST $GATEWAY_URL/api/v1/vpc \
  -H 'Content-Type: application/json' \
  -d '{
    "vpc_name": "test-vpc-001",
    "vrf_name": "vrf-test-001",
    "vlan_id": 100,
    "firewall_zone": "dmz"
  }')
echo "$response"

# 解析workflow_id (需要jq，这里先简单打印)
echo ""

echo ""
echo "3. 创建VPC: prod-vpc-001..."
response=$(curl -s -X POST $GATEWAY_URL/api/v1/vpc \
  -H 'Content-Type: application/json' \
  -d '{
    "vpc_name": "prod-vpc-001",
    "vrf_name": "vrf-prod-001",
    "vlan_id": 200,
    "firewall_zone": "production"
  }')
echo "$response"

echo ""
echo "========================================="
echo "测试完成"
echo "========================================="
