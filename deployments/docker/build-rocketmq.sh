#!/bin/bash

# 构建RocketMQ版本的NSP镜像

set -e

echo "=========================================="
echo "开始构建NSP镜像（RocketMQ版本）"
echo "=========================================="

# 切换到项目根目录
cd "$(dirname "$0")/../.."

# 1. 构建AZ NSP镜像
echo "1. 构建AZ NSP镜像..."
docker build -t nsp-az:latest -f deployments/docker/Dockerfile.az .

# 2. 构建Switch Worker镜像
echo "2. 构建Switch Worker镜像..."
docker build -t nsp-switch-worker:latest -f deployments/docker/Dockerfile.switch .

# 3. 构建Firewall Worker镜像
echo "3. 构建Firewall Worker镜像..."
docker build -t nsp-firewall-worker:latest -f deployments/docker/Dockerfile.firewall .

echo "=========================================="
echo "镜像构建完成！"
echo "=========================================="
docker images | grep nsp
