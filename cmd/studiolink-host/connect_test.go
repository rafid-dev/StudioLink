package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"studiolink/internal/config"
	"studiolink/internal/tokenfile"
)

func newTestConfig(t *testing.T) (string, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "studiolink.json")
	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

func TestConnect(t *testing.T) {
	path, cfg := newTestConfig(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"connect", "-config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("connect: %d %q", code, stderr.String())
	}
	for _, s := range scenarios {
		if !strings.Contains(stdout.String(), "connect "+s.name) {
			t.Errorf("menu lacks %s:\n%s", s.name, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), cfg.Token) {
		t.Error("the menu printed the token")
	}
	if token, err := tokenfile.Read(tokenfile.PathFor(path)); err != nil || token != cfg.Token {
		t.Errorf("token file = %q, %v", token, err)
	}

	stdout.Reset()
	if code := run([]string{"connect", "-config", path, "docker"}, &stdout, &stderr); code != 0 {
		t.Fatalf("connect docker: %d %q", code, stderr.String())
	}
	for _, want := range []string{"http://host.docker.internal:44770/mcp", "Bearer " + cfg.Token, "${STUDIOLINK_TOKEN}", "don't paste them"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("docker recipe lacks %q:\n%s", want, stdout.String())
		}
	}

	for _, name := range []string{"wsl", "vm", "tailscale", "server"} {
		stdout.Reset()
		if code := run([]string{"connect", "-config", path, name}, &stdout, &stderr); code != 0 || stdout.Len() == 0 {
			t.Errorf("connect %s: %d %q", name, code, stderr.String())
		}
	}
	if code := run([]string{"connect", "-config", path, "moon"}, &stdout, &stderr); code != 2 {
		t.Errorf("unknown scenario: %d", code)
	}
}

func TestAddRelay(t *testing.T) {
	path, _ := newTestConfig(t)
	fp := "sha256:" + strings.Repeat("ab", 32)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"add-relay", "-config", path, "vps.example.com:44771", fp}, &stdout, &stderr); code != 0 {
		t.Fatalf("add-relay: %d %q", code, stderr.String())
	}
	fp2 := "sha256:" + strings.Repeat("cd", 32)
	if code := run([]string{"add-relay", "-config", path, "wss://vps.example.com:44771", fp2}, &stdout, &stderr); code != 0 {
		t.Fatalf("add-relay again: %d %q", code, stderr.String())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Relays) != 1 || cfg.Relays[0].URL != "wss://vps.example.com:44771" || cfg.Relays[0].Fingerprint != fp2 {
		t.Errorf("relays = %+v, want one updated entry", cfg.Relays)
	}
	if !strings.Contains(stdout.String(), "Updated") || !strings.Contains(stdout.String(), "not running") {
		t.Errorf("stdout = %q", stdout.String())
	}
	stderr.Reset()
	if code := run([]string{"add-relay", "-config", path, "wss://vps.example.com:44771", "sha256:short"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "fingerprint") {
		t.Errorf("bad fingerprint: %d %q", code, stderr.String())
	}
}

func TestDownloads(t *testing.T) {
	dir := t.TempDir()
	d := downloads{dir: dir}
	mcp := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "mcp") })
	srv := httptest.NewServer(d.wrap(mcp))
	defer srv.Close()
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := get("/install.sh"); code != 404 || !strings.Contains(body, "release build") {
		t.Errorf("install.sh without clients: %d %q", code, body)
	}
	if err := os.WriteFile(filepath.Join(dir, clientFile("linux-amd64")), []byte("ELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, body := get("/install.sh"); code != 200 || !strings.Contains(body, "base='"+srv.URL+"'") {
		t.Errorf("install.sh: %d %q", code, body)
	}
	if code, body := get("/download/studiolink-client-linux-amd64"); code != 200 || body != "ELF" {
		t.Errorf("download: %d %q", code, body)
	}
	if code, _ := get("/download/studiolink.json"); code != 404 {
		t.Errorf("download of a non-client name: %d", code)
	}
	if code, body := get("/mcp"); code != 200 || body != "mcp" {
		t.Errorf("/mcp passthrough: %d %q", code, body)
	}
}
