#!/usr/bin/env bash
# Finds the current Raft leader by scanning each node's latest log lines.
# Prints "nodeN  http://localhost:808N" or exits 1 if no leader.
set -euo pipefail

for n in 1 2 3; do
  c="jq-node${n}"
  if ! docker ps --format '{{.Names}}' | grep -q "^${c}$"; then
    continue
  fi
  # Most recent state line for this node.
  last=$(docker logs --tail 500 "${c}" 2>&1 \
    | grep -oE 'entering (Leader|Follower|Candidate) state' \
    | tail -1 || true)
  if [[ "${last}" == "entering Leader state" ]]; then
    echo "node${n}  http://localhost:808${n}"
    exit 0
  fi
done

echo "no leader found" >&2
exit 1
