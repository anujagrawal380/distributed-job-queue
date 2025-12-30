package test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIntegration_SubmitAndGetJob tests basic job submission and retrieval
func TestIntegration_SubmitAndGetJob(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	clientKey := server.ClientKey

	// Submit job
	jobID := server.SubmitJob(clientKey, "test payload", 3)
	assert.NotEmpty(t, jobID)

	// Get job status
	job := server.GetJob(clientKey, jobID)
	assert.Equal(t, jobID, job.JobID)
	assert.Equal(t, "READY", job.State)
	assert.Equal(t, 0, job.Attempts)
	assert.Equal(t, 3, job.MaxRetries)
}

// TestIntegration_WorkerLeaseAndAck tests the full worker flow
func TestIntegration_WorkerLeaseAndAck(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	clientKey := server.ClientKey
	workerKey := server.WorkerKey

	// Client submits job
	jobID := server.SubmitJob(clientKey, "work to do", 3)

	// Worker leases job
	leasedJob := server.LeaseJob(workerKey)
	assert.Equal(t, jobID, leasedJob.JobID)
	assert.Equal(t, "work to do", leasedJob.Payload)
	assert.Equal(t, 1, leasedJob.Attempt)
	assert.False(t, leasedJob.LeaseUntil.IsZero())

	// Verify job is in RUNNING state
	status := server.GetJob(clientKey, jobID)
	assert.Equal(t, "RUNNING", status.State)

	// Worker acks job
	server.AckJob(workerKey, jobID, "completed successfully", "")

	// Verify job is ACKED
	finalStatus := server.GetJob(clientKey, jobID)
	assert.Equal(t, "ACKED", finalStatus.State)
	assert.Equal(t, "completed successfully", finalStatus.Result)
	assert.Empty(t, finalStatus.ResultError)
}

// TestIntegration_WorkerAckWithError tests acknowledging with error
func TestIntegration_WorkerAckWithError(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	clientKey := server.ClientKey
	workerKey := server.WorkerKey

	// Submit and lease job
	jobID := server.SubmitJob(clientKey, "failing job", 3)
	server.LeaseJob(workerKey)

	// Ack with error
	server.AckJob(workerKey, jobID, "", "processing failed")

	// Verify error is stored
	status := server.GetJob(clientKey, jobID)
	assert.Equal(t, "ACKED", status.State)
	assert.Empty(t, status.Result)
	assert.Equal(t, "processing failed", status.ResultError)
}

// TestIntegration_MultipleJobsLeasing tests leasing multiple jobs
func TestIntegration_MultipleJobsLeasing(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	clientKey := server.ClientKey
	workerKey := server.WorkerKey

	// Submit 3 jobs
	job1 := server.SubmitJob(clientKey, "job 1", 3)
	job2 := server.SubmitJob(clientKey, "job 2", 3)
	job3 := server.SubmitJob(clientKey, "job 3", 3)

	// Lease first job
	leased1 := server.LeaseJob(workerKey)
	assert.Contains(t, []string{job1, job2, job3}, leased1.JobID)

	// Lease second job
	leased2 := server.LeaseJob(workerKey)
	assert.Contains(t, []string{job1, job2, job3}, leased2.JobID)
	assert.NotEqual(t, leased1.JobID, leased2.JobID)

	// Lease third job
	leased3 := server.LeaseJob(workerKey)
	assert.Contains(t, []string{job1, job2, job3}, leased3.JobID)
	assert.NotEqual(t, leased1.JobID, leased3.JobID)
	assert.NotEqual(t, leased2.JobID, leased3.JobID)

	// No more jobs available
	resp := server.LeaseJobRaw(workerKey)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestIntegration_PermissionEnforcement tests scope-based permissions
func TestIntegration_PermissionEnforcement(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	clientKey := server.ClientKey
	workerKey := server.WorkerKey

	tests := []struct {
		name           string
		operation      func() *http.Response
		expectedStatus int
		description    string
	}{
		{
			name: "client can submit",
			operation: func() *http.Response {
				return server.SubmitJobRaw(clientKey, "test", 3)
			},
			expectedStatus: http.StatusCreated,
			description:    "clients have jobs:submit scope",
		},
		{
			name: "client cannot lease",
			operation: func() *http.Response {
				return server.LeaseJobRaw(clientKey)
			},
			expectedStatus: http.StatusForbidden,
			description:    "clients don't have jobs:lease scope",
		},
		{
			name: "worker can lease",
			operation: func() *http.Response {
				// Submit a job first
				server.SubmitJob(clientKey, "test", 3)
				return server.LeaseJobRaw(workerKey)
			},
			expectedStatus: http.StatusOK,
			description:    "workers have jobs:lease scope",
		},
		{
			name: "worker cannot submit",
			operation: func() *http.Response {
				return server.SubmitJobRaw(workerKey, "test", 3)
			},
			expectedStatus: http.StatusForbidden,
			description:    "workers don't have jobs:submit scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := tt.operation()
			assert.Equal(t, tt.expectedStatus, resp.StatusCode, tt.description)
		})
	}
}

// / TestIntegration_AuthenticationRequired tests that auth is enforced
func TestIntegration_AuthenticationRequired(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	tests := []struct {
		name     string
		endpoint string
		method   string
		body     string
	}{
		{
			name:     "submit without auth",
			endpoint: "/jobs",
			method:   "POST",
			body:     `{"payload":"test","max_retries":3}`,
		},
		{
			name:     "lease without auth",
			endpoint: "/jobs/lease",
			method:   "POST",
			body:     "",
		},
		{
			name:     "get job without auth",
			endpoint: "/jobs/test-id",
			method:   "GET",
			body:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = bytes.NewBufferString(tt.body)
			}

			req := httptest.NewRequest(tt.method, tt.endpoint, body)
			// No Authorization header
			rr := httptest.NewRecorder()
			server.Handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
			assert.Contains(t, rr.Body.String(), "Missing Authorization header")
		})
	}
}

// TestIntegration_InvalidAPIKey tests rejection of invalid keys
func TestIntegration_InvalidAPIKey(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	invalidKey := "invalid_key_12345"

	tests := []struct {
		name      string
		operation func() *http.Response
	}{
		{
			name: "submit with invalid key",
			operation: func() *http.Response {
				return server.SubmitJobRaw(invalidKey, "test", 3)
			},
		},
		{
			name: "lease with invalid key",
			operation: func() *http.Response {
				return server.LeaseJobRaw(invalidKey)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := tt.operation()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			assert.Contains(t, readBody(resp), "Invalid API key")
		})
	}
}

// TestIntegration_CrashRecovery tests WAL recovery
func TestIntegration_CrashRecovery(t *testing.T) {
	tempDir := t.TempDir()

	// Phase 1: Create jobs
	server1 := setupIntegrationTestWithDir(t, tempDir)

	job1 := server1.SubmitJob(server1.ClientKey, "job 1", 3)
	job2 := server1.SubmitJob(server1.ClientKey, "job 2", 3)

	server1.Teardown() // Simulate crash

	// Phase 2: Restart server with same WAL directory
	server2 := setupIntegrationTestWithDir(t, tempDir)
	defer server2.Teardown()

	// Jobs should still exist (using server2's client key since Redis is new)
	status1 := server2.GetJob(server2.ClientKey, job1)
	status2 := server2.GetJob(server2.ClientKey, job2)

	assert.Equal(t, job1, status1.JobID)
	assert.Equal(t, job2, status2.JobID)
	assert.Equal(t, "READY", status1.State)
	assert.Equal(t, "READY", status2.State)
}

// TestIntegration_HealthCheck tests health endpoint (no auth required)
func TestIntegration_HealthCheck(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	req := httptest.NewRequest("GET", "/health", nil)
	// No auth header
	rr := httptest.NewRecorder()
	server.Handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "OK", rr.Body.String())
}

// TestIntegration_JobNotFound tests getting non-existent job
func TestIntegration_JobNotFound(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	resp := server.GetJobRaw(server.ClientKey, "nonexistent-job-id")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, readBody(resp), "job not found")
}

// TestIntegration_InvalidJobSubmission tests validation
func TestIntegration_InvalidJobSubmission(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	clientKey := server.ClientKey

	tests := []struct {
		name           string
		payload        string
		maxRetries     int
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "empty payload",
			payload:        "",
			maxRetries:     3,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "payload is required",
		},
		{
			name:           "zero retries is valid",
			payload:        "test",
			maxRetries:     0,
			expectedStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := server.SubmitJobRaw(clientKey, tt.payload, tt.maxRetries)
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)
			if tt.expectedError != "" {
				assert.Contains(t, readBody(resp), tt.expectedError)
			}
		})
	}
}

// TestIntegration_AckJobValidation tests ack endpoint validation
func TestIntegration_AckJobValidation(t *testing.T) {
	server := setupIntegrationTest(t)
	defer server.Teardown()

	clientKey := server.ClientKey
	workerKey := server.WorkerKey

	// Submit and lease a job
	jobID := server.SubmitJob(clientKey, "test", 3)
	server.LeaseJob(workerKey)

	tests := []struct {
		name           string
		jobID          string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "ack valid job",
			jobID:          jobID,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "ack non-existent job",
			jobID:          "nonexistent-id",
			expectedStatus: http.StatusBadRequest,
			expectedError:  "failed to ack job",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := server.AckJobRaw(workerKey, tt.jobID, "result", "")
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)
			if tt.expectedError != "" {
				assert.Contains(t, readBody(resp), tt.expectedError)
			}
		})
	}
}
