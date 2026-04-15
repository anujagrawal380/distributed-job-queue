package cluster

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// apply runs a command through the FSM and returns the result. Wraps it in a
// raft.Log so we hit the same code path Raft would.
func apply(t *testing.T, f *FSM, c *Command) *applyResult {
	t.Helper()
	b, err := c.encode()
	require.NoError(t, err)
	res, ok := f.Apply(&raft.Log{Data: b}).(*applyResult)
	require.True(t, ok)
	return res
}

func TestFSM_SubmitLeaseAck(t *testing.T) {
	f := NewFSM(queue.NewEventBus())
	now := time.Now()

	sub := apply(t, f, &Command{
		Kind:       CmdSubmit,
		NowNs:      now.UnixNano(),
		JobID:      "job-1",
		Payload:    []byte("hello"),
		MaxRetries: 3,
	})
	require.Empty(t, sub.Err)
	require.Equal(t, "job-1", sub.Job.ID)
	require.Equal(t, queue.StateReady, sub.Job.State)

	lease := apply(t, f, &Command{
		Kind:       CmdLease,
		NowNs:      now.Add(time.Millisecond).UnixNano(),
		LeaseDurNs: int64(30 * time.Second),
	})
	require.Empty(t, lease.Err)
	require.Equal(t, "job-1", lease.Job.ID)
	require.Equal(t, queue.StateRunning, lease.Job.State)
	require.Equal(t, 1, lease.Job.Attempts)

	ack := apply(t, f, &Command{
		Kind:   CmdAck,
		NowNs:  now.Add(2 * time.Millisecond).UnixNano(),
		JobID:  "job-1",
		Result: []byte("ok"),
	})
	require.Empty(t, ack.Err)
	require.Equal(t, queue.StateAcked, ack.Job.State)
}

func TestFSM_PriorityOrdering(t *testing.T) {
	f := NewFSM(queue.NewEventBus())
	now := time.Now()

	apply(t, f, &Command{Kind: CmdSubmit, NowNs: now.UnixNano(), JobID: "low", Payload: []byte("x"), Priority: 1})
	apply(t, f, &Command{Kind: CmdSubmit, NowNs: now.Add(time.Millisecond).UnixNano(), JobID: "high", Payload: []byte("x"), Priority: 10})
	apply(t, f, &Command{Kind: CmdSubmit, NowNs: now.Add(2 * time.Millisecond).UnixNano(), JobID: "mid", Payload: []byte("x"), Priority: 5})

	lease := apply(t, f, &Command{Kind: CmdLease, NowNs: now.Add(3 * time.Millisecond).UnixNano(), LeaseDurNs: int64(time.Second)})
	require.Equal(t, "high", lease.Job.ID)

	lease2 := apply(t, f, &Command{Kind: CmdLease, NowNs: now.Add(4 * time.Millisecond).UnixNano(), LeaseDurNs: int64(time.Second)})
	require.Equal(t, "mid", lease2.Job.ID)
}

func TestFSM_ExpireRetry(t *testing.T) {
	f := NewFSM(queue.NewEventBus())
	now := time.Now()

	apply(t, f, &Command{Kind: CmdSubmit, NowNs: now.UnixNano(), JobID: "j", Payload: []byte("x"), MaxRetries: 2})
	apply(t, f, &Command{Kind: CmdLease, NowNs: now.UnixNano(), LeaseDurNs: int64(time.Second)})

	// Jump past the lease.
	future := now.Add(5 * time.Second)
	exp := apply(t, f, &Command{Kind: CmdExpire, NowNs: future.UnixNano()})
	require.Equal(t, []string{"j"}, exp.Retried)
	require.Empty(t, exp.Dead)

	// Job should be leasable again.
	lease := apply(t, f, &Command{Kind: CmdLease, NowNs: future.Add(time.Millisecond).UnixNano(), LeaseDurNs: int64(time.Second)})
	require.Equal(t, "j", lease.Job.ID)
	require.Equal(t, 2, lease.Job.Attempts)
}

func TestFSM_SnapshotRestore(t *testing.T) {
	src := NewFSM(queue.NewEventBus())
	now := time.Now()
	apply(t, src, &Command{Kind: CmdSubmit, NowNs: now.UnixNano(), JobID: "a", Payload: []byte("x"), Priority: 5})
	apply(t, src, &Command{Kind: CmdSubmit, NowNs: now.UnixNano(), JobID: "b", Payload: []byte("y"), Priority: 3})

	snap, err := src.Snapshot()
	require.NoError(t, err)

	buf := &bytes.Buffer{}
	sink := &memSink{Buffer: buf}
	require.NoError(t, snap.Persist(sink))

	dst := NewFSM(queue.NewEventBus())
	require.NoError(t, dst.Restore(io.NopCloser(bytes.NewReader(buf.Bytes()))))

	j, ok := dst.readGetJob("a")
	require.True(t, ok)
	require.Equal(t, 5, j.Priority)

	// After restore, a lease picks the higher-priority job.
	lease := apply(t, dst, &Command{Kind: CmdLease, NowNs: now.UnixNano(), LeaseDurNs: int64(time.Second)})
	require.Equal(t, "a", lease.Job.ID)
}

// memSink is an in-memory raft.SnapshotSink for tests.
type memSink struct {
	*bytes.Buffer
}

func (m *memSink) ID() string     { return "mem" }
func (m *memSink) Cancel() error  { return nil }
func (m *memSink) Close() error   { return nil }
