package api

import (
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// JobBackend is the contract the HTTP layer needs from a queue implementation.
// Both the local *queue.Core and the Raft-backed *cluster.Server satisfy it.
type JobBackend interface {
	SubmitWithOptions(payload []byte, opts queue.SubmitOptions) (string, error)
	Lease(leaseDuration time.Duration) (*queue.Job, error)
	Ack(jobID string, result []byte, resultError string) error
	GetJob(jobID string) (*queue.Job, error)
	Stats() queue.Stats
	ListJobs(f queue.ListFilter) []queue.JobSummary
	Subscribe() (<-chan queue.Event, func())
}

// LeaderInfo lets the API layer redirect writes to the current leader.
// Local mode returns IsLeader=true and an empty address.
type LeaderInfo interface {
	IsLeader() bool
	LeaderHTTPAddr() string // empty when no leader is known
}
