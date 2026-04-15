package cluster

import (
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/raft"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// ErrNotLeader is returned from write methods when this node is a follower.
// The API layer already redirects to the leader via LeaderInfo, so callers
// should rarely see this in practice.
var ErrNotLeader = fmt.Errorf("not leader")

// Server is the cluster-mode JobBackend. Writes go through Raft; reads come
// from the local FSM state.
type Server struct {
	node *Node
	bus  *queue.EventBus

	// applyTimeout bounds how long we wait for a Raft round-trip.
	applyTimeout time.Duration

	// httpAddrs maps raft NodeID -> advertised HTTP address, so the HTTP
	// layer can redirect writes to the leader.
	httpAddrs map[string]string

	mu sync.RWMutex
}

// NewServer builds a cluster-backed queue server.
//
// httpAddrs maps each node's Raft ServerID to the full HTTP URL that API
// clients should follow for redirects (e.g. "http://node1:8080"). Redirects
// include the scheme so http.Redirect produces an absolute URL.
func NewServer(node *Node, bus *queue.EventBus, httpAddrs map[string]string) *Server {
	return &Server{
		node:         node,
		bus:          bus,
		applyTimeout: 5 * time.Second,
		httpAddrs:    httpAddrs,
	}
}

// ----- LeaderInfo -----

// IsLeader reports whether this node is the current Raft leader.
func (s *Server) IsLeader() bool {
	return s.node.Raft.State() == raft.Leader
}

// LeaderHTTPAddr returns the HTTP URL of the current leader, or "" if the
// leader is unknown (e.g. mid-election).
func (s *Server) LeaderHTTPAddr() string {
	_, id := s.node.Raft.LeaderWithID()
	if id == "" {
		return ""
	}
	return s.httpAddrs[string(id)]
}

// ----- JobBackend writes -----

// SubmitWithOptions generates an ID + timestamp, proposes a Submit command,
// and returns the replicated job ID.
func (s *Server) SubmitWithOptions(payload []byte, opts queue.SubmitOptions) (string, error) {
	if len(payload) == 0 {
		return "", fmt.Errorf("payload cannot be empty")
	}
	if opts.MaxRetries < 0 {
		return "", fmt.Errorf("max_retries must be >= 0")
	}
	if !s.IsLeader() {
		return "", ErrNotLeader
	}
	now := time.Now()
	var runAtNs int64
	if !opts.RunAt.IsZero() {
		runAtNs = opts.RunAt.UnixNano()
	}
	cmd := &Command{
		Kind:       CmdSubmit,
		NowNs:      now.UnixNano(),
		JobID:      newJobID(),
		Payload:    payload,
		MaxRetries: opts.MaxRetries,
		Priority:   opts.Priority,
		RunAtNs:    runAtNs,
	}
	res, err := s.apply(cmd)
	if err != nil {
		return "", err
	}
	if res.Err != "" {
		return "", fmt.Errorf("%s", res.Err)
	}
	return res.Job.ID, nil
}

// Lease proposes a Lease command; the FSM picks the best job deterministically.
func (s *Server) Lease(leaseDuration time.Duration) (*queue.Job, error) {
	if leaseDuration <= 0 {
		return nil, fmt.Errorf("lease duration must be positive")
	}
	if !s.IsLeader() {
		return nil, ErrNotLeader
	}
	cmd := &Command{
		Kind:       CmdLease,
		NowNs:      time.Now().UnixNano(),
		LeaseDurNs: int64(leaseDuration),
	}
	res, err := s.apply(cmd)
	if err != nil {
		return nil, err
	}
	if res.Err != "" {
		return nil, fmt.Errorf("%s", res.Err)
	}
	return res.Job, nil
}

// Ack proposes an Ack command.
func (s *Server) Ack(jobID string, result []byte, resultError string) error {
	if jobID == "" {
		return fmt.Errorf("job_id cannot be empty")
	}
	if !s.IsLeader() {
		return ErrNotLeader
	}
	cmd := &Command{
		Kind:        CmdAck,
		NowNs:       time.Now().UnixNano(),
		JobID:       jobID,
		Result:      result,
		ResultError: resultError,
	}
	res, err := s.apply(cmd)
	if err != nil {
		return err
	}
	if res.Err != "" {
		return fmt.Errorf("%s", res.Err)
	}
	return nil
}

// CheckExpiredLeases is called from the server loop on the leader; proposes
// an Expire command so all replicas transition expired jobs uniformly.
func (s *Server) CheckExpiredLeases() {
	if !s.IsLeader() {
		return
	}
	cmd := &Command{Kind: CmdExpire, NowNs: time.Now().UnixNano()}
	_, _ = s.apply(cmd)
}

// apply encodes the command, submits it to Raft, and waits for apply.
func (s *Server) apply(cmd *Command) (*applyResult, error) {
	b, err := cmd.encode()
	if err != nil {
		return nil, fmt.Errorf("encode cmd: %w", err)
	}
	fut := s.node.Raft.Apply(b, s.applyTimeout)
	if err := fut.Error(); err != nil {
		return nil, err
	}
	res, ok := fut.Response().(*applyResult)
	if !ok {
		return nil, fmt.Errorf("unexpected apply response type")
	}
	return res, nil
}

// ----- JobBackend reads (served locally) -----

// GetJob returns a snapshot of the job from this node's FSM.
func (s *Server) GetJob(jobID string) (*queue.Job, error) {
	if jobID == "" {
		return nil, fmt.Errorf("job_id cannot be empty")
	}
	j, ok := s.node.FSM.readGetJob(jobID)
	if !ok {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}
	return j, nil
}

// Stats returns local FSM stats.
func (s *Server) Stats() queue.Stats { return s.node.FSM.readStats() }

// ListJobs returns a local FSM listing.
func (s *Server) ListJobs(f queue.ListFilter) []queue.JobSummary {
	return s.node.FSM.readListJobs(f)
}

// Subscribe returns a channel of local FSM events.
func (s *Server) Subscribe() (<-chan queue.Event, func()) { return s.bus.Subscribe() }
