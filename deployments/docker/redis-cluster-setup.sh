#!/bin/sh
set -e

echo "Waiting for Redis instances to start..."
sleep 5

echo "Creating Redis Cluster..."
redis-cli --cluster create \
  redis-node-1:6379 \
  redis-node-2:6379 \
  redis-node-3:6379 \
  --cluster-yes

echo "Redis Cluster created successfully!"
redis-cli -c -h redis-node-1 -p 6379 cluster info
redis-cli -c -h redis-node-1 -p 6379 cluster nodes
