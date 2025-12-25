package wal

import (
	"encoding/json"
	"time"
)

// EventType represents the type of WAL event
type EventType string

const (
	EventJobCreated EventType = "JOB_CREATED"
	EventJobLeased  EventType = "JOB_LEASED"
	EventJobAcked   EventType = "JOB_ACKED"
	EventJobRetry   EventType = "JOB_RETRY"
	EventJobDead    EventType = "JOB_DEAD"
)

// Entry represents a single WAL entry
type Entry struct {
	JobID     string                 `json:"job_id"`
	Event     EventType              `json:"event"`
	Timestamp int64                  `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// NewEntry creates a new WAL entry
func NewEntry(jobID string, event EventType, metadata map[string]interface{}) *Entry {
	return &Entry{
		JobID:     jobID,
		Event:     event,
		Timestamp: time.Now().Unix(),
		Metadata:  metadata,
	}
}

// Encode serializes the entry to JSON bytes with newline
func (e *Entry) Encode() ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	// Append newline for line-by-line reading
	return append(data, '\n'), nil
}

// DecodeEntry deserializes a JSON line into an Entry
func DecodeEntry(data []byte) (*Entry, error) {
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}
