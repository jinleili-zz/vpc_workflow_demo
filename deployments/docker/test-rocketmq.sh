#!/bin/bash

# 快速测试RocketMQ版本

set -e

echo "=========================================="
echo "RocketMQ版本快速测试"
echo "=========================================="

cd "$(dirname "$0")"

# 1. 启动基础服务
echo "1. 启动基础服务（Redis、RocketMQ）..."
docker-compose up -d redis rocketmq-namesrv rocketmq-broker

echo "等待服务启动..."
sleep 15

# 2. 启动1个AZ NSP
echo "2. 启动AZ NSP（cn-beijing-1a）..."
docker-compose up -d az-nsp-cn-beijing-1a

echo "等待AZ NSP启动..."
sleep 5

# 3. 启动对应的Workers
echo "3. 启动Workers（cn-beijing-1a）..."
docker-compose up -d switch-worker-bj-1a firewall-worker-bj-1a

echo "等待Workers启动..."
sleep 5

# 4. 查看服务状态
echo ""
echo "=========================================="
echo "服务状态:"
echo "=========================================="
docker-compose ps

echo ""
echo "=========================================="
echo "查看RocketMQ Topic列表..."
echo "=========================================="
docker exec rmqbroker sh mqadmin topicList -n rmqnamesrv:9876 || true

echo ""
echo "=========================================="
echo "环境准备完成！"
echo "=========================================="
echo ""
echo "测试命令："
echo "  1. 创建VPC:"
echo "     curl -X POST http://localhost:8080/api/v1/vpc \\"
echo "       -H 'Content-Type: application/json' \\"
echo "       -d '{\"vpc_name\":\"test-vpc\",\"vrf_name\":\"vrf-test\",\"vlan_id\":100,\"firewall_zone\":\"dmz\"}'"
echo ""
echo "  2. 查看Worker日志:"
echo "     docker logs -f switch-worker-cn-beijing-1a"
echo "     docker logs -f firewall-worker-cn-beijing-1a"
echo ""
echo "  3. 查看AZ NSP日志:"
echo "     docker logs -f az-nsp-cn-beijing-1a"
echo ""
