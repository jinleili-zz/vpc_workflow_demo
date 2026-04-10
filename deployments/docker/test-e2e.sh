#!/bin/bash

set -e

echo "========================================="
echo "NSP 端到端测试（含 PCCN + mTLS 验证）"
echo "========================================="

TOP_NSP="http://localhost:8080"
TIMEOUT=60  # 单个操作超时时间（秒）
TEST_FAILED=false

# 辅助函数：等待资源达到指定状态
wait_for_status() {
    local RESOURCE_TYPE=$1  # vpc, pccn
    local RESOURCE_NAME=$2
    local EXPECTED_STATUS=$3  # running, succeeded
    local MAX_WAIT=$4
    
    local ELAPSED=0
    local INTERVAL=2
    
    echo "等待 $RESOURCE_TYPE '$RESOURCE_NAME' 变为 $EXPECTED_STATUS 状态..."
    
    while [ $ELAPSED -lt $MAX_WAIT ]; do
        if [ "$RESOURCE_TYPE" = "vpc" ]; then
            # 只匹配顶层 status 字段，避免匹配 az_details 中的 status
            STATUS=$(curl -s $TOP_NSP/api/v1/vpc/$RESOURCE_NAME/status 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('status',''))" 2>/dev/null || echo "")
        elif [ "$RESOURCE_TYPE" = "pccn" ]; then
            STATUS=$(curl -s $TOP_NSP/api/v1/pccn/$RESOURCE_NAME/status 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('overall_status',''))" 2>/dev/null || echo "")
        fi
        
        if [ "$STATUS" = "$EXPECTED_STATUS" ]; then
            echo "  ✓ $RESOURCE_TYPE '$RESOURCE_NAME' 状态: $STATUS"
            return 0
        fi
        
        # 如果状态是 failed，立即返回失败
        if [ "$STATUS" = "failed" ]; then
            echo "  ✗ $RESOURCE_TYPE '$RESOURCE_NAME' 状态: failed"
            return 1
        fi
        
        sleep $INTERVAL
        ELAPSED=$((ELAPSED + INTERVAL))
        echo "  ... 等待中 (${ELAPSED}s/${MAX_WAIT}s), 当前状态: ${STATUS:-unknown}"
    done
    
    echo "  ✗ 超时: $RESOURCE_TYPE '$RESOURCE_NAME' 未在 ${MAX_WAIT}s 内达到 $EXPECTED_STATUS 状态"
    return 1
}

# 辅助函数：检查服务日志是否有错误
check_service_errors() {
    echo ""
    echo "检查服务日志是否有错误..."
    
    # 只检查最近 120 秒的日志，避免历史错误干扰
    local ERROR_COUNT=$(docker logs top-nsp-vpc --since 120s 2>&1 | grep -iE "(error|fail|panic)" | grep -v "failed to list timed out" | wc -l)
    
    if [ $ERROR_COUNT -gt 0 ]; then
        echo "  ⚠ 发现 $ERROR_COUNT 条错误日志:"
        docker logs top-nsp-vpc --since 120s 2>&1 | grep -iE "(error|fail|panic)" | grep -v "failed to list timed out" | tail -5
        return 1
    fi
    
    echo "  ✓ 无严重错误日志"
    return 0
}

# 辅助函数：检查数据库 SAGA 事务状态
check_saga_status() {
    local TX_ID=$1
    local EXPECTED=$2
    
    echo "检查 SAGA 事务 $TX_ID 状态..."
    
    local TX_STATUS=$(PGPASSWORD=nsp_password psql -h localhost -U nsp_user -d top_nsp_vpc -t -c "SELECT status FROM saga_transactions WHERE id = '$TX_ID';" 2>/dev/null | tr -d ' ')
    
    if [ "$TX_STATUS" = "$EXPECTED" ]; then
        echo "  ✓ SAGA 事务状态: $TX_STATUS"
        return 0
    else
        echo "  ✗ SAGA 事务状态: $TX_STATUS (期望: $EXPECTED)"
        return 1
    fi
}

# 清理函数
cleanup() {
    if [ "$TEST_FAILED" = true ]; then
        echo ""
        echo "========================================="
        echo "测试失败，输出诊断信息"
        echo "========================================="
        echo ""
        echo "=== SAGA 事务状态 ==="
        PGPASSWORD=nsp_password psql -h localhost -U nsp_user -d top_nsp_vpc -c "SELECT id, status, current_step, last_error FROM saga_transactions ORDER BY created_at DESC LIMIT 5;" 2>/dev/null || true
        echo ""
        echo "=== VPC 注册状态 ==="
        PGPASSWORD=nsp_password psql -h localhost -U nsp_user -d top_nsp_vpc -c "SELECT vpc_name, status FROM vpc_registry;" 2>/dev/null || true
        echo ""
        echo "=== 最近错误日志 ==="
        docker logs top-nsp-vpc 2>&1 | grep -iE "(error|fail)" | tail -10 || true
    fi
}

trap cleanup EXIT

# =====================================================
# 0. 检查 mTLS 证书
# =====================================================
echo ""
echo "0. 检查mTLS证书..."
CERTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/certs/generated"

if [ ! -d "${CERTS_DIR}/ca" ] || [ ! -d "${CERTS_DIR}/top" ]; then
    echo "证书未生成，正在生成..."
    ./certs/generate-certs.sh
else
    echo "证书已存在: ${CERTS_DIR}"
fi

CERT_OK=true
[ ! -f "${CERTS_DIR}/ca/ca.crt" ] && { echo "  ✗ CA证书不存在"; CERT_OK=false; } || echo "  ✓ CA证书存在"
[ ! -f "${CERTS_DIR}/top/top-client.crt" ] && { echo "  ✗ Top客户端证书不存在"; CERT_OK=false; } || echo "  ✓ Top客户端证书存在"
[ ! -f "${CERTS_DIR}/az/az-nsp-vpc-cn-beijing-1a/server.crt" ] && { echo "  ✗ AZ服务端证书不存在"; CERT_OK=false; } || echo "  ✓ AZ服务端证书存在"

if [ "$CERT_OK" = false ]; then
    echo "证书检查失败"
    TEST_FAILED=true
    exit 1
fi

# =====================================================
# 1. 检查 Top NSP 健康状态
# =====================================================
echo ""
echo "1. 检查Top NSP健康状态..."
HEALTH=$(curl -s $TOP_NSP/api/v1/health)
echo "$HEALTH" | python3 -m json.tool

if ! echo "$HEALTH" | grep -q '"status":"ok"'; then
    echo "  ✗ Top NSP 健康检查失败"
    TEST_FAILED=true
    exit 1
fi
echo "  ✓ Top NSP 健康"

# =====================================================
# 2. 验证 AZ 注册（mTLS）
# =====================================================
echo ""
echo "2. 验证 AZ 注册状态..."
AZS_RESP=$(curl -s $TOP_NSP/api/v1/azs)
echo "$AZS_RESP" | python3 -m json.tool

# 检查 AZ 地址是否使用 https scheme
if echo "$AZS_RESP" | grep -q '"nsp_addr".*https://'; then
    echo "  ✓ AZ VPC 地址使用 HTTPS scheme"
else
    echo "  ✗ AZ VPC 地址未使用 HTTPS scheme"
    TEST_FAILED=true
    exit 1
fi

# =====================================================
# 3. 创建 VPC 1 并验证
# =====================================================
echo ""
echo "3. 创建Region级VPC: vpc-region-test..."
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

VPC1_TX=$(echo "$VPC_RESP" | grep -o '"workflow_id":"[^"]*"' | cut -d'"' -f4)
if [ -z "$VPC1_TX" ]; then
    echo "  ✗ VPC 创建请求失败"
    TEST_FAILED=true
    exit 1
fi
echo "  事务ID: $VPC1_TX"

# 等待 VPC 创建完成
if ! wait_for_status vpc "vpc-region-test" "running" $TIMEOUT; then
    TEST_FAILED=true
    exit 1
fi

# 验证 SAGA 事务状态
check_saga_status "$VPC1_TX" "succeeded" || { TEST_FAILED=true; exit 1; }

# =====================================================
# 4. 创建 VPC 2 并验证
# =====================================================
echo ""
echo "4. 创建Region级VPC 2: vpc-region-test-2..."
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

VPC2_TX=$(echo "$VPC_RESP2" | grep -o '"workflow_id":"[^"]*"' | cut -d'"' -f4)
if [ -z "$VPC2_TX" ]; then
    echo "  ✗ VPC2 创建请求失败"
    TEST_FAILED=true
    exit 1
fi

if ! wait_for_status vpc "vpc-region-test-2" "running" $TIMEOUT; then
    TEST_FAILED=true
    exit 1
fi

check_saga_status "$VPC2_TX" "succeeded" || { TEST_FAILED=true; exit 1; }

# =====================================================
# 5. 创建 PCCN 并验证
# =====================================================
echo ""
echo "5. 创建PCCN连接: pccn-test-001..."
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

PCCN_TX=$(echo "$PCCN_RESP" | grep -o '"tx_id":"[^"]*"' | cut -d'"' -f4)
if [ -z "$PCCN_TX" ]; then
    echo "  ✗ PCCN 创建请求失败"
    TEST_FAILED=true
    exit 1
fi

if ! wait_for_status pccn "pccn-test-001" "running" $TIMEOUT; then
    TEST_FAILED=true
    exit 1
fi

check_saga_status "$PCCN_TX" "succeeded" || { TEST_FAILED=true; exit 1; }

# =====================================================
# 6. 验证 PCCN 保护 VPC 删除
# =====================================================
echo ""
echo "6. 尝试删除有PCCN连接的VPC（应被拒绝）..."
DELETE_FAIL_RESP=$(curl -s -X DELETE $TOP_NSP/api/v1/vpc/vpc-region-test)
echo "$DELETE_FAIL_RESP" | python3 -m json.tool

if echo "$DELETE_FAIL_RESP" | grep -q '"success":false'; then
    echo "  ✓ VPC 删除被正确拒绝"
else
    echo "  ✗ VPC 删除应该被拒绝但没有"
    TEST_FAILED=true
    exit 1
fi

# =====================================================
# 7. 删除 PCCN
# =====================================================
echo ""
echo "7. 删除PCCN: pccn-test-001..."
DELETE_PCCN_RESP=$(curl -s -X DELETE $TOP_NSP/api/v1/pccn/pccn-test-001)
echo "$DELETE_PCCN_RESP" | python3 -m json.tool

# 等待 PCCN 删除
sleep 5

# =====================================================
# 8. 最终检查
# =====================================================
check_service_errors || { TEST_FAILED=true; exit 1; }

# =====================================================
# 总结
# =====================================================
echo ""
echo "========================================="
echo "测试通过"
echo "========================================="
echo ""
echo "验证项:"
echo "  ✓ mTLS 证书生成正确"
echo "  ✓ Top NSP 健康检查"
echo "  ✓ AZ 注册 HTTPS 地址"
echo "  ✓ VPC 创建成功 (状态: running)"
echo "  ✓ SAGA 事务执行成功"
echo "  ✓ PCCN 创建成功"
echo "  ✓ PCCN 保护 VPC 删除"
echo "  ✓ 无严重错误日志"
echo ""
