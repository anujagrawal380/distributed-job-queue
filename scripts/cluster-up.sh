#!/usr/bin/env bash
# Brings up the 3-node cluster, waits for leader election, and prints the
# dev API keys that were seeded in Redis (pulled from node1 logs).
set -euo pipefail

cd "$(dirname "$0")/.."

docker compose -f docker-compose.cluster.yml up -d --build

echo "Waiting for leader election..."
for i in {1..30}; do
  if docker logs jq-node1 2>&1 | grep -q "entering Leader state\|entering Follower state"; then
    break
  fi
  sleep 1
done

echo
echo "Node ports:  node1=8081  node2=8082  node3=8083"
echo
echo "Dev API keys (from node1 seed):"
docker logs jq-node1 2>&1 | grep -E "Created (admin|client|worker) key:" | sed 's/^/  /'
echo
echo "Current leader:"
"$(dirname "$0")/cluster-leader.sh"
