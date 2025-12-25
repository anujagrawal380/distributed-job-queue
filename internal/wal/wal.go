package wal

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// WAL represents the Write-Ahead Log
type WAL struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
}

// Open opens or creates a WAL file
func Open(dir string) (*WAL, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create WAL directory: %w", err)
	}

	filePath := filepath.Join(dir, "jobs.wal")

	// Open file in append mode, create if not exists
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL file: %w", err)
	}

	return &WAL{
		file:     file,
		filePath: filePath,
	}, nil
}

// Append writes an entry to the WAL and fsyncs
func (w *WAL) Append(entry *Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Encode entry to JSON
	data, err := entry.Encode()
	if err != nil {
		return fmt.Errorf("failed to encode entry: %w", err)
	}

	// Write to file
	if _, err := w.file.Write(data); err != nil {
		return fmt.Errorf("failed to write entry: %w", err)
	}

	// fsync to ensure durability
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync WAL: %w", err)
	}

	return nil
}

// Replay reads all entries from the WAL
func (w *WAL) Replay() ([]*Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Seek to beginning
	if _, err := w.file.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("failed to seek WAL: %w", err)
	}

	var entries []*Entry
	scanner := bufio.NewScanner(w.file)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue // Skip empty lines
		}

		entry, err := DecodeEntry(line)
		if err != nil {
			return nil, fmt.Errorf("failed to decode entry: %w", err)
		}

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read WAL: %w", err)
	}

	// Seek back to end for future appends
	if _, err := w.file.Seek(0, 2); err != nil {
		return nil, fmt.Errorf("failed to seek to end: %w", err)
	}

	return entries, nil
}

// Close closes the WAL file
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.file.Sync(); err != nil {
		return err
	}

	return w.file.Close()
}
