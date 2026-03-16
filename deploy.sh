#!/bin/bash
#
# NSP VPC Workflow Demo - 自动部署脚本
# 用法: ./deploy.sh [options]
#   --skip-build    跳过构建步骤
#   --skip-test     跳过测试步骤
#   --clean         完全清理（停止服务、清空数据）后退出
#   -h, --help      显示帮助信息
#

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="$SCRIPT_DIR/logs"
BIN_DIR="$SCRIPT_DIR/bin"

# 服务端口配置
TOP_NSP_PORT=9080
AZ_NSP_1A_PORT=9081
AZ_NSP_1B_PORT=9082
AZ_NSP_SH_PORT=9083

# 数据库配置
PG_USER="nsp_user"
PG_PASS="nsp_password"
DATABASES=(
    "top_nsp_vpc"
    "nsp_cn_beijing_1a_vpc"
    "nsp_cn_beijing_1b_vpc"
    "nsp_cn_shanghai_1a_vpc"
)

# 参数解析
SKIP_BUILD=false
SKIP_TEST=false
CLEAN_ONLY=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        --skip-test)
            SKIP_TEST=true
            shift
            ;;
        --clean)
            CLEAN_ONLY=true
            shift
            ;;
        -h|--help)
            echo "用法: ./deploy.sh [options]"
            echo "  --skip-build    跳过构建步骤"
            echo "  --skip-test     跳过测试步骤"
            echo "  --clean         完全清理后退出"
            echo "  -h, --help      显示帮助信息"
            exit 0
            ;;
        *)
            echo -e "${RED}未知参数: $1${NC}"
            exit 1
            ;;
    esac
done

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_section() {
    echo ""
    echo -e "${GREEN}=========================================${NC}"
    echo -e "${GREEN} $1${NC}"
    echo -e "${GREEN}=========================================${NC}"
}

# 停止所有服务
stop_services() {
    log_info "停止所有服务..."
    
    # 停止 NSP 服务
    pkill -f "top_nsp" 2>/dev/null || true
    pkill -f "az_nsp" 2>/dev/null || true
    pkill -f "bin/worker" 2>/dev/null || true
    
    # 等待进程退出
    sleep 2
    
    # 强制清理残留进程
    for port in $TOP_NSP_PORT $AZ_NSP_1A_PORT $AZ_NSP_1B_PORT $AZ_NSP_SH_PORT; do
        pid=$(lsof -ti:$port 2>/dev/null) || true
        if [ -n "$pid" ]; then
            kill -9 $pid 2>/dev/null || true
        fi
    done
    
    log_success "服务已停止"
}

# 检查依赖
check_dependencies() {
    log_section "检查依赖条件"
    
    local has_error=false
    
    # 检查 Go
    if command -v go &> /dev/null; then
        GO_VERSION=$(go version | awk '{print $3}')
        log_success "Go: $GO_VERSION"
    else
        log_error "Go 未安装"
        has_error=true
    fi
    
    # 检查 Redis
    if command -v redis-cli &> /dev/null; then
        log_success "redis-cli: 已安装"
    else
        log_error "redis-cli 未安装"
        has_error=true
    fi
    
    # 检查 PostgreSQL
    if command -v psql &> /dev/null; then
        log_success "psql: 已安装"
    else
        log_error "psql 未安装"
        has_error=true
    fi
    
    # 检查 curl
    if command -v curl &> /dev/null; then
        log_success "curl: 已安装"
    else
        log_error "curl 未安装"
        has_error=true
    fi
    
    if [ "$has_error" = true ]; then
        log_error "依赖检查失败，请安装缺失的依赖"
        exit 1
    fi
}

# 启动 Redis
start_redis() {
    log_section "检查 Redis"
    
    if redis-cli ping &> /dev/null; then
        log_success "Redis 已运行"
    else
        log_info "启动 Redis..."
        redis-server --daemonize yes
        sleep 2
        if redis-cli ping &> /dev/null; then
            log_success "Redis 启动成功"
        else
            log_error "Redis 启动失败"
            exit 1
        fi
    fi
}

# 检查 PostgreSQL
check_postgres() {
    log_section "检查 PostgreSQL"
    
    if pg_isready -h localhost -p 5432 &> /dev/null; then
        log_success "PostgreSQL 已就绪"
    else
        log_error "PostgreSQL 未运行，请先启动 PostgreSQL"
        exit 1
    fi
}

# 清空并初始化数据库
init_databases() {
    log_section "初始化数据库"
    
    # 清空 Redis
    log_info "清空 Redis..."
    redis-cli FLUSHALL > /dev/null
    log_success "Redis 数据已清空"
    
    # SAGA 迁移脚本路径
    SAGA_MIGRATION="$(dirname "$SCRIPT_DIR")/nsp_platform/nsp-common/migrations/saga.sql"
    
    # 创建/重置 PostgreSQL 数据库
    for db in "${DATABASES[@]}"; do
        log_info "初始化数据库: $db"
        
        # 删除旧数据库（如果存在）
        sudo -u postgres psql -c "DROP DATABASE IF EXISTS $db;" 2>/dev/null || true
        
        # 创建新数据库
        sudo -u postgres psql -c "CREATE DATABASE $db OWNER $PG_USER;" 2>/dev/null || {
            log_warn "数据库 $db 可能已存在，尝试继续..."
        }
        
        # 执行业务表迁移
        PGPASSWORD=$PG_PASS psql -h localhost -U $PG_USER -d $db \
            -f "$SCRIPT_DIR/internal/db/migrations/001_init_postgresql.sql" \
            > /dev/null 2>&1
        
        # 执行 SAGA 表迁移（仅 Top NSP 数据库需要）
        if [ "$db" = "top_nsp_vpc" ] && [ -f "$SAGA_MIGRATION" ]; then
            PGPASSWORD=$PG_PASS psql -h localhost -U $PG_USER -d $db \
                -f "$SAGA_MIGRATION" > /dev/null 2>&1
            log_info "  SAGA 表已创建"
        fi
        
        log_success "数据库 $db 初始化完成"
    done
}

# 构建项目
build_project() {
    log_section "构建项目"
    
    cd "$SCRIPT_DIR"
    
    # 更新依赖
    log_info "更新 Go 模块依赖..."
    go mod tidy
    
    # 创建 bin 目录
    mkdir -p "$BIN_DIR"
    
    # 构建各组件
    log_info "构建 Top NSP..."
    go build -o "$BIN_DIR/top_nsp" ./cmd/top_nsp
    log_success "Top NSP 构建完成"
    
    log_info "构建 AZ NSP..."
    go build -o "$BIN_DIR/az_nsp" ./cmd/az_nsp
    log_success "AZ NSP 构建完成"
    
    log_info "构建 Worker..."
    go build -o "$BIN_DIR/worker" ./cmd/worker
    log_success "Worker 构建完成"
}

# 启动服务
start_services() {
    log_section "启动服务"
    
    cd "$SCRIPT_DIR"
    mkdir -p "$LOG_DIR"
    
    # 启动 Top NSP
    log_info "启动 Top NSP (端口: $TOP_NSP_PORT)..."
    SERVICE_TYPE=top PORT=$TOP_NSP_PORT \
        "$BIN_DIR/top_nsp" > "$LOG_DIR/top_nsp.log" 2>&1 &
    sleep 3
    
    if curl -s "http://localhost:$TOP_NSP_PORT/api/v1/health" | grep -q "ok"; then
        log_success "Top NSP 启动成功"
    else
        log_error "Top NSP 启动失败，查看日志: $LOG_DIR/top_nsp.log"
        tail -20 "$LOG_DIR/top_nsp.log"
        exit 1
    fi
    
    # 启动 AZ NSP 实例
    declare -A AZ_CONFIGS=(
        ["cn-beijing-1a"]="$AZ_NSP_1A_PORT"
        ["cn-beijing-1b"]="$AZ_NSP_1B_PORT"
        ["cn-shanghai-1a"]="$AZ_NSP_SH_PORT"
    )
    
    for az in "${!AZ_CONFIGS[@]}"; do
        port="${AZ_CONFIGS[$az]}"
        region=$(echo $az | sed 's/-[0-9]*[a-z]$//')
        log_name=$(echo $az | tr '-' '_')
        
        log_info "启动 AZ NSP $az (端口: $port)..."
        SERVICE_TYPE=az \
            REGION=$region \
            AZ=$az \
            PORT=$port \
            TOP_NSP_ADDR="http://localhost:$TOP_NSP_PORT" \
            NSP_ADDR="http://localhost:$port" \
            "$BIN_DIR/az_nsp" > "$LOG_DIR/az_nsp_$log_name.log" 2>&1 &
    done
    
    # 等待 AZ NSP 启动
    sleep 5
    
    # 验证 AZ NSP
    for az in "${!AZ_CONFIGS[@]}"; do
        port="${AZ_CONFIGS[$az]}"
        if curl -s "http://localhost:$port/api/v1/health" | grep -q "ok"; then
            log_success "AZ NSP $az 启动成功"
        else
            log_error "AZ NSP $az 启动失败"
            exit 1
        fi
    done
    
    # 启动 Workers
    log_info "启动 Workers..."
    
    for az in "cn-beijing-1a" "cn-beijing-1b"; do
        region=$(echo $az | sed 's/-[0-9]*[a-z]$//')
        log_name=$(echo $az | tr '-' '_')
        
        for worker_type in "switch" "firewall"; do
            REGION=$region AZ=$az WORKER_TYPE=$worker_type \
                "$BIN_DIR/worker" > "$LOG_DIR/worker_${log_name}_${worker_type}.log" 2>&1 &
        done
    done
    
    sleep 3
    log_success "Workers 启动完成"
}

# 验证 AZ 注册
verify_az_registration() {
    log_section "验证 AZ 注册"
    
    local max_retries=10
    local retry=0
    
    while [ $retry -lt $max_retries ]; do
        az_count=$(curl -s "http://localhost:$TOP_NSP_PORT/api/v1/regions/cn-beijing/azs" | \
            grep -o '"status":"online"' | wc -l)
        
        if [ "$az_count" -ge 2 ]; then
            log_success "cn-beijing: $az_count 个 AZ 已注册"
            break
        fi
        
        retry=$((retry + 1))
        log_info "等待 AZ 注册... ($retry/$max_retries)"
        sleep 3
    done
    
    if [ $retry -eq $max_retries ]; then
        log_error "AZ 注册超时"
        exit 1
    fi
}

# 运行端到端测试
run_e2e_tests() {
    log_section "运行端到端测试"
    
    local test_passed=true
    local vpc_name="vpc-e2e-$(date +%s)"
    local subnet_name="subnet-e2e-$(date +%s)"
    
    # 测试 1: 健康检查
    log_info "测试 1: Top NSP 健康检查"
    if curl -s "http://localhost:$TOP_NSP_PORT/api/v1/health" | grep -q "ok"; then
        log_success "健康检查通过"
    else
        log_error "健康检查失败"
        test_passed=false
    fi
    
    # 测试 2: 查询 Region 列表
    log_info "测试 2: 查询 Region 列表"
    if curl -s "http://localhost:$TOP_NSP_PORT/api/v1/regions" | grep -q "cn-beijing"; then
        log_success "Region 列表查询通过"
    else
        log_error "Region 列表查询失败"
        test_passed=false
    fi
    
    # 测试 3: 创建 VPC
    log_info "测试 3: 创建 Region 级 VPC ($vpc_name)"
    vpc_resp=$(curl -s -X POST "http://localhost:$TOP_NSP_PORT/api/v1/vpc" \
        -H "Content-Type: application/json" \
        -d "{\"vpc_name\":\"$vpc_name\",\"region\":\"cn-beijing\",\"vrf_name\":\"VRF-TEST\",\"vlan_id\":100,\"firewall_zone\":\"trust\"}")
    
    if echo "$vpc_resp" | grep -q '"success":true'; then
        log_success "VPC 创建请求成功"
    else
        log_error "VPC 创建请求失败: $vpc_resp"
        test_passed=false
    fi
    
    # 等待 VPC 创建完成
    log_info "等待 VPC 创建完成..."
    sleep 20
    
    # 测试 4: 查询 VPC 状态
    log_info "测试 4: 查询 VPC 状态"
    vpc_status=$(curl -s "http://localhost:$TOP_NSP_PORT/api/v1/vpc/$vpc_name/status")
    
    completed_1a=$(echo "$vpc_status" | grep -o '"cn-beijing-1a":{[^}]*"completed":[0-9]*' | grep -o '"completed":[0-9]*' | grep -o '[0-9]*')
    completed_1b=$(echo "$vpc_status" | grep -o '"cn-beijing-1b":{[^}]*"completed":[0-9]*' | grep -o '"completed":[0-9]*' | grep -o '[0-9]*')
    
    if [ "${completed_1a:-0}" -ge 3 ] && [ "${completed_1b:-0}" -ge 3 ]; then
        log_success "VPC 在两个 AZ 中创建完成 (1a: $completed_1a/3, 1b: $completed_1b/3)"
    else
        log_warn "VPC 创建可能未完成 (1a: ${completed_1a:-0}/3, 1b: ${completed_1b:-0}/3)"
    fi
    
    # 测试 5: 创建子网
    log_info "测试 5: 创建 AZ 级子网 ($subnet_name)"
    subnet_resp=$(curl -s -X POST "http://localhost:$TOP_NSP_PORT/api/v1/subnet" \
        -H "Content-Type: application/json" \
        -d "{\"subnet_name\":\"$subnet_name\",\"vpc_name\":\"$vpc_name\",\"region\":\"cn-beijing\",\"az\":\"cn-beijing-1a\",\"cidr\":\"10.0.1.0/24\"}")
    
    if echo "$subnet_resp" | grep -q '"success":true'; then
        log_success "子网创建请求成功"
    else
        log_error "子网创建请求失败: $subnet_resp"
        test_passed=false
    fi
    
    # 等待子网创建
    sleep 10
    
    # 测试 6: 查询子网状态
    log_info "测试 6: 查询子网状态"
    subnet_status=$(curl -s "http://localhost:$AZ_NSP_1A_PORT/api/v1/subnet/$subnet_name/status")
    
    if echo "$subnet_status" | grep -q '"status":"running"'; then
        log_success "子网创建完成"
    else
        log_warn "子网状态: $(echo "$subnet_status" | grep -o '"status":"[^"]*"')"
    fi
    
    # 测试总结
    echo ""
    if [ "$test_passed" = true ]; then
        log_success "所有测试通过!"
    else
        log_error "部分测试失败"
        exit 1
    fi
}

# 显示服务信息
show_service_info() {
    log_section "服务信息"
    
    echo -e "服务地址:"
    echo -e "  Top NSP:         ${GREEN}http://localhost:$TOP_NSP_PORT${NC}"
    echo -e "  AZ NSP 1a:       ${GREEN}http://localhost:$AZ_NSP_1A_PORT${NC}  (cn-beijing-1a)"
    echo -e "  AZ NSP 1b:       ${GREEN}http://localhost:$AZ_NSP_1B_PORT${NC}  (cn-beijing-1b)"
    echo -e "  AZ NSP Shanghai: ${GREEN}http://localhost:$AZ_NSP_SH_PORT${NC}  (cn-shanghai-1a)"
    echo ""
    echo -e "日志目录: ${BLUE}$LOG_DIR${NC}"
    echo ""
    echo -e "常用命令:"
    echo -e "  查看日志:  tail -f $LOG_DIR/top_nsp.log"
    echo -e "  停止服务:  $SCRIPT_DIR/deploy.sh --clean"
    echo -e "  健康检查:  curl http://localhost:$TOP_NSP_PORT/api/v1/health"
}

# 主流程
main() {
    echo ""
    echo -e "${GREEN}╔═══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║     NSP VPC Workflow Demo - 自动部署脚本              ║${NC}"
    echo -e "${GREEN}╚═══════════════════════════════════════════════════════╝${NC}"
    echo ""
    
    # 完全清理模式
    if [ "$CLEAN_ONLY" = true ]; then
        stop_services
        log_info "清空 Redis..."
        redis-cli FLUSHALL > /dev/null 2>&1 || true
        log_success "清理完成"
        exit 0
    fi
    
    # 检查依赖
    check_dependencies
    
    # 停止已有服务
    stop_services
    
    # 启动基础设施
    start_redis
    check_postgres
    
    # 初始化数据库
    init_databases
    
    # 构建项目
    if [ "$SKIP_BUILD" = false ]; then
        build_project
    else
        log_warn "跳过构建步骤"
    fi
    
    # 启动服务
    start_services
    
    # 验证 AZ 注册
    verify_az_registration
    
    # 运行测试
    if [ "$SKIP_TEST" = false ]; then
        run_e2e_tests
    else
        log_warn "跳过测试步骤"
    fi
    
    # 显示服务信息
    show_service_info
}

# 执行主流程
main
