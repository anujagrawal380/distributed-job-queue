# Distributed Job Queue

A durable, crash-resistant job queue system built in Go with Write-Ahead Log (WAL) for guaranteed persistence, Redis-backed authentication and an easy-to-use Go [SDK](docs/GO-SDK.md).

## Features

- **Durable**: All jobs persisted to disk via Write-Ahead Log
- **Crash Recovery**: Automatically recovers state on restart
- **Go SDK**: Easy-to-use client and worker libraries for Go applications
- **Authentication**: Redis-backed API key system with scope-based permissions ("jobs:submit", "jobs:read")
- **Job Leasing**: Workers lease jobs with configurable timeouts
- **Automatic Retries**: Failed jobs automatically retry until max attempts
- **Dead Letter Queue**: Jobs that exhaust retries move to DEAD state

## Quick Start

### Prerequisites

- Docker & Docker Compose (recommended)
- OR Go 1.23+ (for local development)

### Running with Docker

```bash
# Build and start the server
docker-compose up --build

# In another terminal, test it
curl http://localhost:8080/health
```

### Running Locally

```bash
# Install dependencies
go mod download

# Run the server
go run cmd/server/main.go

# Or build first
go build -o queue-server cmd/server/main.go
./queue-server
```

## Authentication

All API endpoints (except `/health`) require authentication via API keys.

### Key Types

- **Client Keys**: Can submit jobs and read job status
- **Worker Keys**: Can lease and acknowledge jobs
- **Admin Keys**: Full access to all operations

### Getting Development Keys

On first startup, the server automatically seeds three development API keys:

```bash
# Access Redis CLI
docker exec -it job-queue-redis redis-cli

# List all API keys
SMEMBERS keys:active

# Get key details (replace with actual key from above)
HGETALL api_key:client_<key_value>
HGETALL api_key:worker_<key_value>
HGETALL api_key:admin_<key_value>
```

Or check server logs on startup for the seeded keys:

```bash
docker-compose logs queue | grep "Created"
```

You'll see output like:
```
Created client key: client_abc123... (owner: dev-client)
Created worker key: worker_xyz789... (owner: dev-worker)
Created admin key: admin_def456... (owner: dev-admin)
```

## API Usage

All requests require an `Authorization` header with a Bearer token:

```bash
Authorization: Bearer <api_key>
```

### 1. Submit a Job

```bash
# Get client key from logs or Redis (see Authentication section)
CLIENT_KEY="client_abc123..."

curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $CLIENT_KEY" \
  -d '{"payload":"process this data","max_retries":3}'

# Response:
# {"job_id":"550e8400-e29b-41d4-a716-446655440000"}
```

### 2. Lease a Job (Worker)

```bash
# Get worker key from logs or Redis
WORKER_KEY="worker_xyz789..."

curl -X POST http://localhost:8080/jobs/lease \
  -H "Authorization: Bearer $WORKER_KEY"

# Response:
# {
#   "job_id": "550e8400-e29b-41d4-a716-446655440000",
#   "payload": "process this data",
#   "lease_until": "2025-12-26T10:30:00Z",
#   "attempt": 1
# }
```

### 3. Acknowledge Completion

```bash
curl -X POST http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000/ack \
  -H "Authorization: Bearer $WORKER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"result":"processed successfully"}'

# Or with an error
curl -X POST http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000/ack \
  -H "Authorization: Bearer $WORKER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"result_error":"processing failed"}'  

# Response: 200 OK
```

### 4. Check Job Status

```bash
# Any authenticated key with jobs:read scope can check status
curl http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000 \
  -H "Authorization: Bearer $CLIENT_KEY"

# Response:
# {
#   "job_id": "550e8400-e29b-41d4-a716-446655440000",
#   "state": "ACKED",
#   "attempts": 1,
#   "max_retries": 3,
#   "created_at": "2025-12-26T10:00:00Z",
#   "result": "processed successfully"
# }
```

---

## Key Management API

### Create New API Key (Admin Only)

```bash
# Get admin key from logs
ADMIN_KEY="admin_abc123..."

curl -X POST http://localhost:8080/admin/keys \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Production Worker",
    "owner_id": "prod-team",
    "type": "worker"
  }'

# Response:
# {
#   "key": "worker_xyz...",
#   "name": "Production Worker",
#   "owner_id": "prod-team",
#   "type": "worker",
#   "scopes": ["jobs:lease", "jobs:ack", "jobs:read", "keys:read"],
#   "created_at": "2025-12-30T10:00:00Z"
# }
```

### List Your API Keys

```bash
curl http://localhost:8080/keys \
  -H "Authorization: Bearer $CLIENT_KEY"

# Response: Array of keys owned by your owner_id
```

### List All API Keys (Admin Only)

```bash
curl http://localhost:8080/admin/keys \
  -H "Authorization: Bearer $ADMIN_KEY"

# Response: Array of all keys in the system
```

### Revoke an API Key

```bash
# Admins can revoke any key, users can revoke their own keys
curl -X DELETE http://localhost:8080/keys/client_abc123... \
  -H "Authorization: Bearer $ADMIN_KEY"

# Response:
# {
#   "message": "Key revoked successfully",
#   "key": "client_abc123...",
#   "revoked_at": "2025-12-30T10:05:00Z"
# }
```

---

## Job States

```
READY → RUNNING → ACKED ✓
          ↓
        RETRY → DEAD ✗
```

- **READY**: Job waiting to be leased
- **RUNNING**: Job leased by a worker
- **ACKED**: Job completed successfully
- **RETRY**: Job lease expired, will retry
- **DEAD**: Job exhausted all retries

---

## Go SDK

Programmatic access for submitting and processing jobs.

### Installation

```bash
go get github.com/anujagrawal380/distributed-job-queue/pkg/sdk
```

### Quick Example

**Client** (submit jobs):
```go
import "github.com/anujagrawal380/distributed-job-queue/pkg/sdk"

client := sdk.NewClient("http://localhost:8080", "your-client-key")
jobID, _ := client.Submit("process-data", 3)
job, _ := client.WaitForCompletion(jobID, 60*time.Second)
```

**Worker** (process jobs):
```go
worker := sdk.NewWorker(
    "http://localhost:8080",
    "your-worker-key",
    func(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
        // Do work
        return sdk.Success("result")
    },
)
worker.Run(context.Background())
```

**[Full SDK Documentation](docs/GO-SDK.md)**  
**[SDK Examples](sdk-examples/)**

---

## Configuration

Environment variables (see [`docker-compose.yml`](docker-compose.yml)):

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP server port |
| `WAL_DIR` | `./data` | Write-Ahead Log directory |
| `LEASE_DURATION` | `30s` | How long workers can hold a job |
| `LEASE_CHECK_INTERVAL` | `5s` | How often to check for expired leases |

## Testing Crash Recovery

```bash
# Start the server
docker-compose up

# Submit some jobs
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $CLIENT_KEY" \
  -d '{"payload":"test","max_retries":3}'

# Simulate crash
docker-compose kill queue

# Restart
docker-compose up

# Jobs are recovered! Check status
curl http://localhost:8080/jobs/<job_id> \
  -H "Authorization: Bearer $CLIENT_KEY"
```

## Running Tests

```bash
# Run all tests
go test -v ./...

# Run with coverage
go test -cover ./...

# Run specific package tests
go test -v ./internal/queue/...

# Run integration tests
go test -v ./test/...
```