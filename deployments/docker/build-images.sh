#!/bin/bash

echo "========================================="
echo "NSP Docker 镜像构建"
echo "========================================="

cd "$(dirname "$0")/../.."

echo ""
echo "1. 构建Top NSP镜像..."
docker build -t nsp-top:latest -f deployments/docker/Dockerfile.top .

echo ""
echo "2. 构建AZ NSP镜像..."
docker build -t nsp-az:latest -f deployments/docker/Dockerfile.az .

echo ""
echo "3. 构建Switch Worker镜像..."
docker build -t nsp-switch-worker:latest -f deployments/docker/Dockerfile.switch .

echo ""
echo "4. 构建Firewall Worker镜像..."
docker build -t nsp-firewall-worker:latest -f deployments/docker/Dockerfile.firewall .

echo ""
echo "========================================="
echo "镜像构建完成"
echo ""
echo "查看镜像:"
echo "  docker images | grep nsp"
echo ""
echo "启动服务:"
echo "  cd deployments/docker && docker-compose up -d"
echo "========================================="
