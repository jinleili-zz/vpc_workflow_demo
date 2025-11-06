#!/bin/bash

# Tyk API Gateway 认证模式一键切换脚本
# 用法: ./toggle-tyk-auth.sh [keyless|auth|status]

CONFIG_FILE="./tyk-config/apps/nsp-top-api.json"
BACKUP_FILE="./tyk-config/apps/nsp-top-api.json.bak"

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# 检查配置文件是否存在
if [ ! -f "$CONFIG_FILE" ]; then
    echo -e "${RED}错误: 配置文件不存在: $CONFIG_FILE${NC}"
    exit 1
fi

# 显示当前状态
show_status() {
    echo -e "\n${YELLOW}=== 当前配置状态 ===${NC}"
    
    USE_KEYLESS=$(grep -o '"use_keyless":[[:space:]]*[^,]*' "$CONFIG_FILE" | grep -o 'true\|false')
    USE_STANDARD_AUTH=$(grep -o '"use_standard_auth":[[:space:]]*[^,]*' "$CONFIG_FILE" | grep -o 'true\|false')
    
    if [ "$USE_KEYLESS" = "true" ]; then
        echo -e "${GREEN}✓ 模式: 开放访问 (无需认证)${NC}"
        echo "  - 任何人都可以访问 API"
        echo "  - 仅受速率限制保护"
    elif [ "$USE_STANDARD_AUTH" = "true" ]; then
        echo -e "${YELLOW}✓ 模式: API Key 认证${NC}"
        echo "  - 需要在请求头中提供 X-API-Key"
        echo "  - 支持细粒度权限控制"
    else
        echo -e "${RED}⚠ 模式: 未知状态${NC}"
    fi
    
    echo ""
}

# 切换到开放访问模式
enable_keyless() {
    echo -e "${YELLOW}正在切换到开放访问模式...${NC}"
    
    # 备份当前配置
    cp "$CONFIG_FILE" "$BACKUP_FILE"
    echo -e "${GREEN}✓ 已备份配置到: $BACKUP_FILE${NC}"
    
    # 使用 sed 修改配置
    sed -i 's/"use_keyless":[[:space:]]*false/"use_keyless": true/' "$CONFIG_FILE"
    sed -i 's/"use_standard_auth":[[:space:]]*true/"use_standard_auth": false/' "$CONFIG_FILE"
    
    # 简化 auth 配置
    sed -i '/"auth":/,/},/ c\
  "auth": {\
    "auth_header_name": "Authorization"\
  },' "$CONFIG_FILE"
    
    echo -e "${GREEN}✓ 配置已更新为开放访问模式${NC}"
    
    # 重启 Tyk Gateway
    restart_tyk
}

# 切换到认证模式
enable_auth() {
    echo -e "${YELLOW}正在切换到 API Key 认证模式...${NC}"
    
    # 备份当前配置
    cp "$CONFIG_FILE" "$BACKUP_FILE"
    echo -e "${GREEN}✓ 已备份配置到: $BACKUP_FILE${NC}"
    
    # 使用 sed 修改配置
    sed -i 's/"use_keyless":[[:space:]]*true/"use_keyless": false/' "$CONFIG_FILE"
    
    # 检查是否已有 use_standard_auth，如果没有则添加
    if ! grep -q '"use_standard_auth"' "$CONFIG_FILE"; then
        sed -i '/"use_keyless":[[:space:]]*false/a\
  "use_standard_auth": true,' "$CONFIG_FILE"
    else
        sed -i 's/"use_standard_auth":[[:space:]]*false/"use_standard_auth": true/' "$CONFIG_FILE"
    fi
    
    # 更新 auth 配置
    sed -i '/"auth":/,/},/ c\
  "auth": {\
    "auth_header_name": "X-API-Key",\
    "use_param": false,\
    "use_cookie": false,\
    "use_certificate": false\
  },' "$CONFIG_FILE"
    
    echo -e "${GREEN}✓ 配置已更新为 API Key 认证模式${NC}"
    
    # 重启 Tyk Gateway
    restart_tyk
    
    # 显示示例密钥创建命令
    echo -e "\n${YELLOW}创建测试 API Key:${NC}"
    echo "curl -X POST http://localhost:9696/tyk/keys/my-test-key \\"
    echo "  -H 'x-tyk-authorization: nsp-tyk-secret-key-2025' \\"
    echo "  -H 'Content-Type: application/json' \\"
    echo "  -d '{"
    echo "    \"allowance\": 1000,"
    echo "    \"rate\": 1000,"
    echo "    \"per\": 60,"
    echo "    \"access_rights\": {"
    echo "      \"nsp-top-api-v1\": {"
    echo "        \"api_id\": \"nsp-top-api-v1\","
    echo "        \"versions\": [\"Default\"]"
    echo "      }"
    echo "    }"
    echo "  }'"
    echo ""
    echo -e "${YELLOW}使用 API Key 访问:${NC}"
    echo "curl -H 'X-API-Key: my-test-key' http://localhost:9696/api/v1/health"
}

# 重启 Tyk Gateway
restart_tyk() {
    echo -e "\n${YELLOW}正在重启 Tyk Gateway...${NC}"
    docker-compose restart tyk-gateway > /dev/null 2>&1
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}✓ Tyk Gateway 已重启${NC}"
        sleep 3
        
        # 验证服务状态
        if docker-compose ps tyk-gateway | grep -q "Up"; then
            echo -e "${GREEN}✓ Tyk Gateway 运行正常${NC}"
        else
            echo -e "${RED}✗ Tyk Gateway 启动失败${NC}"
            echo "查看日志: docker-compose logs tyk-gateway"
        fi
    else
        echo -e "${RED}✗ 重启失败${NC}"
    fi
}

# 恢复备份
restore_backup() {
    if [ -f "$BACKUP_FILE" ]; then
        echo -e "${YELLOW}正在恢复备份配置...${NC}"
        cp "$BACKUP_FILE" "$CONFIG_FILE"
        echo -e "${GREEN}✓ 配置已恢复${NC}"
        restart_tyk
    else
        echo -e "${RED}错误: 备份文件不存在: $BACKUP_FILE${NC}"
        exit 1
    fi
}

# 快速测试
quick_test() {
    echo -e "\n${YELLOW}=== 快速测试 ===${NC}"
    
    echo -e "\n测试 1: 访问健康检查接口"
    echo "命令: curl http://localhost:9696/api/v1/health"
    
    RESPONSE=$(curl -s -w "\n%{http_code}" http://localhost:9696/api/v1/health 2>/dev/null)
    HTTP_CODE=$(echo "$RESPONSE" | tail -n 1)
    BODY=$(echo "$RESPONSE" | head -n -1)
    
    if [ "$HTTP_CODE" = "200" ]; then
        echo -e "${GREEN}✓ 成功 (HTTP 200) - 开放访问模式${NC}"
        echo "响应: $BODY"
    elif [ "$HTTP_CODE" = "401" ] || [ "$HTTP_CODE" = "403" ]; then
        echo -e "${YELLOW}✓ 拒绝 (HTTP $HTTP_CODE) - 认证模式已启用${NC}"
        echo "需要提供 API Key 才能访问"
    else
        echo -e "${RED}✗ 异常状态 (HTTP $HTTP_CODE)${NC}"
        echo "响应: $BODY"
    fi
}

# 显示帮助
show_help() {
    echo "Tyk API Gateway 认证模式切换工具"
    echo ""
    echo "用法:"
    echo "  $0 [命令]"
    echo ""
    echo "命令:"
    echo "  keyless    切换到开放访问模式（无需认证）"
    echo "  auth       切换到 API Key 认证模式"
    echo "  status     显示当前配置状态"
    echo "  restore    恢复上次备份的配置"
    echo "  test       快速测试当前配置"
    echo "  help       显示此帮助信息"
    echo ""
    echo "示例:"
    echo "  $0 keyless    # 切换到开放访问"
    echo "  $0 auth       # 切换到认证模式"
    echo "  $0 test       # 测试当前配置"
    echo ""
}

# 主逻辑
case "$1" in
    keyless)
        enable_keyless
        show_status
        quick_test
        ;;
    auth)
        enable_auth
        show_status
        ;;
    status)
        show_status
        quick_test
        ;;
    restore)
        restore_backup
        show_status
        ;;
    test)
        quick_test
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        echo -e "${RED}错误: 未知命令 '$1'${NC}"
        echo ""
        show_help
        exit 1
        ;;
esac
