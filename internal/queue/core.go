package queue

import (
	"fmt"
	"sync"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/wal"
)

// Core manages the job queue state
type Core struct {
	mu   sync.RWMutex
	jobs map[string]*Job // jobID -> Job
	wal  *wal.WAL
}

// NewCore creates a new queue core
func NewCore(w *wal.WAL) (*Core, error) {
	core := &Core{
		jobs: make(map[string]*Job),
		wal:  w,
	}

	// Replay WAL to rebuild state
	if err := core.recoverFromWAL(); err != nil {
		return nil, fmt.Errorf("failed to recover from WAL: %w", err)
	}

	return core, nil
}

// recoverFromWAL rebuilds in-memory state from WAL
func (c *Core) recoverFromWAL() error {
	entries, err := c.wal.Replay()
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := c.applyEvent(entry); err != nil {
			return fmt.Errorf("failed to apply event %s: %w", entry.Event, err)
		}
	}

	// Move RUNNING jobs to RETRY (they died during crash)
	for _, job := range c.jobs {
		if job.State == StateRunning {
			job.State = StateRetry
			job.UpdatedAt = time.Now()
		}
	}

	return nil
}

// applyEvent applies a WAL entry to update job state
func (c *Core) applyEvent(entry *wal.Entry) error {
	switch entry.Event {
	case wal.EventJobCreated:
		return c.applyJobCreated(entry)
	case wal.EventJobLeased:
		return c.applyJobLeased(entry)
	case wal.EventJobAcked:
		return c.applyJobAcked(entry)
	case wal.EventJobRetry:
		return c.applyJobRetry(entry)
	case wal.EventJobDead:
		return c.applyJobDead(entry)
	default:
		return fmt.Errorf("unknown event type: %s", entry.Event)
	}
}

// Submit creates a new job
func (c *Core) Submit(payload []byte, maxRetries int) (string, error) {
	if len(payload) == 0 {
		return "", fmt.Errorf("payload cannot be empty")
	}
	if maxRetries < 0 {
		return "", fmt.Errorf("max_retries must be >= 0")
	}
	if len(payload) > 1024*1024 { // 1MB limit
		return "", fmt.Errorf("payload too large (max 1MB)")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	job := NewJob(payload, maxRetries)

	// Write to WAL first (durability)
	metadata := map[string]interface{}{
		"payload":     string(payload),
		"max_retries": maxRetries,
	}
	entry := wal.NewEntry(job.ID, wal.EventJobCreated, metadata)
	if err := c.wal.Append(entry); err != nil {
		return "", err
	}

	// Update in-memory state
	c.jobs[job.ID] = job

	return job.ID, nil
}

// Lease assigns a job to a worker
func (c *Core) Lease(leaseDuration time.Duration) (*Job, error) {
	if leaseDuration <= 0 {
		return nil, fmt.Errorf("lease duration must be positive")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Find first READY job
	for _, job := range c.jobs {
		if job.CanLease() {
			return c.leaseJob(job, leaseDuration)
		}
	}

	return nil, fmt.Errorf("no jobs available")
}

func (c *Core) leaseJob(job *Job, duration time.Duration) (*Job, error) {
	job.State = StateRunning
	job.Attempts++
	job.LeaseUntil = time.Now().Add(duration)
	job.UpdatedAt = time.Now()

	// Write to WAL
	metadata := map[string]interface{}{
		"lease_until": job.LeaseUntil.Unix(),
		"attempts":    job.Attempts,
	}
	entry := wal.NewEntry(job.ID, wal.EventJobLeased, metadata)
	if err := c.wal.Append(entry); err != nil {
		return nil, err
	}

	return job, nil
}

// Ack marks a job as completed
func (c *Core) Ack(jobID string) error {
	if jobID == "" {
		return fmt.Errorf("job_id cannot be empty")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	job, exists := c.jobs[jobID]
	if !exists {
		return fmt.Errorf("job not found: %s", jobID)
	}

	if !job.CanAck() {
		return fmt.Errorf("job cannot be acked in state: %s", job.State)
	}

	job.State = StateAcked
	job.UpdatedAt = time.Now()

	// Write to WAL
	entry := wal.NewEntry(job.ID, wal.EventJobAcked, nil)
	if err := c.wal.Append(entry); err != nil {
		return err
	}

	return nil
}

// GetJob retrieves a job by ID
func (c *Core) GetJob(jobID string) (*Job, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id cannot be empty")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	job, exists := c.jobs[jobID]
	if !exists {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	return job, nil
}

// CheckExpiredLeases moves expired RUNNING jobs to RETRY or DEAD
func (c *Core) CheckExpiredLeases() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, job := range c.jobs {
		if job.IsExpired() {
			c.handleExpiredJob(job)
		}
	}
}

func (c *Core) handleExpiredJob(job *Job) {
	if job.ShouldRetry() {
		job.State = StateRetry
		entry := wal.NewEntry(job.ID, wal.EventJobRetry, nil)
		c.wal.Append(entry)

		// Move back to READY for retry
		job.State = StateReady
	} else {
		job.State = StateDead
		entry := wal.NewEntry(job.ID, wal.EventJobDead, nil)
		c.wal.Append(entry)
	}
	job.UpdatedAt = time.Now()
}

// Apply event helpers for WAL replay
func (c *Core) applyJobCreated(entry *wal.Entry) error {
	payloadStr, _ := entry.Metadata["payload"].(string)
	maxRetries, _ := entry.Metadata["max_retries"].(float64)

	job := &Job{
		ID:         entry.JobID,
		Payload:    []byte(payloadStr),
		State:      StateReady,
		MaxRetries: int(maxRetries),
		Attempts:   0,
		CreatedAt:  time.Unix(entry.Timestamp, 0),
		UpdatedAt:  time.Unix(entry.Timestamp, 0),
	}
	c.jobs[entry.JobID] = job
	return nil
}

func (c *Core) applyJobLeased(entry *wal.Entry) error {
	job, exists := c.jobs[entry.JobID]
	if !exists {
		return fmt.Errorf("job not found: %s", entry.JobID)
	}

	leaseUntil, _ := entry.Metadata["lease_until"].(float64)
	attempts, _ := entry.Metadata["attempts"].(float64)

	job.State = StateRunning
	job.Attempts = int(attempts)
	job.LeaseUntil = time.Unix(int64(leaseUntil), 0)
	job.UpdatedAt = time.Unix(entry.Timestamp, 0)
	return nil
}

func (c *Core) applyJobAcked(entry *wal.Entry) error {
	job, exists := c.jobs[entry.JobID]
	if !exists {
		return fmt.Errorf("job not found: %s", entry.JobID)
	}

	job.State = StateAcked
	job.UpdatedAt = time.Unix(entry.Timestamp, 0)
	return nil
}

func (c *Core) applyJobRetry(entry *wal.Entry) error {
	job, exists := c.jobs[entry.JobID]
	if !exists {
		return fmt.Errorf("job not found: %s", entry.JobID)
	}

	job.State = StateReady // Retry means back to ready
	job.UpdatedAt = time.Unix(entry.Timestamp, 0)
	return nil
}

func (c *Core) applyJobDead(entry *wal.Entry) error {
	job, exists := c.jobs[entry.JobID]
	if !exists {
		return fmt.Errorf("job not found: %s", entry.JobID)
	}

	job.State = StateDead
	job.UpdatedAt = time.Unix(entry.Timestamp, 0)
	return nil
}
