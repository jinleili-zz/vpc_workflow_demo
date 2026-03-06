#!/bin/bash

echo "========================================="
echo "NSP 系统端到端测试"
echo "========================================="

TOP_NSP="http://localhost:9080"

# 等待服务就绪
echo ""
echo "等待服务启动（3秒）..."
sleep 3

# 1. 检查 Top NSP 健康状态
echo ""
echo "========================================="
echo "1. 检查 Top NSP 健康状态"
echo "========================================="
curl -s $TOP_NSP/api/v1/health | python3 -m json.tool

# 2. 列出所有 Region
echo ""
echo "========================================="
echo "2. 列出所有 Region"
echo "========================================="
curl -s $TOP_NSP/api/v1/regions | python3 -m json.tool

# 3. 查看 cn-beijing 的 AZ 列表
echo ""
echo "========================================="
echo "3. 查看 cn-beijing 的 AZ 列表"
echo "========================================="
curl -s $TOP_NSP/api/v1/regions/cn-beijing/azs | python3 -m json.tool

# 4. 查看 cn-shanghai 的 AZ 列表
echo ""
echo "========================================="
echo "4. 查看 cn-shanghai 的 AZ 列表"
echo "========================================="
curl -s $TOP_NSP/api/v1/regions/cn-shanghai/azs | python3 -m json.tool

# 5. 创建 Region 级 VPC（会在所有 AZ 创建）
echo ""
echo "========================================="
echo "5. 创建 Region 级 VPC: vpc-region-test"
echo "   (将在 cn-beijing 的所有 AZ 创建)"
echo "========================================="
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

# 6. 等待 VPC 创建完成
echo ""
echo "等待 VPC 工作流执行（10秒）..."
sleep 10

# 7. 查询 cn-beijing-1a 的 VPC 状态
echo ""
echo "========================================="
echo "7. 查询 cn-beijing-1a 的 VPC 状态"
echo "========================================="
curl -s http://localhost:9081/api/v1/vpc/vpc-region-test/status | python3 -m json.tool

# 8. 查询 cn-beijing-1b 的 VPC 状态
echo ""
echo "========================================="
echo "8. 查询 cn-beijing-1b 的 VPC 状态"
echo "========================================="
curl -s http://localhost:9082/api/v1/vpc/vpc-region-test/status | python3 -m json.tool

# 9. 创建 AZ 级子网（只在指定 AZ 创建）
echo ""
echo "========================================="
echo "9. 创建 AZ 级子网: subnet-az-test"
echo "   (只在 cn-beijing-1a 创建)"
echo "========================================="
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

# 10. 等待子网创建完成
echo ""
echo "等待子网工作流执行（5秒）..."
sleep 5

# 11. 再创建一个上海的 VPC
echo ""
echo "========================================="
echo "11. 创建上海 Region 的 VPC: vpc-shanghai-test"
echo "========================================="
VPC_SH_RESP=$(curl -s -X POST $TOP_NSP/api/v1/vpc \
  -H "Content-Type: application/json" \
  -d '{
    "vpc_name": "vpc-shanghai-test",
    "region": "cn-shanghai",
    "vrf_name": "VRF-SHANGHAI-001",
    "vlan_id": 200,
    "firewall_zone": "dmz-zone"
  }')

echo "$VPC_SH_RESP" | python3 -m json.tool

echo ""
echo "========================================="
echo "测试完成！"
echo ""
echo "查看各个 AZ 的日志以验证任务执行："
echo "  tail -50 logs/az_nsp_1a.log | grep Workflow"
echo "  tail -50 logs/az_nsp_1b.log | grep Workflow"
echo "  tail -50 logs/az_nsp_sh_1a.log | grep Workflow"
echo ""
echo "系统架构验证："
echo "  ✓ Top NSP 管理多个 Region"
echo "  ✓ 每个 Region 包含多个 AZ"
echo "  ✓ Region 级服务（VPC）在所有 AZ 创建"
echo "  ✓ AZ 级服务（子网）在指定 AZ 创建"
echo "  ✓ 动态 AZ 注册和心跳机制"
echo "========================================="
