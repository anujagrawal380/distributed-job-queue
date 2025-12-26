# Distributed Job Queue

A durable, crash-resistant job queue system built in Go with Write-Ahead Log (WAL) for guaranteed persistence.

## Features

- **Durable**: All jobs persisted to disk via Write-Ahead Log
- **Crash Recovery**: Automatically recovers state on restart
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

## API Usage

### 1. Submit a Job

```bash
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"payload":"process this data","max_retries":3}'

# Response:
# {"job_id":"550e8400-e29b-41d4-a716-446655440000"}
```

### 2. Lease a Job (Worker)

```bash
curl -X POST http://localhost:8080/jobs/lease

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
curl -X POST http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000/ack

# Response: 200 OK
```

### 4. Check Job Status

```bash
curl http://localhost:8080/jobs/550e8400-e29b-41d4-a716-446655440000

# Response:
# {
#   "job_id": "550e8400-e29b-41d4-a716-446655440000",
#   "state": "ACKED",
#   "attempts": 1,
#   "max_retries": 3,
#   "created_at": "2025-12-26T10:00:00Z"
# }
```

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
  -d '{"payload":"test","max_retries":3}'

# Simulate crash
docker-compose kill queue

# Restart
docker-compose up

# Jobs are recovered! Check the logs
```

## Running Tests

```bash
# Run all tests
go test -v ./...

# Run with coverage
go test -cover ./...

# Run specific package tests
go test -v ./internal/queue/...
```