# Demo scripts

Scripts for driving the 10-minute demo. All assume you're running Docker
and are at the repo root (or let the scripts `cd` there — they all do).

## Typical 10-min demo flow

```bash
# 1. Bring up the cluster (prints seeded dev keys + current leader).
./scripts/cluster-up.sh

# Export the admin key printed above:
export ADMIN_KEY=admin_...

# 2. Open the dashboard (any node's UI works — they all read local FSM).
open http://localhost:8081/ui/index.html

# 3. Submit a burst of jobs.
./scripts/submit-burst.sh "$ADMIN_KEY" 200 5

# 4. Kill the leader; new one elects in ~2s, jobs still visible.
./scripts/cluster-kill-leader.sh

# 5. Run chaos: 2000 jobs, 16 workers, 5 leader kills, prove zero loss.
./scripts/run-chaos.sh "$ADMIN_KEY"

# 6. Tear down.
./scripts/cluster-down.sh
```

## Scripts

| Script | What it does |
|---|---|
| `cluster-up.sh` | Builds and starts 3-node cluster; waits for leader; prints dev keys. |
| `cluster-leader.sh` | Prints the current leader's nodeN + HTTP URL (scans logs). |
| `cluster-kill-leader.sh` | Kills the leader container and waits for a new one. |
| `submit-burst.sh` | Submits N jobs (any key/priority) to the current leader. |
| `run-chaos.sh` | Full chaos demo: load + workers + periodic leader kills. |
| `cluster-down.sh` | `docker compose down -v` (wipes volumes for a fresh next run). |
