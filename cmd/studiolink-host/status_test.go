package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"studiolink/internal/hostserver"
)

func TestRunFilePath(t *testing.T) {
	if got := runFilePath(filepath.Join("a", "studiolink.json")); got != filepath.Join("a", "studiolink.run.json") {
		t.Errorf("runFilePath = %q", got)
	}
}

func TestClaimRunFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "studiolink.run.json")
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer live.Close()

	rf, err := claimRunFile(path, live.URL)
	if err != nil {
		t.Fatal(err)
	}
	// A second copy finds the first one answering.
	if _, err := claimRunFile(path, "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second claim: %v", err)
	}
	rf.release()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("release left the run file")
	}

	// A stale file from a crashed bridge is taken over.
	os.WriteFile(path, []byte(`{"pid":1,"status_url":"http://127.0.0.1:1"}`), 0o600)
	if rf, err = claimRunFile(path, live.URL); err != nil {
		t.Fatalf("claim over a stale file: %v", err)
	}
	// release leaves a file another process has since claimed.
	os.WriteFile(path, []byte(`{"pid":1,"status_url":"http://127.0.0.1:1"}`), 0o600)
	rf.release()
	if _, err := os.Stat(path); err != nil {
		t.Error("release removed another process's run file")
	}
}

func TestFetchStatus(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "studiolink.json")
	if _, err := fetchStatus(context.Background(), cfgPath, "tok"); !errors.Is(err, errNotRunning) {
		t.Errorf("no run file: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "missing or invalid token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(Status{Version: "v9", Sessions: []hostserver.SessionInfo{{ID: "abcd1234"}}})
	}))
	defer srv.Close()
	data, _ := json.Marshal(runInfo{PID: 42, StatusURL: srv.URL})
	os.WriteFile(runFilePath(cfgPath), data, 0o600)

	st, err := fetchStatus(context.Background(), cfgPath, "tok")
	if err != nil || st.Version != "v9" || len(st.Sessions) != 1 {
		t.Errorf("fetchStatus = %+v, %v", st, err)
	}
	if _, err := fetchStatus(context.Background(), cfgPath, "bad"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong token: %v", err)
	}
	srv.Close()
	if _, err := fetchStatus(context.Background(), cfgPath, "tok"); !errors.Is(err, errNotRunning) {
		t.Errorf("stale run file: %v", err)
	}
}

func TestPrintStatus(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	st := &Status{
		Version: "dev", PID: 7, Started: now.Add(-time.Hour), Config: `C:\x\studiolink.json`,
		Serving:  []string{"127.0.0.1:44770"},
		Tailnet:  "http://studiolink.t.ts.net",
		AuditLog: `C:\x\audit.jsonl`,
		Relays: []RelayStatus{
			{URL: "wss://a/tunnel", Connected: true, Since: now.Add(-time.Minute)},
			{URL: "wss://b/tunnel", LastError: "relay refused the tunnel: 401"},
		},
		Sessions: []hostserver.SessionInfo{
			{ID: "abcd1234", Client: "owner", Remote: "127.0.0.1:5", Opened: now.Add(-2 * time.Minute), LastUsed: now.Add(-time.Second), ToolCalls: 3},
			{ID: "ffff0000", Client: "vps", Remote: "relay x", Peer: "me@github", Opened: now, LastUsed: now, InFlight: 1},
		},
	}
	var out bytes.Buffer
	printStatus(&out, st, now)
	for _, want := range []string{
		"pid 7, up 1h0m0s",
		"serving:   127.0.0.1:44770, http://studiolink.t.ts.net",
		"wss://a/tunnel connected for 1m0s",
		"wss://b/tunnel DOWN: relay refused the tunnel: 401",
		"audit.jsonl",
		"2 session(s)",
		"abcd1234  owner",
		"me@github",
		"busy (1)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	printStatus(&out, &Status{Started: now}, now)
	if !strings.Contains(out.String(), "No sessions.") {
		t.Errorf("empty status:\n%s", out.String())
	}
}
