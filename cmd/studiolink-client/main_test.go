package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"studiolink/internal/hostserver"
	"studiolink/internal/mcptest"
)

const testToken = "test-token-0123456789abcdef0123456789"

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestRun(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		args       []string
		env        map[string]string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no args", wantCode: 2, wantStderr: "Usage:"},
		{name: "version", args: []string{"version"}, wantStdout: "studiolink-client dev (protocol 1)"},
		{name: "help", args: []string{"help"}, wantStdout: "connect"},
		{name: "unknown", args: []string{"nope"}, wantCode: 2, wantStderr: `unknown command "nope"`},
		{name: "no url", args: []string{"check"}, wantCode: 2, wantStderr: "no bridge URL"},
		{name: "bad url", args: []string{"check", "-url", "ftp://x"}, wantCode: 2, wantStderr: "bad -url"},
		{name: "no token", args: []string{"check", "-url", "http://127.0.0.1:1/mcp"}, wantCode: 2, wantStderr: "no token"},
		{name: "missing token file", args: []string{"check", "-url", "http://127.0.0.1:1/mcp", "-token-file", filepath.Join(t.TempDir(), "nope")}, wantCode: 2, wantStderr: "token file"},
		{
			name:     "check unreachable",
			args:     []string{"check", "-token-file", tokenFile},
			env:      map[string]string{"STUDIOLINK_URL": "http://127.0.0.1:1"},
			wantCode: 1, wantStderr: "FAIL http://127.0.0.1:1/mcp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, strings.NewReader(""), &stdout, &stderr, env(tt.env)); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestCheckAndConnect(t *testing.T) {
	srv := hostserver.New(hostserver.Options{
		Token:  testToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Spawn:  func() (hostserver.Process, error) { return mcptest.NewFakeProc(time.Second), nil },
	})
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	vars := map[string]string{"STUDIOLINK_URL": ts.URL + "/mcp", "STUDIOLINK_TOKEN": testToken}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"check"}, nil, &stdout, &stderr, env(vars)); code != 0 || !strings.Contains(stdout.String(), "ok") {
		t.Fatalf("check: %d %q %q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	in := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}` + "\n"
	if code := run([]string{"connect"}, strings.NewReader(in), &stdout, &stderr, env(vars)); code != 0 {
		t.Fatalf("connect: %d %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"serverInfo"`) || strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("connect stdout = %q, want exactly the initialize reply", stdout.String())
	}
}

func TestIsTrustedHost(t *testing.T) {
	cases := map[string]bool{
		"localhost":                true,
		"host.docker.internal":     true,
		"studiolink.tail1.ts.net":  true,
		"studio-pc":                true,
		"127.0.0.1":                true,
		"192.168.1.20":             true,
		"172.21.80.1":              true,
		"100.102.67.123":           true,
		"203.0.113.9":              false,
		"bridge.example.com":       false,
		"studio.attacker.ts.net.x": false,
	}
	for host, want := range cases {
		if got := isTrustedHost(host); got != want {
			t.Errorf("isTrustedHost(%q) = %v, want %v", host, got, want)
		}
	}
}
