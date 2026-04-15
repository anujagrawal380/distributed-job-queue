package cluster

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// startCluster spins up n real Raft nodes on localhost ports. Each node has
// its own BoltDB on disk (t.TempDir) so this exercises the real code paths.
func startCluster(t *testing.T, n int) []*Node {
	t.Helper()
	addrs := make([]string, n)
	peers := make([]Peer, n)
	for i := 0; i < n; i++ {
		addrs[i] = fmt.Sprintf("127.0.0.1:%d", freePort(t))
		peers[i] = Peer{ID: fmt.Sprintf("node%d", i+1), Addr: addrs[i]}
	}

	nodes := make([]*Node, n)
	for i := 0; i < n; i++ {
		fsm := NewFSM(queue.NewEventBus())
		n, err := NewNode(NodeConfig{
			NodeID:        peers[i].ID,
			RaftAddr:      addrs[i],
			AdvertiseAddr: addrs[i],
			DataDir:       t.TempDir(),
			Bootstrap:     i == 0, // only node1 seeds
			Peers:         peers,
		}, fsm)
		require.NoError(t, err)
		nodes[i] = n
	}
	t.Cleanup(func() {
		for _, n := range nodes {
			if n != nil {
				_ = n.Shutdown()
			}
		}
	})
	return nodes
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitForLeader polls up to d for any node to become leader and returns its index.
func waitForLeader(t *testing.T, nodes []*Node, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for i, n := range nodes {
			if n != nil && n.Raft.State() == raft.Leader {
				return i
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no leader elected within %v", d)
	return -1
}

func TestCluster_ElectAndReplicate(t *testing.T) {
	nodes := startCluster(t, 3)
	leaderIdx := waitForLeader(t, nodes, 10*time.Second)
	leader := NewServer(nodes[leaderIdx], queue.NewEventBus(), nil)

	id, err := leader.SubmitWithOptions([]byte("hello"), queue.SubmitOptions{MaxRetries: 3})
	require.NoError(t, err)

	// Every node's FSM must eventually hold the job.
	require.Eventually(t, func() bool {
		for _, n := range nodes {
			if _, ok := n.FSM.readGetJob(id); !ok {
				return false
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond, "job did not replicate to all nodes")
}

func TestCluster_LeaderFailoverZeroLoss(t *testing.T) {
	nodes := startCluster(t, 3)
	leaderIdx := waitForLeader(t, nodes, 10*time.Second)

	bus := queue.NewEventBus()
	oldLeader := NewServer(nodes[leaderIdx], bus, nil)
	id1, err := oldLeader.SubmitWithOptions([]byte("before-kill"), queue.SubmitOptions{})
	require.NoError(t, err)

	// Kill the leader.
	require.NoError(t, nodes[leaderIdx].Shutdown())
	nodes[leaderIdx] = nil

	// New leader must emerge from the remaining 2 nodes.
	var newLeaderIdx int
	require.Eventually(t, func() bool {
		for i, n := range nodes {
			if n != nil && n.Raft.State() == raft.Leader {
				newLeaderIdx = i
				return true
			}
		}
		return false
	}, 15*time.Second, 100*time.Millisecond, "no new leader after failover")

	newLeader := NewServer(nodes[newLeaderIdx], queue.NewEventBus(), nil)

	// Pre-existing job survived.
	j, err := newLeader.GetJob(id1)
	require.NoError(t, err)
	require.Equal(t, "before-kill", string(j.Payload))

	// New writes still succeed.
	id2, err := newLeader.SubmitWithOptions([]byte("after-failover"), queue.SubmitOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, id2)

	// Lease + ack flow still works end-to-end on the new leader.
	leased, err := newLeader.Lease(5 * time.Second)
	require.NoError(t, err)
	require.NoError(t, newLeader.Ack(leased.ID, []byte("done"), ""))
}
