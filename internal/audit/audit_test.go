package audit

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		events = append(events, e)
	}
	return events
}

func TestRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "audit.jsonl")
	l, err := Open(path, 0, quiet)
	if err != nil {
		t.Fatal(err)
	}
	l.Record(Event{Event: "tool_call", Session: "abcd", Tool: "execute_luau", Result: ResultDenied})
	l.Record(Event{Event: "session_close", Session: "abcd", Reason: "idle timeout"})
	l.Close()
	l.Record(Event{Event: "after close"}) // must not panic

	events := readEvents(t, path)
	if len(events) != 2 || events[0].Tool != "execute_luau" || events[0].Time.IsZero() || events[1].Reason != "idle timeout" {
		t.Errorf("events = %+v", events)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"peer"`) {
		t.Errorf("empty fields written: %s", raw)
	}
}

func TestRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path, 300, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for range 10 {
		l.Record(Event{Event: "tool_call", Tool: "list_roblox_studios", Result: ResultOK})
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no rotated file: %v", err)
	}
	if info, _ := os.Stat(path); info.Size() > 300 {
		t.Errorf("current file is %d bytes, over the limit", info.Size())
	}
}

func TestNilLog(t *testing.T) {
	var l *Log
	l.Record(Event{Event: "x"})
	if l.Close() != nil || l.Path() != "" {
		t.Error("nil log misbehaved")
	}
}
