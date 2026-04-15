#!/usr/bin/env bash
# Runs the chaos harness against the cluster. Submits a load, spins workers,
# periodically kills the current leader, and reports zero-loss + latency.
#
# Usage:
#   scripts/run-chaos.sh <admin-key> [jobs=2000] [workers=16] [kills=5]
set -euo pipefail

KEY="${1:?usage: $0 <admin-key> [jobs] [workers] [kills]}"
JOBS="${2:-2000}"
WORKERS="${3:-16}"
KILLS="${4:-5}"

cd "$(dirname "$0")/.."

leader=$("$(dirname "$0")/cluster-leader.sh" | awk '{print $2}')

go run ./cmd/chaos \
  -target "${leader}" \
  -key "${KEY}" \
  -jobs "${JOBS}" -workers "${WORKERS}" \
  -kills "${KILLS}" -kill-interval 3s \
  -kill-cmd    "$(dirname "$0")/cluster-kill-leader.sh" \
  -restart-cmd "docker compose -f docker-compose.cluster.yml up -d"
