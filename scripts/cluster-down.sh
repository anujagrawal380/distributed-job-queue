#!/usr/bin/env bash
# Tears the cluster down and wipes persistent volumes so the next run
# bootstraps fresh.
set -euo pipefail

cd "$(dirname "$0")/.."
docker compose -f docker-compose.cluster.yml down -v
