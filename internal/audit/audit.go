// Package audit writes one JSON line per tool call. A line never holds
// result content. If a line cannot be written, the call fails (fail closed).
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one audit line.
type Entry struct {
	Time            time.Time      `json:"time"`
	Surface         string         `json:"surface"` // "mcp" or "cli"
	Tool            string         `json:"tool"`
	Args            map[string]any `json:"args"` // redacted
	Context         string         `json:"context"`
	Namespace       string         `json:"namespace"`
	Identity        string         `json:"identity"`
	ResultBytes     int            `json:"result_bytes"`
	Truncated       bool           `json:"truncated"`
	Redactions      int            `json:"redactions"`
	BlockedRequests int64          `json:"blocked_requests"`
	DurationMS      int64          `json:"duration_ms"`
	Error           string         `json:"error"` // redacted
	Session         string         `json:"session"`
	Version         string         `json:"version"`
}

// Logger appends entries to a file.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// Open creates the directory (0700) and the file (0600) if needed and opens
// the file for appending. It refuses a file that others can read or write.
func Open(path string) (*Logger, error) {
	if path == "" {
		return nil, fmt.Errorf("audit log path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat audit log: %w", err)
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("audit log %s is not a regular file", path)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 && !isWindows() {
		_ = f.Close()
		return nil, fmt.Errorf("audit log %s has mode %v; it must not be readable or writable by others (chmod 600)", path, perm)
	}
	return &Logger{f: f, path: path}, nil
}

// Path is the file path.
func (l *Logger) Path() string { return l.path }

// Write appends one line with a single write call.
func (l *Logger) Write(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode audit line: %w", err)
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return fmt.Errorf("audit log is closed")
	}
	n, err := l.f.Write(b)
	if err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	if n != len(b) {
		return fmt.Errorf("write audit log: short write")
	}
	return nil
}

// Close closes the file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
