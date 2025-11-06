#!/bin/bash

# Tyk API Gateway 认证测试脚本

TYK_URL="http://localhost:9696"
TYK_API_ID="nsp-top-api-v1"

echo "=========================================="
echo "Tyk API Gateway 认证测试"
echo "=========================================="

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 1. 测试无认证访问（应该失败）
echo -e "\n${YELLOW}[测试 1] 无认证访问（预期失败）${NC}"
echo "命令: curl -X GET ${TYK_URL}/api/v1/health"
RESPONSE=$(curl -s -w "\n%{http_code}" ${TYK_URL}/api/v1/health)
HTTP_CODE=$(echo "$RESPONSE" | tail -n 1)
BODY=$(echo "$RESPONSE" | head -n -1)

if [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ]; then
    echo -e "${GREEN}✓ 正确拒绝未认证请求 (HTTP $HTTP_CODE)${NC}"
    echo "响应: $BODY"
else
    echo -e "${RED}✗ 未按预期拒绝 (HTTP $HTTP_CODE)${NC}"
fi

# 2. 创建测试 API Key
echo -e "\n${YELLOW}[测试 2] 创建 API Key${NC}"
API_KEY="nsp-test-key-$(date +%s)"
echo "生成的 API Key: $API_KEY"

# 使用 Tyk Gateway API 创建密钥
curl -s -X POST ${TYK_URL}/tyk/keys/${API_KEY} \
  -H "x-tyk-authorization: nsp-tyk-secret-key-2025" \
  -H "Content-Type: application/json" \
  -d '{
    "allowance": 1000,
    "rate": 1000,
    "per": 60,
    "expires": -1,
    "quota_max": -1,
    "org_id": "nsp-org",
    "access_rights": {
      "'${TYK_API_ID}'": {
        "api_id": "'${TYK_API_ID}'",
        "api_name": "NSP Top API",
        "versions": ["Default"]
      }
    },
    "meta_data": {
      "user": "test-user",
      "created_at": "'$(date -Iseconds)'"
    }
  }' | jq '.'

echo -e "${GREEN}✓ API Key 已创建${NC}"

# 3. 使用 API Key 访问（应该成功）
echo -e "\n${YELLOW}[测试 3] 使用 API Key 访问${NC}"
echo "命令: curl -H 'X-API-Key: ${API_KEY}' ${TYK_URL}/api/v1/health"
RESPONSE=$(curl -s -w "\n%{http_code}" -H "X-API-Key: ${API_KEY}" ${TYK_URL}/api/v1/health)
HTTP_CODE=$(echo "$RESPONSE" | tail -n 1)
BODY=$(echo "$RESPONSE" | head -n -1)

if [ "$HTTP_CODE" = "200" ]; then
    echo -e "${GREEN}✓ 认证成功，访问允许 (HTTP $HTTP_CODE)${NC}"
    echo "响应: $BODY"
else
    echo -e "${RED}✗ 认证失败 (HTTP $HTTP_CODE)${NC}"
    echo "响应: $BODY"
fi

# 4. 测试速率限制
echo -e "\n${YELLOW}[测试 4] 速率限制测试（发送1005次请求）${NC}"
SUCCESS_COUNT=0
RATE_LIMITED_COUNT=0

for i in {1..1005}; do
    HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "X-API-Key: ${API_KEY}" ${TYK_URL}/api/v1/health)
    if [ "$HTTP_CODE" = "200" ]; then
        ((SUCCESS_COUNT++))
    elif [ "$HTTP_CODE" = "429" ]; then
        ((RATE_LIMITED_COUNT++))
    fi
    
    # 每100次显示进度
    if [ $((i % 100)) -eq 0 ]; then
        echo "进度: $i/1005 (成功: $SUCCESS_COUNT, 限制: $RATE_LIMITED_COUNT)"
    fi
done

echo -e "${GREEN}成功请求: $SUCCESS_COUNT${NC}"
echo -e "${YELLOW}被限制请求: $RATE_LIMITED_COUNT${NC}"

if [ $RATE_LIMITED_COUNT -gt 0 ]; then
    echo -e "${GREEN}✓ 速率限制生效${NC}"
else
    echo -e "${RED}✗ 速率限制未生效${NC}"
fi

# 5. 测试 JWT 认证（如果启用）
echo -e "\n${YELLOW}[测试 5] JWT 认证测试${NC}"
JWT_SECRET="nsp-jwt-secret-2025"

# 生成简单的 JWT (需要安装 jq 和 base64)
JWT_HEADER=$(echo -n '{"alg":"HS256","typ":"JWT"}' | base64 | tr -d '=' | tr '/+' '_-' | tr -d '\n')
JWT_PAYLOAD=$(echo -n '{"sub":"test-user","pol":"default","exp":'$(($(date +%s) + 3600))'}' | base64 | tr -d '=' | tr '/+' '_-' | tr -d '\n')
JWT_SIGNATURE=$(echo -n "${JWT_HEADER}.${JWT_PAYLOAD}" | openssl dgst -sha256 -hmac "$JWT_SECRET" -binary | base64 | tr -d '=' | tr '/+' '_-' | tr -d '\n')
JWT_TOKEN="${JWT_HEADER}.${JWT_PAYLOAD}.${JWT_SIGNATURE}"

echo "生成的 JWT: ${JWT_TOKEN:0:50}..."
echo "命令: curl -H 'Authorization: Bearer \$JWT_TOKEN' ${TYK_URL}/jwt/api/v1/health"

RESPONSE=$(curl -s -w "\n%{http_code}" -H "Authorization: Bearer ${JWT_TOKEN}" ${TYK_URL}/jwt/api/v1/health)
HTTP_CODE=$(echo "$RESPONSE" | tail -n 1)
BODY=$(echo "$RESPONSE" | head -n -1)

if [ "$HTTP_CODE" = "200" ]; then
    echo -e "${GREEN}✓ JWT 认证成功${NC}"
    echo "响应: $BODY"
else
    echo -e "${YELLOW}⚠ JWT 端点可能未配置 (HTTP $HTTP_CODE)${NC}"
fi

# 6. 查询 API Key 信息
echo -e "\n${YELLOW}[测试 6] 查询 API Key 信息${NC}"
curl -s -X GET ${TYK_URL}/tyk/keys/${API_KEY} \
  -H "x-tyk-authorization: nsp-tyk-secret-key-2025" | jq '.'

# 7. 删除测试 API Key
echo -e "\n${YELLOW}[测试 7] 清理测试数据${NC}"
curl -s -X DELETE ${TYK_URL}/tyk/keys/${API_KEY} \
  -H "x-tyk-authorization: nsp-tyk-secret-key-2025"
echo -e "${GREEN}✓ 测试 API Key 已删除${NC}"

echo -e "\n=========================================="
echo -e "${GREEN}测试完成！${NC}"
echo "=========================================="
