package main

import (
	"bytes"
	"context"
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
	"studiolink/internal/tokenfile"
)

// TestMain keeps tests from finding a real bridge running on this machine.
func TestMain(m *testing.M) {
	probeHealth = func(context.Context, string) bool { return false }
	os.Exit(m.Run())
}

// homeEnv points every OS's config directory at dir.
func homeEnv(dir string, extra map[string]string) func(string) string {
	vars := map[string]string{"APPDATA": dir, "HOME": dir, "XDG_CONFIG_HOME": dir}
	for k, v := range extra {
		vars[k] = v
	}
	return env(vars)
}

func TestParseGateway(t *testing.T) {
	table := "Iface\tDestination\tGateway \tFlags\n" +
		"eth0\t0050A8C0\t00000000\t0001\n" +
		"eth0\t00000000\t0150A8C0\t0003\n"
	if got := parseGateway(table); got != "192.168.80.1" {
		t.Errorf("parseGateway = %q, want 192.168.80.1", got)
	}
	if got := parseGateway("Iface\tDestination\tGateway\n"); got != "" {
		t.Errorf("parseGateway without a default route = %q", got)
	}
}

func TestReadTokenOrder(t *testing.T) {
	dir := t.TempDir()
	saved := savedEnvPath(homeEnv(dir, nil))
	if err := tokenfile.WriteValues(saved, "", "STUDIOLINK_URL", "http://10.0.0.5:44770/mcp", tokenfile.Key, "from-saved"); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dir, "bare")
	if err := os.WriteFile(bare, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, file string
		vars       map[string]string
		want       string
	}{
		{"flag wins", bare, map[string]string{"STUDIOLINK_TOKEN": "from-env"}, "from-file"},
		{"env", "", map[string]string{"STUDIOLINK_TOKEN": "from-env"}, "from-env"},
		{"token file env", "", map[string]string{"STUDIOLINK_TOKEN_FILE": bare}, "from-file"},
		{"saved", "", nil, "from-saved"},
	}
	for _, tt := range tests {
		got, err := readToken(tt.file, homeEnv(dir, tt.vars))
		if err != nil || got != tt.want {
			t.Errorf("%s: readToken = %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
	if got := savedURL(homeEnv(dir, nil)); got != "http://10.0.0.5:44770/mcp" {
		t.Errorf("savedURL = %q", got)
	}
	if _, err := readToken("", homeEnv(t.TempDir(), nil)); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("readToken with nothing = %v", err)
	}
}

func TestDiscoverURL(t *testing.T) {
	defer func(old func(context.Context, string) bool) { probeHealth = old }(probeHealth)
	var tried []string
	probeHealth = func(_ context.Context, base string) bool {
		tried = append(tried, base)
		return true
	}
	got, err := discoverURL(env(nil))
	if err != nil || got != "http://127.0.0.1:44770/mcp" {
		t.Errorf("discoverURL = %q, %v", got, err)
	}
}

func TestSave(t *testing.T) {
	srv := hostserver.New(hostserver.Options{
		Token:  testToken,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Spawn:  func() (hostserver.Process, error) { return mcptest.NewFakeProc(time.Second), nil },
	})
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	dir := t.TempDir()
	getenv := homeEnv(dir, nil)

	var stdout, stderr bytes.Buffer
	// A wrong token is checked and not saved.
	if code := run([]string{"save", "-url", ts.URL}, strings.NewReader("wrong-token-0123456789abcdef0123456789\n"), &stdout, &stderr, getenv); code != 1 {
		t.Fatalf("save with a wrong token: %d %q", code, stderr.String())
	}
	if _, err := os.Stat(savedEnvPath(getenv)); err == nil {
		t.Fatal("a failed save wrote the file")
	}
	// The token is read from piped input, never the command line.
	if code := run([]string{"save", "-url", ts.URL}, strings.NewReader(testToken+"\n"), &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("save: %d %q", code, stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"check"}, nil, &stdout, &stderr, getenv); code != 0 || !strings.Contains(stdout.String(), ts.URL+"/mcp") {
		t.Fatalf("check after save: %d %q %q", code, stdout.String(), stderr.String())
	}

	// The bridge's own file on the Studio PC is left alone.
	host := t.TempDir()
	hostEnv := homeEnv(host, map[string]string{"STUDIOLINK_TOKEN": testToken})
	if err := tokenfile.Write(savedEnvPath(hostEnv), testToken); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"save", "-url", ts.URL}, nil, &stdout, &stderr, hostEnv); code != 1 || !strings.Contains(stderr.String(), "belongs to studiolink-host") {
		t.Errorf("save over the host's file: %d %q", code, stderr.String())
	}
}
