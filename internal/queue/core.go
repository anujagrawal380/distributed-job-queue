package queue

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/wal"
)

// Stats is a point-in-time snapshot of queue health.
type Stats struct {
	Total         int                `json:"total"`
	ByState       map[JobState]int   `json:"by_state"`
	Runnable      int                `json:"runnable"`       // leasable right now
	Scheduled     int                `json:"scheduled"`      // READY/RETRY with future RunAt
	TotalSubmits  uint64             `json:"total_submits"`  // cumulative
	TotalAcks     uint64             `json:"total_acks"`     // cumulative
	TotalRetries  uint64             `json:"total_retries"`  // cumulative
	TotalDead     uint64             `json:"total_dead"`     // cumulative
	SnapshotAt    time.Time          `json:"snapshot_at"`
}

// ListFilter filters jobs for ListJobs.
type ListFilter struct {
	State  JobState // empty = all
	Limit  int
	Offset int
}

// JobSummary is a lightweight projection of Job for listings.
type JobSummary struct {
	ID         string    `json:"job_id"`
	State      JobState  `json:"state"`
	Priority   int       `json:"priority"`
	Attempts   int       `json:"attempts"`
	MaxRetries int       `json:"max_retries"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	RunAt      time.Time `json:"run_at"`
}

// Core manages the job queue state
type Core struct {
	mu   sync.RWMutex
	jobs map[string]*Job // jobID -> Job
	wal  *wal.WAL
	bus  *EventBus

	// cumulative counters (protected by mu)
	totalSubmits uint64
	totalAcks    uint64
	totalRetries uint64
	totalDead    uint64
}

// Subscribe returns a channel of queue events and an unsubscribe function.
func (c *Core) Subscribe() (<-chan Event, func()) {
	return c.bus.Subscribe()
}

// NewCore creates a new queue core
func NewCore(w *wal.WAL) (*Core, error) {
	core := &Core{
		jobs: make(map[string]*Job),
		wal:  w,
		bus:  NewEventBus(),
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

	// Move RUNNING jobs to RETRY, then back to READY (they died during crash)
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

// Submit creates a new job with default priority (0) and immediate run time.
func (c *Core) Submit(payload []byte, maxRetries int) (string, error) {
	return c.SubmitWithOptions(payload, SubmitOptions{MaxRetries: maxRetries})
}

// SubmitWithOptions creates a new job with full control over priority and scheduling.
func (c *Core) SubmitWithOptions(payload []byte, opts SubmitOptions) (string, error) {
	if len(payload) == 0 {
		return "", fmt.Errorf("payload cannot be empty")
	}
	if opts.MaxRetries < 0 {
		return "", fmt.Errorf("max_retries must be >= 0")
	}
	if len(payload) > 1024*1024 { // 1MB limit
		return "", fmt.Errorf("payload too large (max 1MB)")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	job := NewJob(payload, opts)

	// Write to WAL first (durability)
	metadata := map[string]interface{}{
		"payload":     string(payload),
		"max_retries": opts.MaxRetries,
		"priority":    job.Priority,
		"run_at":      job.RunAt.Unix(),
	}
	entry := wal.NewEntry(job.ID, wal.EventJobCreated, metadata)
	if err := c.wal.Append(entry); err != nil {
		return "", err
	}

	c.jobs[job.ID] = job
	c.totalSubmits++
	c.bus.Publish(Event{Kind: EventSubmitted, JobID: job.ID, State: StateReady})

	return job.ID, nil
}

// Lease assigns a job to a worker
func (c *Core) Lease(leaseDuration time.Duration) (*Job, error) {
	if leaseDuration <= 0 {
		return nil, fmt.Errorf("lease duration must be positive")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	var best *Job
	for _, job := range c.jobs {
		if !job.IsRunnable(now) {
			continue
		}
		if best == nil || betterThan(job, best) {
			best = job
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no jobs available")
	}
	return c.leaseJob(best, leaseDuration)
}

// betterThan picks the job that should be leased first:
// higher priority wins; on ties, earlier RunAt wins; then older CreatedAt.
func betterThan(a, b *Job) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if !a.RunAt.Equal(b.RunAt) {
		return a.RunAt.Before(b.RunAt)
	}
	return a.CreatedAt.Before(b.CreatedAt)
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

	c.bus.Publish(Event{Kind: EventLeased, JobID: job.ID, State: StateRunning})
	return job, nil
}

// Ack marks a job as completed
func (c *Core) Ack(jobID string, result []byte, resultError string) error {
	if jobID == "" {
		return fmt.Errorf("job_id cannot be empty")
	}

	// TODO: Add Environment Configurable Result Size Limit
	const MaxResultSize = 1024 * 1024 // 1MB
	if len(result) > MaxResultSize {
		return fmt.Errorf("result too large (max 1MB)")
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
	job.Result = result
	job.ResultError = resultError
	job.UpdatedAt = time.Now()

	metadata := map[string]interface{}{
		"result":       string(result),
		"result_error": resultError,
	}

	// Write to WAL
	entry := wal.NewEntry(job.ID, wal.EventJobAcked, metadata)
	if err := c.wal.Append(entry); err != nil {
		return err
	}

	c.totalAcks++
	c.bus.Publish(Event{Kind: EventAcked, JobID: job.ID, State: StateAcked})
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
		c.totalRetries++
		// Move back to READY for retry
		job.State = StateReady
		c.bus.Publish(Event{Kind: EventRetry, JobID: job.ID, State: StateReady})
	} else {
		job.State = StateDead
		entry := wal.NewEntry(job.ID, wal.EventJobDead, nil)
		c.wal.Append(entry)
		c.totalDead++
		c.bus.Publish(Event{Kind: EventDead, JobID: job.ID, State: StateDead})
	}
	job.UpdatedAt = time.Now()
}

// Stats returns a snapshot of current queue state.
func (c *Core) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	s := Stats{
		ByState:      make(map[JobState]int),
		TotalSubmits: c.totalSubmits,
		TotalAcks:    c.totalAcks,
		TotalRetries: c.totalRetries,
		TotalDead:    c.totalDead,
		SnapshotAt:   now,
	}
	for _, j := range c.jobs {
		s.Total++
		s.ByState[j.State]++
		if j.CanLease() {
			if now.Before(j.RunAt) {
				s.Scheduled++
			} else {
				s.Runnable++
			}
		}
	}
	return s
}

// ListJobs returns job summaries, newest first, filtered by state if set.
func (c *Core) ListJobs(f ListFilter) []JobSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	matched := make([]*Job, 0, len(c.jobs))
	for _, j := range c.jobs {
		if f.State != "" && j.State != f.State {
			continue
		}
		matched = append(matched, j)
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].UpdatedAt.After(matched[j].UpdatedAt)
	})

	start := f.Offset
	if start > len(matched) {
		start = len(matched)
	}
	end := len(matched)
	if f.Limit > 0 && start+f.Limit < end {
		end = start + f.Limit
	}

	out := make([]JobSummary, 0, end-start)
	for _, j := range matched[start:end] {
		out = append(out, JobSummary{
			ID:         j.ID,
			State:      j.State,
			Priority:   j.Priority,
			Attempts:   j.Attempts,
			MaxRetries: j.MaxRetries,
			CreatedAt:  j.CreatedAt,
			UpdatedAt:  j.UpdatedAt,
			RunAt:      j.RunAt,
		})
	}
	return out
}

// Apply event helpers for WAL replay
func (c *Core) applyJobCreated(entry *wal.Entry) error {
	payloadStr, _ := entry.Metadata["payload"].(string)
	maxRetries, _ := entry.Metadata["max_retries"].(float64)
	priority, _ := entry.Metadata["priority"].(float64)
	createdAt := time.Unix(entry.Timestamp, 0)

	runAt := createdAt
	if ra, ok := entry.Metadata["run_at"].(float64); ok && ra > 0 {
		runAt = time.Unix(int64(ra), 0)
	}

	job := &Job{
		ID:         entry.JobID,
		Payload:    []byte(payloadStr),
		State:      StateReady,
		MaxRetries: int(maxRetries),
		Priority:   int(priority),
		RunAt:      runAt,
		Attempts:   0,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
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

	// Restore result and error if present
	if entry.Metadata != nil {
		if result, ok := entry.Metadata["result"].(string); ok {
			job.Result = []byte(result)
		}
		if resultError, ok := entry.Metadata["result_error"].(string); ok {
			job.ResultError = resultError
		}
	}

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
