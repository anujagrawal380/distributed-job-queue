package cluster

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/hashicorp/raft"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// FSM is the Raft-replicated state machine. All replicas apply the same
// log stream in the same order, so state stays identical.
type FSM struct {
	mu    sync.RWMutex
	state *state
	bus   *queue.EventBus
}

// NewFSM creates an empty FSM. The event bus is used to publish state
// transitions to local SSE subscribers (not replicated — each node publishes
// to its own local subscribers as log entries commit).
func NewFSM(bus *queue.EventBus) *FSM {
	return &FSM{state: newState(), bus: bus}
}

// Apply implements raft.FSM. It's called on every node for every committed
// entry. Return value is only observed by the leader (via ApplyFuture).
func (f *FSM) Apply(log *raft.Log) interface{} {
	cmd, err := decodeCommand(log.Data)
	if err != nil {
		return &applyResult{Err: fmt.Sprintf("decode: %v", err)}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch cmd.Kind {
	case CmdSubmit:
		j := f.state.applySubmit(cmd)
		f.publish(queue.Event{Kind: queue.EventSubmitted, JobID: j.ID, State: j.State})
		jobCopy := *j
		return &applyResult{Job: &jobCopy}
	case CmdLease:
		j := f.state.applyLease(cmd)
		if j == nil {
			return &applyResult{Err: "no jobs available"}
		}
		f.publish(queue.Event{Kind: queue.EventLeased, JobID: j.ID, State: j.State})
		jobCopy := *j
		return &applyResult{Job: &jobCopy}
	case CmdAck:
		if err := f.state.applyAck(cmd); err != nil {
			return &applyResult{Err: err.Error()}
		}
		j := f.state.jobs[cmd.JobID]
		f.publish(queue.Event{Kind: queue.EventAcked, JobID: j.ID, State: j.State})
		jobCopy := *j
		return &applyResult{Job: &jobCopy}
	case CmdExpire:
		retried, dead := f.state.applyExpire(cmd)
		for _, id := range retried {
			f.publish(queue.Event{Kind: queue.EventRetry, JobID: id, State: queue.StateReady})
		}
		for _, id := range dead {
			f.publish(queue.Event{Kind: queue.EventDead, JobID: id, State: queue.StateDead})
		}
		return &applyResult{Retried: retried, Dead: dead}
	default:
		return &applyResult{Err: fmt.Sprintf("unknown cmd kind: %d", cmd.Kind)}
	}
}

func (f *FSM) publish(e queue.Event) {
	if f.bus != nil {
		f.bus.Publish(e)
	}
}

// snapshotData is the on-disk form of an FSM snapshot.
type snapshotData struct {
	Jobs     []queue.Job `json:"jobs"`
	Counters counters    `json:"counters"`
}

// Snapshot implements raft.FSM. Called periodically; must be cheap to capture
// and safe to stream without holding the write lock for long.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	jobs, c := f.state.snapshot()
	return &fsmSnapshot{data: snapshotData{Jobs: jobs, Counters: c}}, nil
}

// Restore implements raft.FSM. Replaces state from a snapshot stream.
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var d snapshotData
	if err := json.NewDecoder(rc).Decode(&d); err != nil {
		return fmt.Errorf("snapshot decode: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.restore(d.Jobs, d.Counters)
	return nil
}

// readStats / readListJobs / readGetJob are local reads. They acquire the
// read lock so they are consistent with the FSM's own view — not necessarily
// the latest leader view (followers can be slightly behind).
func (f *FSM) readStats() queue.Stats {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state.stats()
}

func (f *FSM) readListJobs(fl queue.ListFilter) []queue.JobSummary {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state.listJobs(fl)
}

func (f *FSM) readGetJob(id string) (*queue.Job, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	j, ok := f.state.jobs[id]
	if !ok {
		return nil, false
	}
	c := *j
	return &c, true
}

// fsmSnapshot implements raft.FSMSnapshot. Persist streams JSON to the sink.
type fsmSnapshot struct {
	data snapshotData
}

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if err := json.NewEncoder(sink).Encode(s.data); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}
