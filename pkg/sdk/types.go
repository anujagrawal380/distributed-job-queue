package sdk

import "time"

// JobState represents the current state of a job
type JobState string

const (
	StateReady   JobState = "READY"
	StateRunning JobState = "RUNNING"
	StateAcked   JobState = "ACKED"
	StateRetry   JobState = "RETRY"
	StateDead    JobState = "DEAD"
)

// Job represents a job in the queue
type Job struct {
	JobID       string     `json:"job_id"`
	Payload     string     `json:"payload,omitempty"`
	State       JobState   `json:"state"`
	Attempts    int        `json:"attempts"`
	MaxRetries  int        `json:"max_retries"`
	CreatedAt   time.Time  `json:"created_at"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	Result      string     `json:"result,omitempty"`
	ResultError string     `json:"result_error,omitempty"`
}

// LeasedJob represents a job that has been leased by a worker
type LeasedJob struct {
	JobID      string    `json:"job_id"`
	Payload    string    `json:"payload"`
	LeaseUntil time.Time `json:"lease_until"`
	Attempt    int       `json:"attempt"`
}

// JobResult represents the result of job processing
type JobResult struct {
	// Data contains the result data (e.g., output URL, processed data, etc.)
	Data  string
	Error error
}

// Success creates a successful job result
func Success(data string) *JobResult {
	return &JobResult{
		Data:  data,
		Error: nil,
	}
}

// Failure creates a failed job result
func Failure(err error) *JobResult {
	return &JobResult{
		Data:  "",
		Error: err,
	}
}

// IsSuccess returns true if the result is successful
func (r *JobResult) IsSuccess() bool {
	return r.Error == nil
}

// IsComplete returns true if the job is in a terminal state
func (j *Job) IsComplete() bool {
	return j.State == StateAcked || j.State == StateDead
}

// IsSuccessful returns true if the job completed successfully
func (j *Job) IsSuccessful() bool {
	return j.State == StateAcked && j.ResultError == ""
}

// IsFailed returns true if the job failed permanently
func (j *Job) IsFailed() bool {
	return j.State == StateDead || (j.State == StateAcked && j.ResultError != "")
}
