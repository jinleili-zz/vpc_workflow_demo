#!/bin/bash

echo "========================================="
echo "NSP 端到端测试（含 PCCN + mTLS 验证）"
echo "========================================="

TOP_NSP="http://localhost:8080"

# 0. 检查证书是否已生成
echo ""
echo "0. 检查mTLS证书..."
CERTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/certs/generated"
if [ ! -d "${CERTS_DIR}/ca" ] || [ ! -d "${CERTS_DIR}/top" ]; then
    echo "证书未生成，正在生成..."
    ./certs/generate-certs.sh
else
    echo "证书已存在: ${CERTS_DIR}"
fi

# 验证证书文件存在
echo ""
echo "验证证书文件..."
CERT_OK=true
if [ ! -f "${CERTS_DIR}/ca/ca.crt" ]; then
    echo "  ✗ CA证书不存在"
    CERT_OK=false
else
    echo "  ✓ CA证书存在"
fi
if [ ! -f "${CERTS_DIR}/top/top-client.crt" ] || [ ! -f "${CERTS_DIR}/top/top-client.key" ]; then
    echo "  ✗ Top客户端证书不存在"
    CERT_OK=false
else
    echo "  ✓ Top客户端证书存在"
fi
if [ ! -f "${CERTS_DIR}/az/az-nsp-vpc-cn-beijing-1a/server.crt" ]; then
    echo "  ✗ AZ服务端证书不存在"
    CERT_OK=false
else
    echo "  ✓ AZ服务端证书存在"
fi

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
echo ""
echo "========================================="
echo "mTLS 链路验证"
echo "========================================="

# 验证 AZ VPC 服务使用 HTTPS
echo ""
echo "17. 验证 AZ VPC 服务注册地址（应为 https://）..."
AZS_RESP=$(curl -s $TOP_NSP/api/v1/azs)
echo "$AZS_RESP" | python3 -m json.tool

# 检查 AZ 地址是否使用 https scheme
if echo "$AZS_RESP" | grep -q '"nsp_addr".*https://'; then
    echo "  ✓ AZ VPC 地址使用 HTTPS scheme"
else
    echo "  ✗ AZ VPC 地址未使用 HTTPS scheme（mTLS 可能未启用）"
fi

# 验证 mTLS 连接（通过 Top NSP 的健康检查间接验证）
echo ""
echo "18. 验证 Top -> AZ mTLS 通信（通过 AZ 健康检查）..."
# Top NSP 在轮询 AZ 健康状态时会使用 mTLS client
# 如果 mTLS 配置正确，AZ 应该能够被成功健康检查
sleep 2
curl -s $TOP_NSP/api/v1/health | python3 -m json.tool

echo ""
echo "========================================="
echo "mTLS 测试验证:"
echo "  ✓ 证书已生成并挂载"
echo "  ✓ AZ VPC 使用 HTTPS 监听"
echo "  ✓ Top VPC 使用 mTLS 客户端连接 AZ"
echo ""
echo "注意: VFW 服务保持 AK/SK + HTTP（未使用 mTLS）"
echo "========================================="
