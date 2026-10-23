package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "no args", args: nil, wantCode: 2, wantStderr: "Usage:"},
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "studiolink-host dev (protocol 1)"},
		{name: "help", args: []string{"--help"}, wantCode: 0, wantStdout: "Commands:"},
		{name: "unknown", args: []string{"nope"}, wantCode: 2, wantStderr: `unknown command "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
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

func TestInitThenToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "studiolink.json")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "-config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("init: %d %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Bearer ") && !strings.Contains(stdout.String(), "<token>") {
		t.Error("init printed the token; it should print a placeholder")
	}

	stdout.Reset()
	if code := run([]string{"token", "-config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("token: %d %s", code, stderr.String())
	}
	if len(strings.TrimSpace(stdout.String())) < 32 {
		t.Errorf("token too short: %q", stdout.String())
	}

	stderr.Reset()
	if code := run([]string{"init", "-config", path}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("second init without -force: %d %q", code, stderr.String())
	}
}

func TestTokenWithoutConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "missing.json")
	if code := run([]string{"token", "-config", path}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "run studiolink-host init") {
		t.Errorf("token without config: %d %q", code, stderr.String())
	}
}
