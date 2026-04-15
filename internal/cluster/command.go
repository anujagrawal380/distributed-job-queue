package cluster

import (
	"encoding/json"

	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
)

// CmdKind discriminates the union below. JSON is used for log entries so they
// stay debuggable on disk.
type CmdKind uint8

const (
	CmdSubmit CmdKind = iota + 1
	CmdLease
	CmdAck
	CmdExpire
)

// Command is the single replicated mutation type. Fields are reused across
// kinds to keep the wire format compact.
type Command struct {
	Kind CmdKind `json:"k"`
	// NowNs is the leader's wall clock at proposal time, used so all replicas
	// agree on timestamps. UnixNano.
	NowNs int64 `json:"t"`

	// Submit
	JobID      string `json:"id,omitempty"`
	Payload    []byte `json:"p,omitempty"`
	MaxRetries int    `json:"r,omitempty"`
	Priority   int    `json:"pr,omitempty"`
	RunAtNs    int64  `json:"ra,omitempty"`

	// Lease
	LeaseDurNs int64 `json:"ld,omitempty"`

	// Ack
	Result      []byte `json:"res,omitempty"`
	ResultError string `json:"err,omitempty"`
}

func (c *Command) encode() ([]byte, error) { return json.Marshal(c) }
func decodeCommand(b []byte) (*Command, error) {
	var c Command
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyResult is what FSM.Apply returns to the proposer (only meaningful on
// the leader; followers ignore the return value). Job is a value copy so the
// caller can use it outside the FSM lock.
type applyResult struct {
	Job     *queue.Job
	Retried []string
	Dead    []string
	Err     string
}
