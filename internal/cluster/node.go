package cluster

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

// Peer describes a cluster member: stable ID plus the Raft transport address.
type Peer struct {
	ID   string
	Addr string
}

// NodeConfig is the minimum needed to start a Raft node.
type NodeConfig struct {
	NodeID    string // unique within the cluster (e.g. "node1")
	RaftAddr  string // TCP address this node binds, e.g. "0.0.0.0:7000"
	DataDir   string // where BoltDB + snapshots live
	Bootstrap bool   // true on exactly one node, on first boot only
	Peers     []Peer // full cluster membership (used when Bootstrap=true)
}

// Node wraps raft.Raft with the stores + transport so the caller can shut
// them down cleanly.
type Node struct {
	Raft      *raft.Raft
	FSM       *FSM
	transport *raft.NetworkTransport
	logStore  *raftboltdb.BoltStore
	snapStore raft.SnapshotStore
}

// NewNode starts a Raft node. On first boot with Bootstrap=true, it seeds
// the cluster configuration from Peers.
func NewNode(cfg NodeConfig, fsm *FSM) (*Node, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.NodeID)
	// Snapshot thresholds tuned small so demos produce snapshots quickly.
	rc.SnapshotInterval = 30 * time.Second
	rc.SnapshotThreshold = 1024

	addr, err := net.ResolveTCPAddr("tcp", cfg.RaftAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve raft addr: %w", err)
	}
	// advertiseAddr is what we tell peers to dial; use the configured one
	// directly (docker-compose service names work here).
	transport, err := raft.NewTCPTransport(cfg.RaftAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("tcp transport: %w", err)
	}

	logStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-log.bolt"))
	if err != nil {
		return nil, fmt.Errorf("log store: %w", err)
	}
	// Reuse the same BoltDB for stable storage (terms, votes).
	stableStore, err := raftboltdb.NewBoltStore(filepath.Join(cfg.DataDir, "raft-stable.bolt"))
	if err != nil {
		return nil, fmt.Errorf("stable store: %w", err)
	}

	snapStore, err := raft.NewFileSnapshotStore(cfg.DataDir, 3, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("snapshot store: %w", err)
	}

	r, err := raft.NewRaft(rc, fsm, logStore, stableStore, snapStore, transport)
	if err != nil {
		return nil, fmt.Errorf("new raft: %w", err)
	}

	if cfg.Bootstrap {
		hasState, err := raft.HasExistingState(logStore, stableStore, snapStore)
		if err != nil {
			return nil, fmt.Errorf("check existing state: %w", err)
		}
		if !hasState {
			servers := make([]raft.Server, 0, len(cfg.Peers))
			for _, p := range cfg.Peers {
				servers = append(servers, raft.Server{
					ID:      raft.ServerID(p.ID),
					Address: raft.ServerAddress(p.Addr),
				})
			}
			bootCfg := raft.Configuration{Servers: servers}
			if err := r.BootstrapCluster(bootCfg).Error(); err != nil {
				return nil, fmt.Errorf("bootstrap: %w", err)
			}
		}
	}

	return &Node{
		Raft:      r,
		FSM:       fsm,
		transport: transport,
		logStore:  logStore,
		snapStore: snapStore,
	}, nil
}

// Shutdown stops the Raft node and closes stores.
func (n *Node) Shutdown() error {
	if err := n.Raft.Shutdown().Error(); err != nil {
		return err
	}
	if err := n.transport.Close(); err != nil {
		return err
	}
	return n.logStore.Close()
}
