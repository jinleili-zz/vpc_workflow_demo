#!/bin/bash

echo "========================================="
echo "NSP Docker 镜像构建"
echo "========================================="

cd "$(dirname "$0")/../.."

echo ""
echo "1. 构建Top NSP VPC镜像..."
docker build -t nsp-top-vpc:latest -f deployments/docker/Dockerfile.top-vpc .

echo ""
echo "2. 构建Top NSP VFW镜像..."
docker build -t nsp-top-vfw:latest -f deployments/docker/Dockerfile.top-vfw .

echo ""
echo "3. 构建AZ NSP VPC镜像..."
docker build -t nsp-az-vpc:latest -f deployments/docker/Dockerfile.az-vpc .

echo ""
echo "4. 构建AZ NSP VFW镜像..."
docker build -t nsp-az-vfw:latest -f deployments/docker/Dockerfile.az-vfw .

echo ""
echo "5. 构建Worker镜像..."
docker build -t nsp-worker:latest -f deployments/docker/Dockerfile.worker .

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
