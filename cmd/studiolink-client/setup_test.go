package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"studiolink/internal/shim"
	"studiolink/internal/tokenfile"
)

func TestNormalizeBridgeURL(t *testing.T) {
	good := map[string]string{
		"192.168.106.1":           "http://192.168.106.1:44770/mcp",
		"myhost":                  "http://myhost:44770/mcp",
		"myhost:1234":             "http://myhost:1234/mcp",
		"http://host":             "http://host:44770/mcp",
		"https://host.ts.net":     "https://host.ts.net/mcp",
		"https://host.ts.net:443": "https://host.ts.net:443/mcp",
		"http://host:44770/mcp":   "http://host:44770/mcp",
		"  https://h:8443/x  ":    "https://h:8443/x",
		"[::1]":                   "http://[::1]:44770/mcp",
		"http://host:":            "http://host:44770/mcp",
	}
	for in, want := range good {
		got, err := normalizeBridgeURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeBridgeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "ftp://host", "http://", "https://", "://x", "http://:44770"} {
		if got, err := normalizeBridgeURL(in); err == nil {
			t.Errorf("normalizeBridgeURL(%q) = %q, want an error", in, got)
		}
	}
}

// stubSetup replaces the setup seams for one test. The bridge answers only at
// 192.168.106.1 and accepts testToken; the agents named in found are on PATH.
// Agent commands are appended to calls. It returns the absolute path that
// stands in for this exe.
func stubSetup(t *testing.T, calls *[]string, found ...string) string {
	t.Helper()
	oldHealth, oldProbe, oldLook, oldRun := probeHealth, probeBridge, lookPath, runAgent
	oldExe, oldAdd := executable, addUserPath
	t.Cleanup(func() {
		probeHealth, probeBridge, lookPath, runAgent = oldHealth, oldProbe, oldLook, oldRun
		executable, addUserPath = oldExe, oldAdd
	})
	exe, err := filepath.Abs(filepath.Join(t.TempDir(), "studiolink-client"))
	if err != nil {
		t.Fatal(err)
	}
	probeHealth = func(_ context.Context, base string) bool { return base == "http://192.168.106.1:44770" }
	probeBridge = func(_ context.Context, _, token string) (*shim.ProbeResult, error) {
		if token != testToken {
			return nil, errors.New("initialize: studiolink-host rejected the token (check STUDIOLINK_TOKEN or -token-file)")
		}
		return &shim.ProbeResult{Server: "StudioMCP 1.0", Protocol: "2025-06-18", Tools: 12}, nil
	}
	lookPath = func(name string) (string, error) {
		for _, f := range found {
			if f == name {
				return "/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	runAgent = func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(append([]string{name}, args...), " "))
		return nil, nil
	}
	executable = func() (string, error) { return exe, nil }
	addUserPath = func(dir string) (bool, error) {
		t.Errorf("addUserPath(%q) called with -no-install", dir)
		return false, nil
	}
	return exe
}

func TestSetupInteractive(t *testing.T) {
	var calls []string
	exe := stubSetup(t, &calls, "codex")
	dir := t.TempDir()
	getenv := homeEnv(dir, nil)

	var stdout, stderr bytes.Buffer
	in := "192.168.106.1\n" + testToken + "\ny\n"
	if code := run([]string{"setup", "-no-install"}, strings.NewReader(in), &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("setup: %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	t.Logf("sample output:\n%s", stdout.String())

	data, err := os.ReadFile(savedEnvPath(getenv))
	if err != nil {
		t.Fatal(err)
	}
	vals := tokenfile.Values(data)
	if vals["STUDIOLINK_URL"] != "http://192.168.106.1:44770/mcp" || vals[tokenfile.Key] != testToken {
		t.Errorf("saved file = %q", data)
	}
	want := []string{
		"codex mcp remove Roblox_Studio",
		"codex mcp add Roblox_Studio -- " + exe + " connect",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("agent calls = %q, want %q", calls, want)
	}
	if !strings.Contains(stdout.String(), "ok Added to Codex") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestSetupWrongTokenThenRight(t *testing.T) {
	var calls []string
	stubSetup(t, &calls)
	getenv := homeEnv(t.TempDir(), nil)

	var stdout, stderr bytes.Buffer
	in := "192.168.106.1\nwrong-token-0123456789abcdef\n" + testToken + "\n"
	if code := run([]string{"setup", "-no-install", "-agents", "none"}, strings.NewReader(in), &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("setup: %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "refused that token") || !strings.Contains(stdout.String(), "ok Token accepted") {
		t.Errorf("stdout = %q", stdout.String())
	}
	data, err := os.ReadFile(savedEnvPath(getenv))
	if err != nil || tokenfile.Values(data)[tokenfile.Key] != testToken {
		t.Errorf("saved file = %q, %v", data, err)
	}
}

func TestSetupYesWithoutBridge(t *testing.T) {
	var calls []string
	stubSetup(t, &calls, "codex")
	getenv := homeEnv(t.TempDir(), nil)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"setup", "-yes", "-no-install"}, strings.NewReader(""), &stdout, &stderr, getenv); code != 1 {
		t.Fatalf("setup -yes with no bridge: %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(savedEnvPath(getenv)); err == nil {
		t.Error("a failed setup saved a file")
	}
	if len(calls) != 0 {
		t.Errorf("agents were changed: %q", calls)
	}
}

func TestSetupRefusesHostFile(t *testing.T) {
	var calls []string
	stubSetup(t, &calls)
	dir := t.TempDir()
	getenv := homeEnv(dir, nil)
	path := savedEnvPath(getenv)
	hostFile := "# Written by studiolink-host from studiolink.json; edits are overwritten.\nSTUDIOLINK_TOKEN=host-token\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(hostFile), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	in := "192.168.106.1\n" + testToken + "\n"
	if code := run([]string{"setup", "-no-install", "-agents", "none"}, strings.NewReader(in), &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("setup: %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "belongs to studiolink-host") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if data, _ := os.ReadFile(path); string(data) != hostFile {
		t.Errorf("the host's file changed to %q", data)
	}
}

func TestSetupNoAgentsFound(t *testing.T) {
	var calls []string
	exe := stubSetup(t, &calls)
	getenv := homeEnv(t.TempDir(), nil)

	var stdout, stderr bytes.Buffer
	in := "192.168.106.1\n" + testToken + "\n"
	if code := run([]string{"setup", "-no-install"}, strings.NewReader(in), &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("setup: %d stderr %q", code, stderr.String())
	}
	if len(calls) != 0 {
		t.Errorf("agent calls = %q", calls)
	}
	if want := "codex mcp add Roblox_Studio -- " + exe + " connect"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want the manual command %q", stdout.String(), want)
	}
}

func TestSetupBadAgents(t *testing.T) {
	var calls []string
	stubSetup(t, &calls)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"setup", "-agents", "cursor"}, strings.NewReader(""), &stdout, &stderr, homeEnv(t.TempDir(), nil)); code != 2 || !strings.Contains(stderr.String(), `unknown agent "cursor"`) {
		t.Errorf("setup -agents cursor: %d %q", code, stderr.String())
	}
}
