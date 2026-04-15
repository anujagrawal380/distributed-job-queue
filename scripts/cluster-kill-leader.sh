#!/usr/bin/env bash
# Kills the current leader container, then waits for a new leader to be
# elected. Great headline demo: "watch the cluster heal in 2-3 seconds."
set -euo pipefail

cd "$(dirname "$0")/.."

leader_line=$("$(dirname "$0")/cluster-leader.sh")
leader_name=$(echo "$leader_line" | awk '{print $1}')  # e.g. "node2"
container="jq-${leader_name}"

echo "Killing current leader: ${container}"
docker kill "${container}" >/dev/null

echo "Waiting for new leader..."
for i in {1..30}; do
  sleep 1
  if out=$("$(dirname "$0")/cluster-leader.sh" 2>/dev/null); then
    new_name=$(echo "$out" | awk '{print $1}')
    if [[ "${new_name}" != "${leader_name}" ]]; then
      echo "New leader elected: ${out}"
      exit 0
    fi
  fi
done

echo "no new leader after 30s" >&2
exit 1
