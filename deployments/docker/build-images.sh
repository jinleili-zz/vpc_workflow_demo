#!/bin/bash

echo "========================================="
echo "NSP Docker 镜像构建"
echo "========================================="

# 进入 nsp 根目录（包含 vpc_workflow_demo 和 nsp_platform）
cd "$(dirname "$0")/../../.."

echo ""
echo "1. 构建Top NSP VPC镜像..."
docker build -t nsp-top-vpc:latest -f vpc_workflow_demo/deployments/docker/Dockerfile.top-vpc .

echo ""
echo "2. 构建Top NSP VFW镜像..."
docker build -t nsp-top-vfw:latest -f vpc_workflow_demo/deployments/docker/Dockerfile.top-vfw .

echo ""
echo "3. 构建AZ NSP VPC镜像..."
docker build -t nsp-az-vpc:latest -f vpc_workflow_demo/deployments/docker/Dockerfile.az-vpc .

echo ""
echo "4. 构建AZ NSP VFW镜像..."
docker build -t nsp-az-vfw:latest -f vpc_workflow_demo/deployments/docker/Dockerfile.az-vfw .

echo ""
echo "5. 构建Worker镜像..."
docker build -t nsp-worker:latest -f vpc_workflow_demo/deployments/docker/Dockerfile.worker .

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
