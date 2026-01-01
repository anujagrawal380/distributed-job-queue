package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// ProcessFunc is the function signature for job processing
// Returns JobResult with custom data or error
type ProcessFunc func(ctx context.Context, job *LeasedJob) *JobResult

// Worker processes jobs from the queue
type Worker struct {
	baseURL       string
	apiKey        string
	httpClient    *http.Client
	handler       ProcessFunc
	pollInterval  time.Duration
	maxConcurrent int
	logger        Logger
}

// Logger interface for worker logging
type Logger interface {
	Printf(format string, v ...interface{})
}

// WorkerOption configures the worker
type WorkerOption func(*Worker)

// WithPollInterval sets how often to poll for new jobs
func WithPollInterval(interval time.Duration) WorkerOption {
	return func(w *Worker) {
		w.pollInterval = interval
	}
}

// WithMaxConcurrent sets the maximum number of concurrent jobs
func WithMaxConcurrent(max int) WorkerOption {
	return func(w *Worker) {
		w.maxConcurrent = max
	}
}

// WithLogger sets a custom logger
func WithLogger(logger Logger) WorkerOption {
	return func(w *Worker) {
		w.logger = logger
	}
}

// WithWorkerTimeout sets the HTTP client timeout
func WithWorkerTimeout(timeout time.Duration) WorkerOption {
	return func(w *Worker) {
		w.httpClient.Timeout = timeout
	}
}

// NewWorker creates a new worker
func NewWorker(baseURL, apiKey string, handler ProcessFunc, opts ...WorkerOption) *Worker {
	w := &Worker{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		handler:       handler,
		pollInterval:  2 * time.Second,
		maxConcurrent: 1,
		logger:        log.Default(),
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Run starts the worker loop (blocks until context is cancelled)
func (w *Worker) Run(ctx context.Context) error {
	if w.handler == nil {
		return fmt.Errorf("no handler registered")
	}

	w.logger.Printf("Worker started, polling every %v", w.pollInterval)

	// Semaphore for concurrency control
	sem := make(chan struct{}, w.maxConcurrent)

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Printf("Worker stopping...")
			return ctx.Err()
		case <-ticker.C:
			// Try to acquire semaphore
			select {
			case sem <- struct{}{}:
				go func() {
					defer func() { <-sem }()
					w.processOne(ctx)
				}()
			default:
				// Max concurrent jobs reached, skip this cycle
			}
		}
	}
}

// processOne leases and processes a single job
func (w *Worker) processOne(ctx context.Context) {
	// Lease a job
	job, err := w.lease(ctx)
	if err != nil {
		w.logger.Printf("Error leasing job: %v", err)
		return
	}

	if job == nil {
		// No jobs available
		return
	}

	w.logger.Printf("Processing job %s (attempt %d)", job.JobID, job.Attempt)

	// Process the job - handler returns result
	result := w.handler(ctx, job)

	// Acknowledge the job with result
	if result.IsSuccess() {
		ackErr := w.ack(ctx, job.JobID, result.Data, "")
		if ackErr != nil {
			w.logger.Printf("Failed to ack job %s: %v", job.JobID, ackErr)
		}
		w.logger.Printf("Job %s completed successfully with result: %s", job.JobID, result.Data)
	} else {
		ackErr := w.ack(ctx, job.JobID, "", result.Error.Error())
		if ackErr != nil {
			w.logger.Printf("Failed to ack job %s: %v", job.JobID, ackErr)
		}
		w.logger.Printf("Job %s failed: %v", job.JobID, result.Error)
	}
}

// lease leases a job from the queue
func (w *Worker) lease(ctx context.Context) (*LeasedJob, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", w.baseURL+"/jobs/lease", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+w.apiKey)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to lease job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No jobs available
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var job LeasedJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &job, nil
}

// ack acknowledges job completion
func (w *Worker) ack(ctx context.Context, jobID string, result string, resultError string) error {
	body := map[string]interface{}{
		"result":       result,
		"result_error": resultError,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", w.baseURL+"/jobs/"+jobID+"/ack", bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to ack job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}
