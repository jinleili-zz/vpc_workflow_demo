#!/bin/bash

echo "========================================="
echo "Top NSP 测试"
echo "========================================="

# 1. 检查Top NSP健康状态
echo ""
echo "1. 检查Top NSP健康状态..."
curl -s http://localhost:8080/api/v1/health | python3 -m json.tool 2>/dev/null

# 2. 列出所有Region
echo ""
echo "2. 列出所有Region..."
curl -s http://localhost:8080/api/v1/regions | python3 -m json.tool 2>/dev/null

echo ""
echo "========================================="
echo "Top NSP测试完成"
echo "========================================="
