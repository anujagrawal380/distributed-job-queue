package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
	"github.com/anujagrawal380/distributed-job-queue/internal/dashboard"
	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// Server holds the queue backend and HTTP handlers.
type Server struct {
	core          JobBackend
	leader        LeaderInfo // optional; nil means single-node (always leader)
	leaseDuration time.Duration
	authStore     auth.Store
}

// NewServer creates a new API server. Pass nil for leader if running single-node.
func NewServer(core JobBackend, leader LeaderInfo, leaseDuration time.Duration, authStore auth.Store) *Server {
	return &Server{
		core:          core,
		leader:        leader,
		leaseDuration: leaseDuration,
		authStore:     authStore,
	}
}

// requireLeader enforces that writes go to the current leader. If this node
// is a follower, redirect to the leader's HTTP address with 307 (preserves
// method and body).
func (s *Server) requireLeader(w http.ResponseWriter, r *http.Request) bool {
	if s.leader == nil || s.leader.IsLeader() {
		return true
	}
	addr := s.leader.LeaderHTTPAddr()
	if addr == "" {
		s.sendError(w, "no leader available; cluster is electing", http.StatusServiceUnavailable)
		return false
	}
	http.Redirect(w, r, addr+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	return false
}

// SubmitJobRequest represents the job submission request
type SubmitJobRequest struct {
	Payload    string     `json:"payload"`
	MaxRetries int        `json:"max_retries"`
	Priority   int        `json:"priority,omitempty"`
	RunAt      *time.Time `json:"run_at,omitempty"`
	DelayMs    int64      `json:"delay_ms,omitempty"`
}

// SubmitJobResponse represents the job submission response
type SubmitJobResponse struct {
	JobID string `json:"job_id"`
}

// AckJobRequest represents the job acknowledgment request
type AckJobRequest struct {
	Result      string `json:"result,omitempty"`
	ResultError string `json:"result_error,omitempty"`
}

// LeaseJobResponse represents the lease response
type LeaseJobResponse struct {
	JobID      string    `json:"job_id"`
	Payload    string    `json:"payload"`
	LeaseUntil time.Time `json:"lease_until"`
	Attempt    int       `json:"attempt"`
}

// JobStatusResponse represents the job status response
type JobStatusResponse struct {
	JobID       string         `json:"job_id"`
	State       queue.JobState `json:"state"`
	Attempts    int            `json:"attempts"`
	MaxRetries  int            `json:"max_retries"`
	Priority    int            `json:"priority"`
	RunAt       time.Time      `json:"run_at"`
	CreatedAt   time.Time      `json:"created_at"`
	LeaseUntil  *time.Time     `json:"lease_until,omitempty"`
	Result      string         `json:"result,omitempty"`
	ResultError string         `json:"result_error,omitempty"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error"`
}

// HandleSubmitJob handles POST /jobs
func (s *Server) HandleSubmitJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireLeader(w, r) {
		return
	}

	// Parse request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.sendError(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req SubmitJobRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.sendError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.Payload == "" {
		s.sendError(w, "payload is required", http.StatusBadRequest)
		return
	}
	if req.MaxRetries < 0 {
		s.sendError(w, "max_retries must be >= 0", http.StatusBadRequest)
		return
	}

	// Build submit options
	opts := queue.SubmitOptions{
		MaxRetries: req.MaxRetries,
		Priority:   req.Priority,
	}
	if req.RunAt != nil {
		opts.RunAt = *req.RunAt
	} else if req.DelayMs > 0 {
		opts.RunAt = time.Now().Add(time.Duration(req.DelayMs) * time.Millisecond)
	}

	jobID, err := s.core.SubmitWithOptions([]byte(req.Payload), opts)
	if err != nil {
		s.sendError(w, fmt.Sprintf("failed to submit job: %v", err), http.StatusInternalServerError)
		return
	}

	// Send response
	s.sendJSON(w, SubmitJobResponse{JobID: jobID}, http.StatusCreated)
}

// HandleLeaseJob handles POST /jobs/lease
func (s *Server) HandleLeaseJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireLeader(w, r) {
		return
	}

	// Lease a job
	job, err := s.core.Lease(s.leaseDuration)
	if err != nil {
		s.sendError(w, fmt.Sprintf("no jobs available: %v", err), http.StatusNotFound)
		return
	}

	response := LeaseJobResponse{
		JobID:      job.ID,
		Payload:    string(job.Payload),
		LeaseUntil: job.LeaseUntil,
		Attempt:    job.Attempts,
	}

	s.sendJSON(w, response, http.StatusOK)
}

// HandleAckJob handles POST /jobs/{job_id}/ack
func (s *Server) HandleAckJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireLeader(w, r) {
		return
	}

	// Extract job ID from path
	jobID := r.URL.Path[len("/jobs/"):]
	if idx := len(jobID) - len("/ack"); idx > 0 && jobID[idx:] == "/ack" {
		jobID = jobID[:idx]
	}

	if jobID == "" {
		s.sendError(w, "job_id is required", http.StatusBadRequest)
		return
	}

	// Parse request body
	var req AckJobRequest
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err == nil && len(body) > 0 {
			json.Unmarshal(body, &req)
		}
		r.Body.Close()
	}

	// Ack the job with result
	if err := s.core.Ack(jobID, []byte(req.Result), req.ResultError); err != nil {
		s.sendError(w, fmt.Sprintf("failed to ack job: %v", err), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandleGetJob handles GET /jobs/{job_id}
func (s *Server) HandleGetJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job ID from path
	jobID := r.URL.Path[len("/jobs/"):]
	if jobID == "" {
		s.sendError(w, "job_id is required", http.StatusBadRequest)
		return
	}

	// Get the job
	job, err := s.core.GetJob(jobID)
	if err != nil {
		s.sendError(w, fmt.Sprintf("job not found: %v", err), http.StatusNotFound)
		return
	}

	// Build response
	var leaseUntil *time.Time
	if !job.LeaseUntil.IsZero() {
		leaseUntil = &job.LeaseUntil
	}

	response := JobStatusResponse{
		JobID:       job.ID,
		State:       job.State,
		Attempts:    job.Attempts,
		MaxRetries:  job.MaxRetries,
		Priority:    job.Priority,
		RunAt:       job.RunAt,
		CreatedAt:   job.CreatedAt,
		LeaseUntil:  leaseUntil,
		Result:      string(job.Result),
		ResultError: job.ResultError,
	}

	s.sendJSON(w, response, http.StatusOK)
}

// HandleStats returns a snapshot of queue stats for dashboards.
func (s *Server) HandleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.sendJSON(w, s.core.Stats(), http.StatusOK)
}

// HandleListJobs returns recent jobs, optionally filtered by ?state=.
func (s *Server) HandleListJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	filter := queue.ListFilter{
		State:  queue.JobState(strings.ToUpper(q.Get("state"))),
		Limit:  parseIntDefault(q.Get("limit"), 50),
		Offset: parseIntDefault(q.Get("offset"), 0),
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	s.sendJSON(w, s.core.ListJobs(filter), http.StatusOK)
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// HandleEvents streams queue events as Server-Sent Events.
func (s *Server) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.sendError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	events, unsub := s.core.Subscribe()
	defer unsub()

	// Initial stats push so dashboards paint immediately.
	if data, err := json.Marshal(s.core.Stats()); err == nil {
		fmt.Fprintf(w, "event: stats\ndata: %s\n\n", data)
		flusher.Flush()
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: job\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			// periodic stats refresh keeps charts alive even if idle
			if data, err := json.Marshal(s.core.Stats()); err == nil {
				fmt.Fprintf(w, "event: stats\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}
	}
}

// HandleHealth handles GET /health
func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Helper: send JSON response
func (s *Server) sendJSON(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

// Helper: send error response
func (s *Server) sendError(w http.ResponseWriter, message string, statusCode int) {
	s.sendJSON(w, ErrorResponse{Error: message}, statusCode)
}

// RegisterRoutes registers all HTTP routes with authentication
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// Health check (no auth required)
	mux.HandleFunc("/health", s.HandleHealth)

	// Auth middleware wrapper
	authMW := AuthMiddleware(s.authStore)

	// Jobs endpoints (require auth + specific scopes)
	// POST /jobs -> submit; GET /jobs -> list
	mux.Handle("/jobs", authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			RequireScope(auth.ScopeJobsSubmit)(http.HandlerFunc(s.HandleSubmitJob)).ServeHTTP(w, r)
		case http.MethodGet:
			RequireScope(auth.ScopeJobsRead)(http.HandlerFunc(s.HandleListJobs)).ServeHTTP(w, r)
		default:
			s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})))

	mux.Handle("/jobs/lease", authMW(RequireScope(auth.ScopeJobsLease)(http.HandlerFunc(s.HandleLeaseJob))))

	// Stats: any authenticated read-capable user
	mux.Handle("/stats", authMW(RequireScope(auth.ScopeJobsRead)(http.HandlerFunc(s.HandleStats))))

	// SSE stream for live dashboard updates
	mux.Handle("/events", authMW(RequireScope(auth.ScopeJobsRead)(http.HandlerFunc(s.HandleEvents))))

	// Jobs/{id} routes need custom handling (GET vs POST /ack)
	mux.Handle("/jobs/", authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route to either GET /jobs/{id} or POST /jobs/{id}/ack
		if strings.HasSuffix(r.URL.Path, "/ack") {
			// Require jobs:ack scope for ack endpoint
			RequireScope(auth.ScopeJobsAck)(http.HandlerFunc(s.HandleAckJob)).ServeHTTP(w, r)
		} else {
			// Require jobs:read scope for get job endpoint
			RequireScope(auth.ScopeJobsRead)(http.HandlerFunc(s.HandleGetJob)).ServeHTTP(w, r)
		}
	})))

	// Admin-only: create keys
	mux.Handle("/admin/keys", authMW(RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Create key - admin only
			s.HandleCreateKey(w, r)
		} else if r.Method == http.MethodGet {
			// List ALL keys - admin only, very sensitive
			s.HandleListAllKeys(w, r)
		} else {
			s.sendError(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))))

	// User endpoint: list OWN keys (any authenticated user with keys:read)
	mux.Handle("/keys", authMW(RequireScope(auth.ScopeKeysRead)(http.HandlerFunc(s.HandleListKeys))))

	// Revoke key: needs scope + ownership check in handler
	mux.Handle("/keys/", authMW(RequireScope(auth.ScopeKeysRevoke)(http.HandlerFunc(s.HandleRevokeKey))))

	// Dashboard UI (public; it gates itself with an API key entered by the user).
	// Assets live under /ui/, and / redirects to /ui/index.html for a clean entry.
	dash := dashboard.Handler()
	mux.Handle("/ui/", http.StripPrefix("/ui/", dash))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/index.html", http.StatusFound)
	})
}
