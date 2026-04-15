#!/usr/bin/env bash
# Submits N jobs to the cluster's current leader. Usage:
#   scripts/submit-burst.sh <admin-or-client-key> [count] [priority]
set -euo pipefail

KEY="${1:?usage: $0 <api-key> [count=50] [priority=0]}"
COUNT="${2:-50}"
PRIORITY="${3:-0}"

leader=$("$(dirname "$0")/cluster-leader.sh" | awk '{print $2}')
echo "Submitting ${COUNT} jobs (priority=${PRIORITY}) to ${leader}"

for i in $(seq 1 "${COUNT}"); do
  curl -s -X POST "${leader}/jobs" \
    -H "Authorization: Bearer ${KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"payload\":\"job-${i}\",\"max_retries\":3,\"priority\":${PRIORITY}}" \
    -o /dev/null -w "%{http_code} " || true
done
echo
echo "Done. Check stats: curl -s ${leader}/stats -H 'Authorization: Bearer ${KEY}' | jq"
