package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/anujagrawal380/distributed-job-queue/internal/api"
	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
	"github.com/anujagrawal380/distributed-job-queue/internal/wal"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// IntegrationTestServer holds all components for integration testing
type IntegrationTestServer struct {
	Handler   http.Handler
	Redis     *miniredis.Miniredis
	WALDir    string
	ClientKey string
	WorkerKey string
	AdminKey  string
	AuthStore auth.Store
	QueueCore *queue.Core
	WAL       *wal.WAL
}

// setupIntegrationTest creates a full test environment
func setupIntegrationTest(t *testing.T) *IntegrationTestServer {
	return setupIntegrationTestWithDir(t, t.TempDir())
}

// setupIntegrationTestWithDir creates test environment with specific WAL directory
func setupIntegrationTestWithDir(t *testing.T, walDir string) *IntegrationTestServer {
	t.Helper()

	// Start miniredis (in-memory Redis)
	mr := miniredis.RunT(t)

	// Create Redis client
	redisClient := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	// Create auth store
	authStore := auth.NewRedisStore(redisClient)

	// Seed API keys
	ctx := context.Background()
	clientKey := seedClientKey(t, authStore, ctx)
	workerKey := seedWorkerKey(t, authStore, ctx)
	adminKey := seedAdminKey(t, authStore, ctx)

	// Open WAL
	w, err := wal.Open(walDir)
	require.NoError(t, err)

	// Create queue core
	core, err := queue.NewCore(w)
	require.NoError(t, err)

	// Create API server
	server := api.NewServer(core, 30*time.Second, authStore)

	// Register routes
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	return &IntegrationTestServer{
		Handler:   mux,
		Redis:     mr,
		WALDir:    walDir,
		ClientKey: clientKey,
		WorkerKey: workerKey,
		AdminKey:  adminKey,
		AuthStore: authStore,
		QueueCore: core,
		WAL:       w,
	}
}

// Teardown cleans up test resources
func (s *IntegrationTestServer) Teardown() {
	s.WAL.Close()
	s.Redis.Close()
}

// seedClientKey creates a test client key
func seedClientKey(t *testing.T, store auth.Store, ctx context.Context) string {
	t.Helper()
	key, err := auth.GenerateAPIKey("Test Client", "test-client", auth.KeyTypeClient)
	require.NoError(t, err)
	require.NoError(t, store.CreateKey(ctx, key))
	return key.Key
}

// seedWorkerKey creates a test worker key
func seedWorkerKey(t *testing.T, store auth.Store, ctx context.Context) string {
	t.Helper()
	key, err := auth.GenerateAPIKey("Test Worker", "test-worker", auth.KeyTypeWorker)
	require.NoError(t, err)
	require.NoError(t, store.CreateKey(ctx, key))
	return key.Key
}

// seedAdminKey creates a test admin key
func seedAdminKey(t *testing.T, store auth.Store, ctx context.Context) string {
	t.Helper()
	key, err := auth.GenerateAPIKey("Test Admin", "test-admin", auth.KeyTypeAdmin)
	require.NoError(t, err)
	require.NoError(t, store.CreateKey(ctx, key))
	return key.Key
}

// HTTP helper methods

// SubmitJob submits a job and returns the job ID
func (s *IntegrationTestServer) SubmitJob(apiKey, payload string, maxRetries int) string {
	resp := s.SubmitJobRaw(apiKey, payload, maxRetries)
	if resp.StatusCode != http.StatusCreated {
		panic(fmt.Sprintf("SubmitJob failed: %d - %s", resp.StatusCode, readBody(resp)))
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	return result["job_id"]
}

// SubmitJobRaw submits a job and returns the raw response
func (s *IntegrationTestServer) SubmitJobRaw(apiKey, payload string, maxRetries int) *http.Response {
	body := fmt.Sprintf(`{"payload":"%s","max_retries":%d}`, payload, maxRetries)
	req := httptest.NewRequest("POST", "/jobs", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// LeaseJob leases a job and returns the lease response
func (s *IntegrationTestServer) LeaseJob(apiKey string) LeaseJobResponse {
	resp := s.LeaseJobRaw(apiKey)
	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("LeaseJob failed: %d - %s", resp.StatusCode, readBody(resp)))
	}

	var result LeaseJobResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

// LeaseJobRaw leases a job and returns the raw response
func (s *IntegrationTestServer) LeaseJobRaw(apiKey string) *http.Response {
	req := httptest.NewRequest("POST", "/jobs/lease", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// AckJob acknowledges a job
func (s *IntegrationTestServer) AckJob(apiKey, jobID, result, resultError string) {
	resp := s.AckJobRaw(apiKey, jobID, result, resultError)
	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("AckJob failed: %d - %s", resp.StatusCode, readBody(resp)))
	}
}

// AckJobRaw acknowledges a job and returns the raw response
func (s *IntegrationTestServer) AckJobRaw(apiKey, jobID, result, resultError string) *http.Response {
	body := fmt.Sprintf(`{"result":"%s","result_error":"%s"}`, result, resultError)
	req := httptest.NewRequest("POST", "/jobs/"+jobID+"/ack", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// GetJob gets a job status
func (s *IntegrationTestServer) GetJob(apiKey, jobID string) JobStatusResponse {
	resp := s.GetJobRaw(apiKey, jobID)
	if resp.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("GetJob failed: %d - %s", resp.StatusCode, readBody(resp)))
	}

	var result JobStatusResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

// GetJobRaw gets a job status and returns the raw response
func (s *IntegrationTestServer) GetJobRaw(apiKey, jobID string) *http.Response {
	req := httptest.NewRequest("GET", "/jobs/"+jobID, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// Response types (matching API responses)

type LeaseJobResponse struct {
	JobID      string    `json:"job_id"`
	Payload    string    `json:"payload"`
	LeaseUntil time.Time `json:"lease_until"`
	Attempt    int       `json:"attempt"`
}

type JobStatusResponse struct {
	JobID       string     `json:"job_id"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	MaxRetries  int        `json:"max_retries"`
	CreatedAt   time.Time  `json:"created_at"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	Result      string     `json:"result,omitempty"`
	ResultError string     `json:"result_error,omitempty"`
}

// readBody reads and returns response body as string
func readBody(resp *http.Response) string {
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body)
}

// CreateKey creates a new API key and returns the key string
func (s *IntegrationTestServer) CreateKey(apiKey, name, ownerID, keyType string) string {
	resp := s.CreateKeyRaw(apiKey, map[string]string{
		"name":     name,
		"owner_id": ownerID,
		"type":     keyType,
	})
	if resp.StatusCode != http.StatusCreated {
		panic(fmt.Sprintf("CreateKey failed: %d - %s", resp.StatusCode, readBody(resp)))
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result["key"].(string)
}

// CreateKeyRaw creates a new API key and returns the raw response
func (s *IntegrationTestServer) CreateKeyRaw(apiKey string, keyData map[string]string) *http.Response {
	jsonData, _ := json.Marshal(keyData)
	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBuffer(jsonData))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// ListKeysRaw lists own keys and returns the raw response
func (s *IntegrationTestServer) ListKeysRaw(apiKey string) *http.Response {
	req := httptest.NewRequest("GET", "/keys", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// ListAllKeysRaw lists all keys (admin only) and returns the raw response
func (s *IntegrationTestServer) ListAllKeysRaw(apiKey string) *http.Response {
	req := httptest.NewRequest("GET", "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}

// RevokeKeyRaw revokes a key and returns the raw response
func (s *IntegrationTestServer) RevokeKeyRaw(apiKey, keyToRevoke string) *http.Response {
	req := httptest.NewRequest("DELETE", "/keys/"+keyToRevoke, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	rr := httptest.NewRecorder()
	s.Handler.ServeHTTP(rr, req)
	return rr.Result()
}
