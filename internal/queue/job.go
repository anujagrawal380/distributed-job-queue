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
	ID         string    `json:"job_id"`
	Payload    []byte    `json:"payload"`
	State      JobState  `json:"state"`
	MaxRetries int       `json:"max_retries"`
	Attempts   int       `json:"attempts"`
	LeaseUntil time.Time `json:"lease_until,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// NewJob creates a new job with default values
func NewJob(payload []byte, maxRetries int) *Job {
	now := time.Now()
	return &Job{
		ID:         uuid.New().String(),
		Payload:    payload,
		State:      StateReady,
		MaxRetries: maxRetries,
		Attempts:   0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// CanLease checks if a job can be leased
func (j *Job) CanLease() bool {
	return j.State == StateReady
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
