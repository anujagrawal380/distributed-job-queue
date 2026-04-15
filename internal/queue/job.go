package queue

import (
	"time"

	"github.com/google/uuid"
)

// JobState represents the current state of a job
type JobState string

const (
	StateReady   JobState = "READY"   // Ready to be leased
	StateRunning JobState = "RUNNING" // Currently leased to a worker
	StateAcked   JobState = "ACKED"   // Successfully completed
	StateRetry   JobState = "RETRY"   // Failed, will retry
	StateDead    JobState = "DEAD"    // Failed permanently
)

// Job represents a job in the queue
type Job struct {
	ID          string    `json:"job_id"`
	Payload     []byte    `json:"payload"`
	State       JobState  `json:"state"`
	MaxRetries  int       `json:"max_retries"`
	Attempts    int       `json:"attempts"`
	Priority    int       `json:"priority"`
	RunAt       time.Time `json:"run_at"`
	LeaseUntil  time.Time `json:"lease_until,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Result      []byte    `json:"result,omitempty"`
	ResultError string    `json:"result_error,omitempty"`
}

// SubmitOptions configures a new job. Zero values mean "use default".
type SubmitOptions struct {
	MaxRetries int
	Priority   int       // higher = leased sooner; default 0
	RunAt      time.Time // job won't lease until this time; zero means now
}

// NewJob creates a new job with the given options.
func NewJob(payload []byte, opts SubmitOptions) *Job {
	now := time.Now()
	runAt := opts.RunAt
	if runAt.IsZero() {
		runAt = now
	}
	return &Job{
		ID:         uuid.New().String(),
		Payload:    payload,
		State:      StateReady,
		MaxRetries: opts.MaxRetries,
		Priority:   opts.Priority,
		RunAt:      runAt,
		Attempts:   0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// CanLease checks if a job's state allows leasing. Call IsRunnable to also
// check that its scheduled run time has arrived.
func (j *Job) CanLease() bool {
	return j.State == StateReady || j.State == StateRetry
}

// IsRunnable reports whether the job is leasable right now.
func (j *Job) IsRunnable(now time.Time) bool {
	return j.CanLease() && !now.Before(j.RunAt)
}

// CanAck checks if a job can be acknowledged
func (j *Job) CanAck() bool {
	return j.State == StateRunning
}

// IsExpired checks if the lease has expired
func (j *Job) IsExpired() bool {
	return j.State == StateRunning && time.Now().After(j.LeaseUntil)
}

// ShouldRetry determines if a failed job should retry or go to DEAD
func (j *Job) ShouldRetry() bool {
	return j.Attempts < j.MaxRetries
}
