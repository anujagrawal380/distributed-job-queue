package queue

import (
	"testing"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/wal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a test Core with temporary WAL
func setupTestCore(t *testing.T) (*Core, string) {
	t.Helper()

	// Create temporary directory for WAL
	tmpDir := t.TempDir()

	// Open WAL
	w, err := wal.Open(tmpDir)
	require.NoError(t, err, "failed to open WAL")

	// Create Core
	core, err := NewCore(w)
	require.NoError(t, err, "failed to create core")

	return core, tmpDir
}

// Test: Submit job with various inputs
func TestSubmit(t *testing.T) {
	tests := []struct {
		name        string
		payload     []byte
		maxRetries  int
		wantErr     bool
		errContains string
	}{
		{
			name:       "valid job",
			payload:    []byte("test payload"),
			maxRetries: 3,
			wantErr:    false,
		},
		{
			name:       "zero retries",
			payload:    []byte("test"),
			maxRetries: 0,
			wantErr:    false,
		},
		{
			name:        "empty payload",
			payload:     []byte(""),
			maxRetries:  3,
			wantErr:     true,
			errContains: "payload cannot be empty",
		},
		{
			name:        "nil payload",
			payload:     nil,
			maxRetries:  3,
			wantErr:     true,
			errContains: "payload cannot be empty",
		},
		{
			name:        "negative retries",
			payload:     []byte("test"),
			maxRetries:  -1,
			wantErr:     true,
			errContains: "max_retries must be >= 0",
		},
		{
			name:        "payload too large",
			payload:     make([]byte, 2*1024*1024), // 2MB
			maxRetries:  3,
			wantErr:     true,
			errContains: "payload too large",
		},
		{
			name:       "max size payload",
			payload:    make([]byte, 1024*1024), // Exactly 1MB
			maxRetries: 3,
			wantErr:    false,
		},
		{
			name:       "large retries",
			payload:    []byte("test"),
			maxRetries: 1000,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, _ := setupTestCore(t)

			jobID, err := core.Submit(tt.payload, tt.maxRetries)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, jobID)

			// Verify job exists and has correct properties
			job, err := core.GetJob(jobID)
			require.NoError(t, err)

			assert.Equal(t, StateReady, job.State)
			assert.Equal(t, tt.maxRetries, job.MaxRetries)
			assert.Equal(t, 0, job.Attempts)
		})
	}
}

// Test: Lease job with various scenarios
func TestLease(t *testing.T) {
	tests := []struct {
		name          string
		setupJobs     int // Number of jobs to create
		leaseDuration time.Duration
		wantErr       bool
		errContains   string
		checkState    bool
		expectedState JobState
	}{
		{
			name:          "lease single job",
			setupJobs:     1,
			leaseDuration: 30 * time.Second,
			wantErr:       false,
			checkState:    true,
			expectedState: StateRunning,
		},
		{
			name:          "no jobs available",
			setupJobs:     0,
			leaseDuration: 30 * time.Second,
			wantErr:       true,
			errContains:   "no jobs available",
		},
		{
			name:          "zero duration",
			setupJobs:     1,
			leaseDuration: 0,
			wantErr:       true,
			errContains:   "lease duration must be positive",
		},
		{
			name:          "negative duration",
			setupJobs:     1,
			leaseDuration: -5 * time.Second,
			wantErr:       true,
			errContains:   "lease duration must be positive",
		},
		{
			name:          "very short duration",
			setupJobs:     1,
			leaseDuration: 1 * time.Millisecond,
			wantErr:       false,
			checkState:    true,
			expectedState: StateRunning,
		},
		{
			name:          "very long duration",
			setupJobs:     1,
			leaseDuration: 24 * time.Hour,
			wantErr:       false,
			checkState:    true,
			expectedState: StateRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, _ := setupTestCore(t)

			// Setup: create jobs
			for i := 0; i < tt.setupJobs; i++ {
				_, _ = core.Submit([]byte("test"), 3)
			}

			// Execute lease
			job, err := core.Lease(tt.leaseDuration)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, job)

			if tt.checkState {
				assert.Equal(t, tt.expectedState, job.State)
			}

			assert.Equal(t, 1, job.Attempts)
			assert.False(t, job.LeaseUntil.IsZero())

			expectedLeaseTime := time.Now().Add(tt.leaseDuration)
			assert.WithinDuration(t, expectedLeaseTime, job.LeaseUntil, 1*time.Second)
		})
	}
}

// Test: Ack job with various scenarios
func TestAck(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*Core) string // Returns job ID to ack
		jobID         string             // Used if setup is nil
		result        []byte
		resultError   string
		wantErr       bool
		errContains   string
		expectedState JobState
	}{
		{
			name: "ack running job with result",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				c.Lease(30 * time.Second)
				return id
			},
			result:        []byte("processed successfully"),
			wantErr:       false,
			expectedState: StateAcked,
		},
		{
			name: "ack running job with error",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				c.Lease(30 * time.Second)
				return id
			},
			resultError:   "processing failed",
			wantErr:       false,
			expectedState: StateAcked,
		},
		{
			name: "ack with both result and error",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				c.Lease(30 * time.Second)
				return id
			},
			result:        []byte("partial output"),
			resultError:   "completed with warnings",
			wantErr:       false,
			expectedState: StateAcked,
		},
		{
			name: "ack with empty result (valid)",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				c.Lease(30 * time.Second)
				return id
			},
			result:        []byte(""),
			wantErr:       false,
			expectedState: StateAcked,
		},
		{
			name: "ack with nil result (valid)",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				c.Lease(30 * time.Second)
				return id
			},
			result:        nil,
			wantErr:       false,
			expectedState: StateAcked,
		},
		{
			name: "ack ready job (wrong state)",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				return id
			},
			result:      []byte("result"),
			wantErr:     true,
			errContains: "job cannot be acked",
		},
		{
			name: "ack already acked job",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				c.Lease(30 * time.Second)
				c.Ack(id, []byte("first result"), "")
				return id
			},
			result:      []byte("second result"),
			wantErr:     true,
			errContains: "job cannot be acked",
		},
		{
			name:        "ack non-existent job",
			jobID:       "non-existent-id",
			result:      []byte("result"),
			wantErr:     true,
			errContains: "job not found",
		},
		{
			name:        "ack empty job ID",
			jobID:       "",
			result:      []byte("result"),
			wantErr:     true,
			errContains: "job_id cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, _ := setupTestCore(t)

			var jobID string
			if tt.setup != nil {
				jobID = tt.setup(core)
			} else {
				jobID = tt.jobID
			}

			err := core.Ack(jobID, tt.result, tt.resultError)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)

			// Verify state and result
			job, _ := core.GetJob(jobID)
			assert.Equal(t, tt.expectedState, job.State)
			assert.Equal(t, tt.result, job.Result)
			assert.Equal(t, tt.resultError, job.ResultError)
		})
	}
}

// Test: GetJob with various scenarios
func TestGetJob(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*Core) string // Returns job ID to get
		jobID       string             // Used if setup is nil
		wantErr     bool
		errContains string
	}{
		{
			name: "get existing job",
			setup: func(c *Core) string {
				id, _ := c.Submit([]byte("test"), 3)
				return id
			},
			wantErr: false,
		},
		{
			name:        "get non-existent job",
			jobID:       "non-existent",
			wantErr:     true,
			errContains: "job not found",
		},
		{
			name:        "get with empty ID",
			jobID:       "",
			wantErr:     true,
			errContains: "job_id cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, _ := setupTestCore(t)

			var jobID string
			if tt.setup != nil {
				jobID = tt.setup(core)
			} else {
				jobID = tt.jobID
			}

			job, err := core.GetJob(jobID)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, jobID, job.ID)
		})
	}
}

// Test: Expired lease handling
func TestCheckExpiredLeases(t *testing.T) {
	tests := []struct {
		name          string
		maxRetries    int
		attempts      int // Simulate this many failed attempts
		leaseDuration time.Duration
		expectedState JobState
	}{
		{
			name:          "first timeout - should retry",
			maxRetries:    3,
			attempts:      1,
			leaseDuration: 1 * time.Millisecond,
			expectedState: StateReady,
		},
		{
			name:          "retries exhausted - should be dead",
			maxRetries:    0,
			attempts:      1,
			leaseDuration: 1 * time.Millisecond,
			expectedState: StateDead,
		},
		{
			name:          "multiple retries remaining",
			maxRetries:    5,
			attempts:      2,
			leaseDuration: 1 * time.Millisecond,
			expectedState: StateReady,
		},
		{
			name:          "last retry exhausted",
			maxRetries:    3,
			attempts:      3,
			leaseDuration: 1 * time.Millisecond,
			expectedState: StateDead,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, _ := setupTestCore(t)

			// Submit job
			jobID, _ := core.Submit([]byte("test"), tt.maxRetries)

			// Simulate multiple attempts
			for i := 0; i < tt.attempts; i++ {
				core.Lease(tt.leaseDuration)
				time.Sleep(10 * time.Millisecond)
				core.CheckExpiredLeases()
			}

			// Verify final state
			job, _ := core.GetJob(jobID)
			assert.Equal(t, tt.expectedState, job.State)
			assert.Equal(t, tt.attempts, job.Attempts)
		})
	}
}

// Test: WAL recovery scenarios
func TestWALRecovery(t *testing.T) {
	t.Run("recover ready jobs", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - create ready jobs
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)
		id1, _ := core1.Submit([]byte("job1"), 3)
		id2, _ := core1.Submit([]byte("job2"), 3)
		w1.Close()

		// Second core - recover
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		job1, err := core2.GetJob(id1)
		require.NoError(t, err)
		assert.Equal(t, StateReady, job1.State)

		job2, err := core2.GetJob(id2)
		require.NoError(t, err)
		assert.Equal(t, StateReady, job2.State)

		w2.Close()
	})

	t.Run("recover running jobs as retry", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - create and lease job
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)
		id, _ := core1.Submit([]byte("job"), 3)
		core1.Lease(30 * time.Second)
		w1.Close()

		// Second core - recover (RUNNING should become RETRY)
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		job, err := core2.GetJob(id)
		require.NoError(t, err)
		assert.Equal(t, StateRetry, job.State, "running job should become RETRY after crash")

		// Verify it can still be leased from RETRY state
		leasedJob, err := core2.Lease(30 * time.Second)
		require.NoError(t, err)
		assert.Equal(t, id, leasedJob.ID, "should be able to lease job in RETRY state")
		assert.Equal(t, StateRunning, leasedJob.State)
		assert.Equal(t, 2, leasedJob.Attempts, "should have 2 attempts (1 before crash + 1 after)") // Changed from 1 to 2

		w2.Close()
	})

	t.Run("recover acked jobs", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - create, lease, and ack job
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)
		id, _ := core1.Submit([]byte("job"), 3)
		core1.Lease(30 * time.Second)
		core1.Ack(id, nil, "")
		w1.Close()

		// Second core - recover
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		job, err := core2.GetJob(id)
		require.NoError(t, err)
		assert.Equal(t, StateAcked, job.State)

		w2.Close()
	})

	t.Run("recover mixed states", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - create jobs in different states
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)

		// Create 3 jobs (all start as READY)
		core1.Submit([]byte("job1"), 3)
		core1.Submit([]byte("job2"), 3)
		core1.Submit([]byte("job3"), 3)

		// Lease one job (becomes RUNNING)
		core1.Lease(30 * time.Second)

		// Lease another job and ack it (becomes ACKED)
		leased, _ := core1.Lease(30 * time.Second)
		core1.Ack(leased.ID, nil, "")

		// Now we have: 1 READY, 1 RUNNING, 1 ACKED
		w1.Close()

		// Second core - recover from WAL
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		// Count jobs by state after recovery
		stateCount := make(map[JobState]int)
		core2.mu.RLock()
		for _, job := range core2.jobs {
			stateCount[job.State]++
		}
		core2.mu.RUnlock()

		// Verify state counts after recovery
		assert.Equal(t, 1, stateCount[StateReady], "should have 1 READY job")
		assert.Equal(t, 1, stateCount[StateRetry], "should have 1 RETRY job (was RUNNING before crash)")
		assert.Equal(t, 1, stateCount[StateAcked], "should have 1 ACKED job")
		assert.Equal(t, 0, stateCount[StateRunning], "should have 0 RUNNING jobs after recovery")
		assert.Equal(t, 3, len(core2.jobs), "should have 3 total jobs")

		w2.Close()
	})

	t.Run("recover job with max retries exhausted", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - create job with 0 retries and lease it
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)
		id, _ := core1.Submit([]byte("job"), 0) // No retries allowed
		core1.Lease(30 * time.Second)
		w1.Close()

		// Second core - recover (should become RETRY but not leasable since attempts >= maxRetries)
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		job, err := core2.GetJob(id)
		require.NoError(t, err)
		assert.Equal(t, StateRetry, job.State)
		assert.Equal(t, 1, job.Attempts)
		assert.Equal(t, 0, job.MaxRetries)

		w2.Close()
	})

	t.Run("recover job with result", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - create, lease, and ack job with result
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)
		id, _ := core1.Submit([]byte("job"), 3)
		core1.Lease(30 * time.Second)
		core1.Ack(id, []byte("success output"), "")
		w1.Close()

		// Second core - recover
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		job, err := core2.GetJob(id)
		require.NoError(t, err)
		assert.Equal(t, StateAcked, job.State)
		assert.Equal(t, []byte("success output"), job.Result)
		assert.Empty(t, job.ResultError)
		w2.Close()
	})

	t.Run("recover job with error", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - ack job with error
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)
		id, _ := core1.Submit([]byte("job"), 3)
		core1.Lease(30 * time.Second)
		core1.Ack(id, nil, "connection timeout")
		w1.Close()

		// Second core - recover
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		job, err := core2.GetJob(id)
		require.NoError(t, err)
		assert.Equal(t, StateAcked, job.State)
		assert.Empty(t, job.Result)
		assert.Equal(t, "connection timeout", job.ResultError)

		w2.Close()
	})

	t.Run("recover job with both result and error", func(t *testing.T) {
		tmpDir := t.TempDir()

		// First core - ack job with partial result and error
		w1, _ := wal.Open(tmpDir)
		core1, _ := NewCore(w1)
		id, _ := core1.Submit([]byte("job"), 3)
		core1.Lease(30 * time.Second)
		core1.Ack(id, []byte("partial data"), "warning: incomplete")
		w1.Close()

		// Second core - recover
		w2, _ := wal.Open(tmpDir)
		core2, _ := NewCore(w2)

		job, err := core2.GetJob(id)
		require.NoError(t, err)
		assert.Equal(t, StateAcked, job.State)
		assert.Equal(t, []byte("partial data"), job.Result)
		assert.Equal(t, "warning: incomplete", job.ResultError)

		w2.Close()
	})
}

// Add new test for leasing RETRY jobs
func TestLeaseRetryJobs(t *testing.T) {
	t.Run("can lease job in retry state", func(t *testing.T) {
		core, _ := setupTestCore(t)

		// Submit a job
		jobID, _ := core.Submit([]byte("test"), 3)

		// Manually set it to RETRY state
		core.mu.Lock()
		core.jobs[jobID].State = StateRetry
		core.jobs[jobID].Attempts = 1
		core.mu.Unlock()

		// Should be able to lease it
		job, err := core.Lease(30 * time.Second)
		require.NoError(t, err)
		assert.Equal(t, jobID, job.ID)
		assert.Equal(t, StateRunning, job.State)
		assert.Equal(t, 2, job.Attempts, "attempts should increment")
	})

	t.Run("retry state is leasable", func(t *testing.T) {
		core, _ := setupTestCore(t)

		// Create job and manually set to RETRY
		jobID, _ := core.Submit([]byte("test"), 3)
		core.mu.Lock()
		job := core.jobs[jobID]
		job.State = StateRetry
		job.Attempts = 2
		core.mu.Unlock()

		// Verify CanLease returns true for RETRY
		assert.True(t, job.CanLease(), "job in RETRY state should be leasable")

		// Actually lease it
		leasedJob, err := core.Lease(30 * time.Second)
		require.NoError(t, err)
		assert.Equal(t, jobID, leasedJob.ID)
		assert.Equal(t, 3, leasedJob.Attempts)
	})
}

// Test: Concurrent operations
func TestConcurrentOperations(t *testing.T) {
	tests := []struct {
		name           string
		numGoroutines  int
		operationsEach int
	}{
		{
			name:           "10 concurrent submits",
			numGoroutines:  10,
			operationsEach: 1,
		},
		{
			name:           "100 concurrent submits",
			numGoroutines:  100,
			operationsEach: 1,
		},
		{
			name:           "concurrent submit and lease",
			numGoroutines:  50,
			operationsEach: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, _ := setupTestCore(t)

			done := make(chan bool)
			errors := make(chan error, tt.numGoroutines)

			// Concurrent submits
			for i := 0; i < tt.numGoroutines; i++ {
				go func(n int) {
					defer func() { done <- true }()

					for j := 0; j < tt.operationsEach; j++ {
						_, err := core.Submit([]byte("job"), 3)
						if err != nil {
							errors <- err
						}
					}
				}(i)
			}

			// Wait for completion
			for i := 0; i < tt.numGoroutines; i++ {
				<-done
			}
			close(errors)

			// Check for errors
			for err := range errors {
				t.Errorf("concurrent operation failed: %v", err)
			}

			// Verify job count
			core.mu.RLock()
			jobCount := len(core.jobs)
			core.mu.RUnlock()

			expectedCount := tt.numGoroutines * tt.operationsEach
			assert.Equal(t, expectedCount, jobCount)
		})
	}
}
