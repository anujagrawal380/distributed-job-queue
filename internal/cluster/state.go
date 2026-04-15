// Package cluster runs the queue as a Raft-replicated cluster. The leader
// accepts writes; all nodes apply committed log entries through the FSM.
package cluster

import (
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// state is the in-memory job table. Mutations are NOT thread-safe; the FSM
// holds a write mutex around Apply.
type state struct {
	jobs     map[string]*queue.Job
	counters counters
}

type counters struct {
	Submits, Acks, Retries, Dead uint64
}

func newState() *state {
	return &state{jobs: make(map[string]*queue.Job)}
}

// applySubmit creates a new job from a Submit command. The leader generates
// JobID + Now so all replicas agree.
func (s *state) applySubmit(c *Command) *queue.Job {
	now := time.Unix(0, c.NowNs)
	runAt := time.Unix(0, c.RunAtNs)
	if c.RunAtNs == 0 {
		runAt = now
	}
	j := &queue.Job{
		ID:         c.JobID,
		Payload:    c.Payload,
		State:      queue.StateReady,
		MaxRetries: c.MaxRetries,
		Priority:   c.Priority,
		RunAt:      runAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	s.jobs[j.ID] = j
	s.counters.Submits++
	return j
}

// applyLease deterministically picks the best runnable job and marks it
// running. Returns nil if nothing is runnable.
func (s *state) applyLease(c *Command) *queue.Job {
	now := time.Unix(0, c.NowNs)
	leaseDur := time.Duration(c.LeaseDurNs)

	// Deterministic pick: sort candidates by (priority desc, RunAt asc,
	// CreatedAt asc, ID asc). All replicas must agree, so we cannot rely on
	// Go's randomized map iteration.
	candidates := make([]*queue.Job, 0)
	for _, j := range s.jobs {
		if j.IsRunnable(now) {
			candidates = append(candidates, j)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, k int) bool {
		a, b := candidates[i], candidates[k]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.RunAt.Equal(b.RunAt) {
			return a.RunAt.Before(b.RunAt)
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})

	j := candidates[0]
	j.State = queue.StateRunning
	j.Attempts++
	j.LeaseUntil = now.Add(leaseDur)
	j.UpdatedAt = now
	return j
}

// applyAck transitions a RUNNING job to ACKED. Returns an error if invalid.
func (s *state) applyAck(c *Command) error {
	now := time.Unix(0, c.NowNs)
	j, ok := s.jobs[c.JobID]
	if !ok {
		return fmt.Errorf("job not found: %s", c.JobID)
	}
	if !j.CanAck() {
		return fmt.Errorf("job cannot be acked in state: %s", j.State)
	}
	j.State = queue.StateAcked
	j.Result = c.Result
	j.ResultError = c.ResultError
	j.UpdatedAt = now
	s.counters.Acks++
	return nil
}

// applyExpire walks all running jobs and transitions expired ones. Returns
// the IDs that moved to RETRY/DEAD so the caller can publish events.
func (s *state) applyExpire(c *Command) (retried, dead []string) {
	now := time.Unix(0, c.NowNs)
	for _, j := range s.jobs {
		if j.State != queue.StateRunning || !now.After(j.LeaseUntil) {
			continue
		}
		if j.ShouldRetry() {
			j.State = queue.StateReady
			retried = append(retried, j.ID)
			s.counters.Retries++
		} else {
			j.State = queue.StateDead
			dead = append(dead, j.ID)
			s.counters.Dead++
		}
		j.UpdatedAt = now
	}
	return retried, dead
}

// snapshot returns a deep copy of the jobs map and counters for FSM snapshot.
func (s *state) snapshot() ([]queue.Job, counters) {
	out := make([]queue.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j) // value copy; payload byte slice is shared but immutable
	}
	return out, s.counters
}

// restore replaces state from a snapshot.
func (s *state) restore(jobs []queue.Job, c counters) {
	s.jobs = make(map[string]*queue.Job, len(jobs))
	for i := range jobs {
		j := jobs[i]
		s.jobs[j.ID] = &j
	}
	s.counters = c
}

// stats builds a queue.Stats snapshot.
func (s *state) stats() queue.Stats {
	now := time.Now()
	out := queue.Stats{
		ByState:      make(map[queue.JobState]int),
		TotalSubmits: s.counters.Submits,
		TotalAcks:    s.counters.Acks,
		TotalRetries: s.counters.Retries,
		TotalDead:    s.counters.Dead,
		SnapshotAt:   now,
	}
	for _, j := range s.jobs {
		out.Total++
		out.ByState[j.State]++
		if j.CanLease() {
			if now.Before(j.RunAt) {
				out.Scheduled++
			} else {
				out.Runnable++
			}
		}
	}
	return out
}

// listJobs returns summaries newest-first, optionally filtered by state.
func (s *state) listJobs(f queue.ListFilter) []queue.JobSummary {
	matched := make([]*queue.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
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
	out := make([]queue.JobSummary, 0, end-start)
	for _, j := range matched[start:end] {
		out = append(out, queue.JobSummary{
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

// newJobID generates a UUID for a new job. Lives here so the leader makes the
// ID before proposing — the FSM must use the same value on every replica.
func newJobID() string { return uuid.New().String() }
