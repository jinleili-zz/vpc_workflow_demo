#!/bin/bash

# VPC并发测试 - 简化版（不依赖parallel）
# 适用于没有安装GNU parallel的环境

set -e

TOP_NSP_ADDR="http://localhost:8080"
VPC_COUNT=100
REGION="cn-beijing"

LOG_DIR="./test_logs_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$LOG_DIR"

echo "========================================"
echo "VPC并发创建测试 (简化版)"
echo "========================================"
echo "VPC数量: $VPC_COUNT"
echo "日志目录: $LOG_DIR"
echo "========================================"
echo ""

# 阶段1: 并发创建VPC
echo "[阶段1] 并发创建 $VPC_COUNT 个VPC"
echo ""

create_start=$(date +%s)

for i in $(seq 1 $VPC_COUNT); do
    vpc_name="test-vpc-$(printf "%03d" $i)"
    vrf_name="VRF-$(printf "%03d" $i)"
    vlan_id=$((100 + $i))
    firewall_zone="zone-$(printf "%03d" $i)"
    
    # 后台并发执行
    (
        response=$(curl -s -X POST "${TOP_NSP_ADDR}/api/v1/vpc" \
            -H "Content-Type: application/json" \
            -d "{\"vpc_name\":\"${vpc_name}\",\"region\":\"${REGION}\",\"vrf_name\":\"${vrf_name}\",\"vlan_id\":${vlan_id},\"firewall_zone\":\"${firewall_zone}\"}")
        
        success=$(echo "$response" | grep -o '"success":true' || echo "")
        
        if [ -n "$success" ]; then
            echo "✓ $vpc_name 创建请求成功"
            echo "$vpc_name|success" >> "${LOG_DIR}/create_results.txt"
        else
            echo "✗ $vpc_name 创建请求失败"
            echo "$vpc_name|failed" >> "${LOG_DIR}/create_results.txt"
        fi
    ) &
    
    # 控制并发数（每20个等待一下）
    if [ $((i % 20)) -eq 0 ]; then
        wait
    fi
done

wait  # 等待所有后台任务完成

create_end=$(date +%s)
create_duration=$((create_end - create_start))

echo ""
echo "创建阶段完成，耗时: ${create_duration}s"
echo ""

# 统计创建结果
if [ -f "${LOG_DIR}/create_results.txt" ]; then
    total=$(wc -l < "${LOG_DIR}/create_results.txt")
    success=$(grep -c "|success" "${LOG_DIR}/create_results.txt" || echo 0)
    failed=$(grep -c "|failed" "${LOG_DIR}/create_results.txt" || echo 0)
    
    echo "创建统计:"
    echo "  总数: $total"
    echo "  成功: $success"
    echo "  失败: $failed"
fi

# 阶段2: 等待执行
echo ""
echo "[阶段2] 等待VPC任务执行 (30秒)"
echo "提示: 100个VPC，每个3个任务，每个任务2秒，worker并发执行中..."
sleep 30

# 阶段3: 查询状态
echo ""
echo "[阶段3] 查询VPC状态"
echo ""

query_start=$(date +%s)

for i in $(seq 1 $VPC_COUNT); do
    vpc_name="test-vpc-$(printf "%03d" $i)"
    
    (
        response=$(curl -s "${TOP_NSP_ADDR}/api/v1/vpc/${vpc_name}/status")
        status=$(echo "$response" | grep -o '"overall_status":"[^"]*"' | cut -d'"' -f4 || echo "unknown")
        
        echo "$vpc_name|$status" >> "${LOG_DIR}/query_results.txt"
        
        case "$status" in
            "running") echo "✓ $vpc_name: running" ;;
            "creating") echo "⊙ $vpc_name: creating" ;;
            "failed") echo "✗ $vpc_name: failed" ;;
            *) echo "? $vpc_name: $status" ;;
        esac
    ) &
    
    if [ $((i % 20)) -eq 0 ]; then
        wait
    fi
done

wait

query_end=$(date +%s)
query_duration=$((query_end - query_start))

echo ""
echo "查询阶段完成，耗时: ${query_duration}s"
echo ""

# 阶段4: 统计结果
echo "========================================"
echo "测试结果汇总"
echo "========================================"
echo ""

if [ -f "${LOG_DIR}/query_results.txt" ]; then
    # 去重并统计
    sort -u "${LOG_DIR}/query_results.txt" > "${LOG_DIR}/query_results_unique.txt"
    
    total_query=$(wc -l < "${LOG_DIR}/query_results_unique.txt")
    running=$(grep -c "|running" "${LOG_DIR}/query_results_unique.txt" 2>/dev/null || echo 0)
    creating=$(grep -c "|creating" "${LOG_DIR}/query_results_unique.txt" 2>/dev/null || echo 0)
    failed_status=$(grep -c "|failed" "${LOG_DIR}/query_results_unique.txt" 2>/dev/null || echo 0)
    unknown=$(grep -c "|unknown" "${LOG_DIR}/query_results_unique.txt" 2>/dev/null || echo 0)
    
    echo "VPC状态分布:"
    echo "  Running (完成): $running"
    echo "  Creating (进行中): $creating"
    echo "  Failed (失败): $failed_status"
    echo "  Unknown (未知): $unknown"
    echo ""
    
    if [ $total_query -gt 0 ]; then
        success_rate=$((running * 100 / total_query))
        echo "成功率: ${success_rate}%"
    fi
fi

echo ""
echo "性能指标:"
echo "  创建耗时: ${create_duration}s"
echo "  查询耗时: ${query_duration}s"

if [ $create_duration -gt 0 ]; then
    throughput=$((VPC_COUNT / create_duration))
    echo "  创建吞吐量: ${throughput} VPC/s"
fi

echo ""
echo "日志文件:"
echo "  ${LOG_DIR}/create_results.txt"
echo "  ${LOG_DIR}/query_results.txt"
echo ""
echo "========================================"

if [ "$running" -eq "$VPC_COUNT" ]; then
    echo "✓ 测试通过: 所有VPC创建成功"
    exit 0
else
    echo "⚠ 测试未完全通过: $running/$VPC_COUNT VPC完成"
    exit 1
fi