// Package audit writes studiolink-host's audit log: one JSON object per
// line for every session, tool call and refused request.
package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMaxBytes is the size at which the log rotates. One previous file
// (path + ".1") is kept.
const DefaultMaxBytes = 10 << 20

// Results recorded for tool calls.
const (
	ResultOK        = "ok"         // the tool ran and reported success
	ResultToolError = "tool_error" // the tool ran and reported an error (isError)
	ResultError     = "error"      // a JSON-RPC error, e.g. unknown tool
	ResultDenied    = "denied"     // blocked by the tool policy; never reached Studio
	ResultNoReply   = "no_reply"   // the client left or StudioMCP exited first
)

// Event is one audit record. Empty fields are left out.
type Event struct {
	Time       time.Time       `json:"time"`
	Event      string          `json:"event"` // session_open, session_close, tool_call, auth_refused
	Session    string          `json:"session,omitempty"`
	Client     string          `json:"client,omitempty"`
	Remote     string          `json:"remote,omitempty"`
	Peer       string          `json:"peer,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Result     string          `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	ToolCalls  int             `json:"tool_calls,omitempty"`
}

// Log is an append-only JSON-lines file. A nil *Log records nothing.
type Log struct {
	path     string
	maxBytes int64
	warn     *slog.Logger

	mu     sync.Mutex
	f      *os.File
	size   int64
	failed bool // a write failed; warned once
}

// Open opens (or creates) the log at path for appending.
func Open(path string, maxBytes int64, warn *slog.Logger) (*Log, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &Log{path: path, maxBytes: maxBytes, warn: warn}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Log) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("audit log: %w", err)
	}
	l.f, l.size = f, info.Size()
	return nil
}

// Path returns the file's location.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Record appends one event, stamping its time if unset.
func (l *Log) Record(e Event) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return // closed
	}
	if l.size+int64(len(line)) > l.maxBytes && l.size > 0 {
		l.rotate()
	}
	n, err := l.f.Write(line)
	l.size += int64(n)
	if err != nil && !l.failed {
		l.failed = true
		l.warn.Error("cannot write the audit log; events are being lost", "path", l.path, "err", err)
	}
}

// rotate moves the current file to path.1, replacing an older one.
func (l *Log) rotate() {
	l.f.Close()
	old := l.path + ".1"
	_ = os.Remove(old)
	if err := os.Rename(l.path, old); err != nil {
		l.warn.Warn("cannot rotate the audit log; it will keep growing", "path", l.path, "err", err)
	}
	if err := l.open(); err != nil {
		l.f = nil
		l.warn.Error("cannot reopen the audit log; events are being lost", "err", err)
	}
}

// Close flushes and closes the file.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
