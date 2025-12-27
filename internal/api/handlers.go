package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// Server holds the queue core and HTTP handlers
type Server struct {
	core          *queue.Core
	leaseDuration time.Duration
}

// NewServer creates a new API server
func NewServer(core *queue.Core, leaseDuration time.Duration) *Server {
	return &Server{
		core:          core,
		leaseDuration: leaseDuration,
	}
}

// SubmitJobRequest represents the job submission request
type SubmitJobRequest struct {
	Payload    string `json:"payload"`
	MaxRetries int    `json:"max_retries"`
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

	// Submit job
	jobID, err := s.core.Submit([]byte(req.Payload), req.MaxRetries)
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
		CreatedAt:   job.CreatedAt,
		LeaseUntil:  leaseUntil,
		Result:      string(job.Result),
		ResultError: job.ResultError,
	}

	s.sendJSON(w, response, http.StatusOK)
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

// RegisterRoutes registers all HTTP routes
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.HandleHealth)
	mux.HandleFunc("/jobs", s.HandleSubmitJob)
	mux.HandleFunc("/jobs/lease", s.HandleLeaseJob)
	mux.HandleFunc("/jobs/", func(w http.ResponseWriter, r *http.Request) {
		// Route to either GET /jobs/{id} or POST /jobs/{id}/ack
		if r.URL.Path[len(r.URL.Path)-4:] == "/ack" {
			s.HandleAckJob(w, r)
		} else {
			s.HandleGetJob(w, r)
		}
	})
}
