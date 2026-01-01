package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client for submitting and checking jobs
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// ClientOption configures the client
type ClientOption func(*Client)

// WithTimeout sets the HTTP client timeout
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = timeout
	}
}

// WithHTTPClient sets a custom HTTP client
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = client
	}
}

// NewClient creates a new job queue client
func NewClient(baseURL, apiKey string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Submit submits a new job to the queue
func (c *Client) Submit(payload string, maxRetries int) (string, error) {
	return c.SubmitWithContext(context.Background(), payload, maxRetries)
}

// SubmitWithContext submits a new job with context
func (c *Client) SubmitWithContext(ctx context.Context, payload string, maxRetries int) (string, error) {
	body := map[string]interface{}{
		"payload":     payload,
		"max_retries": maxRetries,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/jobs", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to submit job: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		JobID string `json:"job_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	return result.JobID, nil
}

// GetStatus gets the status of a job
func (c *Client) GetStatus(jobID string) (*Job, error) {
	return c.GetStatusWithContext(context.Background(), jobID)
}

// GetStatusWithContext gets the status of a job with context
func (c *Client) GetStatusWithContext(ctx context.Context, jobID string) (*Job, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/jobs/"+jobID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get job status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var job Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &job, nil
}

// WaitForCompletion polls until the job is complete or timeout is reached
func (c *Client) WaitForCompletion(jobID string, timeout time.Duration) (*Job, error) {
	return c.WaitForCompletionWithContext(context.Background(), jobID, timeout)
}

// WaitForCompletionWithContext polls until the job is complete with context
func (c *Client) WaitForCompletionWithContext(ctx context.Context, jobID string, timeout time.Duration) (*Job, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting for job completion")
		case <-ticker.C:
			job, err := c.GetStatusWithContext(ctx, jobID)
			if err != nil {
				return nil, err
			}

			if job.IsComplete() {
				return job, nil
			}
		}
	}
}

// SubmitAndWait submits a job and waits for completion
func (c *Client) SubmitAndWait(payload string, maxRetries int, timeout time.Duration) (*Job, error) {
	jobID, err := c.Submit(payload, maxRetries)
	if err != nil {
		return nil, err
	}

	return c.WaitForCompletion(jobID, timeout)
}
