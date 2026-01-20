#!/bin/sh
set -e

echo "Waiting for Redis instances to start..."
sleep 5

# Check if cluster is already created
if redis-cli -h redis-node-1 -p 6379 cluster info | grep -q "cluster_state:ok"; then
  echo "Redis Cluster already exists and is healthy!"
  redis-cli -c -h redis-node-1 -p 6379 cluster info
  redis-cli -c -h redis-node-1 -p 6379 cluster nodes
  exit 0
fi

echo "Creating Redis Cluster..."
redis-cli --cluster create \
  redis-node-1:6379 \
  redis-node-2:6380 \
  redis-node-3:6381 \
  --cluster-yes

echo "Redis Cluster created successfully!"
redis-cli -c -h redis-node-1 -p 6379 cluster info
redis-cli -c -h redis-node-1 -p 6379 cluster nodes