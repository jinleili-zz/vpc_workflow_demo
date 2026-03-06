#!/bin/bash

# VPC并发创建测试脚本
# 功能：并发创建100个VPC，并查询创建结果

set -e

# 配置参数
TOP_NSP_ADDR="http://localhost:8080"
VPC_COUNT=100
CONCURRENT_JOBS=20  # 并发数
REGION="cn-beijing"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志目录
LOG_DIR="./test_logs_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$LOG_DIR"

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}VPC并发创建测试${NC}"
echo -e "${BLUE}========================================${NC}"
echo -e "测试参数："
echo -e "  Top NSP地址: ${TOP_NSP_ADDR}"
echo -e "  VPC数量: ${VPC_COUNT}"
echo -e "  并发数: ${CONCURRENT_JOBS}"
echo -e "  日志目录: ${LOG_DIR}"
echo -e "${BLUE}========================================${NC}\n"

# 创建VPC函数
create_vpc() {
    local vpc_id=$1
    local vpc_name="test-vpc-$(printf "%03d" $vpc_id)"
    local vrf_name="VRF-$(printf "%03d" $vpc_id)"
    local vlan_id=$((100 + $vpc_id))
    local firewall_zone="zone-$(printf "%03d" $vpc_id)"
    
    local start_time=$(date +%s.%N)
    
    # 发送创建请求
    local response=$(curl -s -X POST "${TOP_NSP_ADDR}/api/v1/vpc" \
        -H "Content-Type: application/json" \
        -d "{\"vpc_name\":\"${vpc_name}\",\"region\":\"${REGION}\",\"vrf_name\":\"${vrf_name}\",\"vlan_id\":${vlan_id},\"firewall_zone\":\"${firewall_zone}\"}" \
        2>&1)
    
    local end_time=$(date +%s.%N)
    local duration=$(echo "$end_time - $start_time" | bc)
    
    # 解析响应
    local success=$(echo "$response" | jq -r '.success // false' 2>/dev/null || echo "false")
    
    if [ "$success" == "true" ]; then
        echo -e "${GREEN}✓${NC} VPC ${vpc_name} 创建成功 (耗时: ${duration}s)"
        echo "$vpc_name|success|$duration" >> "${LOG_DIR}/create_results.txt"
    else
        echo -e "${RED}✗${NC} VPC ${vpc_name} 创建失败"
        echo "$vpc_name|failed|$duration|$response" >> "${LOG_DIR}/create_results.txt"
    fi
}

# 查询VPC状态函数
query_vpc_status() {
    local vpc_name=$1
    
    local response=$(curl -s "${TOP_NSP_ADDR}/api/v1/vpc/${vpc_name}/status" 2>&1)
    local overall_status=$(echo "$response" | jq -r '.overall_status // "unknown"' 2>/dev/null || echo "unknown")
    
    echo "$vpc_name|$overall_status|$response" >> "${LOG_DIR}/query_results.txt"
    
    case "$overall_status" in
        "running")
            echo -e "${GREEN}✓${NC} $vpc_name: running"
            return 0
            ;;
        "creating")
            echo -e "${YELLOW}⊙${NC} $vpc_name: creating"
            return 1
            ;;
        "failed")
            echo -e "${RED}✗${NC} $vpc_name: failed"
            return 2
            ;;
        *)
            echo -e "${RED}?${NC} $vpc_name: $overall_status"
            return 3
            ;;
    esac
}

# 阶段1: 并发创建VPC
echo -e "\n${BLUE}[阶段1] 开始并发创建 ${VPC_COUNT} 个VPC (并发数: ${CONCURRENT_JOBS})${NC}\n"

create_start_time=$(date +%s)

export -f create_vpc
export TOP_NSP_ADDR REGION LOG_DIR RED GREEN YELLOW BLUE NC

# 使用GNU parallel或xargs进行并发
if command -v parallel &> /dev/null; then
    echo "使用GNU parallel进行并发创建"
    seq 1 $VPC_COUNT | parallel -j $CONCURRENT_JOBS create_vpc {}
else
    echo "使用xargs进行并发创建"
    seq 1 $VPC_COUNT | xargs -I {} -P $CONCURRENT_JOBS bash -c "create_vpc {}"
fi

create_end_time=$(date +%s)
create_duration=$((create_end_time - create_start_time))

echo -e "\n${GREEN}创建阶段完成，总耗时: ${create_duration}s${NC}\n"

# 统计创建结果
if [ -f "${LOG_DIR}/create_results.txt" ]; then
    total_created=$(wc -l < "${LOG_DIR}/create_results.txt")
    success_count=$(grep -c "|success|" "${LOG_DIR}/create_results.txt" || echo 0)
    failed_count=$(grep -c "|failed|" "${LOG_DIR}/create_results.txt" || echo 0)
    
    echo -e "${BLUE}创建结果统计:${NC}"
    echo -e "  总数: ${total_created}"
    echo -e "  ${GREEN}成功: ${success_count}${NC}"
    echo -e "  ${RED}失败: ${failed_count}${NC}"
else
    echo -e "${RED}未找到创建结果文件${NC}"
    exit 1
fi

# 阶段2: 等待任务执行完成
echo -e "\n${BLUE}[阶段2] 等待VPC创建任务执行完成${NC}"
echo -e "等待时间: 每个VPC约需8-10秒 (3个顺序任务，每个2秒)\n"

# 计算预期等待时间
expected_wait=$((10))  # 因为是并发，只需等待最长的一批
echo -e "预计等待: ${expected_wait}秒\n"

sleep $expected_wait

# 阶段3: 查询所有VPC状态
echo -e "\n${BLUE}[阶段3] 查询所有VPC创建状态${NC}\n"

query_start_time=$(date +%s)

export -f query_vpc_status

# 生成VPC名称列表
for i in $(seq 1 $VPC_COUNT); do
    echo "test-vpc-$(printf "%03d" $i)"
done > "${LOG_DIR}/vpc_list.txt"

# 并发查询状态
if command -v parallel &> /dev/null; then
    cat "${LOG_DIR}/vpc_list.txt" | parallel -j $CONCURRENT_JOBS query_vpc_status {}
else
    cat "${LOG_DIR}/vpc_list.txt" | xargs -I {} -P $CONCURRENT_JOBS bash -c "query_vpc_status {}"
fi

query_end_time=$(date +%s)
query_duration=$((query_end_time - query_start_time))

echo -e "\n${GREEN}查询阶段完成，总耗时: ${query_duration}s${NC}\n"

# 阶段4: 统计最终结果
echo -e "\n${BLUE}========================================${NC}"
echo -e "${BLUE}测试结果汇总${NC}"
echo -e "${BLUE}========================================${NC}\n"

if [ -f "${LOG_DIR}/query_results.txt" ]; then
    total_queried=$(wc -l < "${LOG_DIR}/query_results.txt")
    running_count=$(grep -c "|running|" "${LOG_DIR}/query_results.txt" || echo 0)
    creating_count=$(grep -c "|creating|" "${LOG_DIR}/query_results.txt" || echo 0)
    failed_query_count=$(grep -c "|failed|" "${LOG_DIR}/query_results.txt" || echo 0)
    unknown_count=$(grep -c "|unknown|" "${LOG_DIR}/query_results.txt" || echo 0)
    
    echo -e "VPC状态分布:"
    echo -e "  ${GREEN}Running (完成): ${running_count}${NC}"
    echo -e "  ${YELLOW}Creating (进行中): ${creating_count}${NC}"
    echo -e "  ${RED}Failed (失败): ${failed_query_count}${NC}"
    echo -e "  Unknown (未知): ${unknown_count}"
    echo -e ""
    
    success_rate=$(echo "scale=2; $running_count * 100 / $total_queried" | bc)
    echo -e "成功率: ${GREEN}${success_rate}%${NC}"
else
    echo -e "${RED}未找到查询结果文件${NC}"
fi

# 性能指标
echo -e "\n性能指标:"
echo -e "  创建阶段耗时: ${create_duration}s"
echo -e "  查询阶段耗时: ${query_duration}s"
echo -e "  总测试时间: $((create_duration + expected_wait + query_duration))s"

if [ $success_count -gt 0 ]; then
    avg_create_time=$(awk -F'|' '{sum+=$3; count++} END {printf "%.2f", sum/count}' "${LOG_DIR}/create_results.txt")
    echo -e "  平均创建API响应时间: ${avg_create_time}s"
    
    throughput=$(echo "scale=2; $VPC_COUNT / $create_duration" | bc)
    echo -e "  创建吞吐量: ${throughput} VPC/s"
fi

# 生成详细报告
echo -e "\n详细日志文件:"
echo -e "  创建结果: ${LOG_DIR}/create_results.txt"
echo -e "  查询结果: ${LOG_DIR}/query_results.txt"

# 如果有失败的，显示失败详情
if [ $failed_count -gt 0 ] || [ $failed_query_count -gt 0 ]; then
    echo -e "\n${RED}发现失败案例，请检查日志文件获取详情${NC}"
fi

echo -e "\n${BLUE}========================================${NC}"
echo -e "${BLUE}测试完成${NC}"
echo -e "${BLUE}========================================${NC}\n"

# 返回状态码
if [ $running_count -eq $VPC_COUNT ]; then
    echo -e "${GREEN}✓ 所有VPC创建成功${NC}\n"
    exit 0
else
    echo -e "${YELLOW}⚠ 部分VPC创建未完成或失败${NC}\n"
    exit 1
fi
