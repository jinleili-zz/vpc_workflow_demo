#!/bin/sh

echo "Waiting for Redis instances to start..."
sleep 5

echo "Checking if Redis Cluster already exists..."
CLUSTER_INFO=$(redis-cli -h redis-node-1 -p 6379 cluster info 2>/dev/null || echo "")

if echo "$CLUSTER_INFO" | grep -q "cluster_state:ok"; then
  echo "Redis Cluster already exists and is healthy!"
  redis-cli -c -h redis-node-1 -p 6379 cluster nodes
  exit 0
fi

echo "Creating Redis Cluster..."
redis-cli --cluster create \
  redis-node-1:6379 \
  redis-node-2:6380 \
  redis-node-3:6381 \
  --cluster-yes || {
    echo "Cluster creation failed, checking if already exists..."
    redis-cli -c -h redis-node-1 -p 6379 cluster info
    exit 0
  }

echo "Redis Cluster created successfully!"
redis-cli -c -h redis-node-1 -p 6379 cluster info
redis-cli -c -h redis-node-1 -p 6379 cluster nodes
