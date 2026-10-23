package main

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"studiolink/internal/config"
	"studiolink/internal/netif"
)

// TestMain keeps a test from taking "no arguments" for a double-click and
// installing StudioLink for real.
func TestMain(m *testing.M) {
	openedFromExplorer = func() bool { return false }
	os.Exit(m.Run())
}

func TestAddToPath(t *testing.T) {
	dir := `C:\Users\me\AppData\Local\Programs\StudioLink`
	tests := []struct {
		list, want string
		added      bool
	}{
		{"", dir, true},
		{`C:\a;C:\b`, `C:\a;C:\b;` + dir, true},
		{`C:\a;`, `C:\a;` + dir, true},
		{`C:\a;` + strings.ToUpper(dir) + `\`, `C:\a;` + strings.ToUpper(dir) + `\`, false},
		{`%USERPROFILE%\bin;` + dir, `%USERPROFILE%\bin;` + dir, false},
	}
	for _, tt := range tests {
		got, added := addToPath(tt.list, dir)
		if got != tt.want || added != tt.added {
			t.Errorf("addToPath(%q) = %q, %v; want %q, %v", tt.list, got, added, tt.want, tt.added)
		}
	}
}

func TestPrompter(t *testing.T) {
	var out bytes.Buffer
	p := &prompter{in: bufio.NewReader(strings.NewReader("maybe\nn\n\ny\n")), out: &out}
	if p.ask("A?", true) {
		t.Error("A: want no after an unclear answer then n")
	}
	if !p.ask("B?", true) {
		t.Error("B: an empty line takes the default")
	}
	if !p.ask("C?", false) {
		t.Error("C: want yes")
	}
	if p.ask("D?", false) || !p.ask("E?", true) {
		t.Error("at end of input, take the default")
	}
	yes := &prompter{in: bufio.NewReader(strings.NewReader("n\n")), out: &out, yes: true}
	if !yes.ask("F?", true) {
		t.Error("-yes takes the default without reading")
	}
}

func TestInstallFiles(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "Programs", "StudioLink")
	exe := filepath.Join(src, "studiolink-host.exe")
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(exe, "host v1")
	write(filepath.Join(src, "clients", "studiolink-client-linux-amd64"), "client")
	write(filepath.Join(src, "clients", "notes.txt"), "not copied")
	write(filepath.Join(src, "clients", clientFile("windows-"+runtime.GOARCH)), "own client")

	got, err := installFiles(exe, dst)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dst, "studiolink-host.exe") {
		t.Errorf("installed exe = %s", got)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "clients", "studiolink-client-linux-amd64")); string(data) != "client" {
		t.Errorf("client not copied: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "studiolink-client.exe")); string(data) != "own client" {
		t.Errorf("this PC's client not installed: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dst, "clients", "notes.txt")); err == nil {
		t.Error("copied a file that is not a client build")
	}
	// An upgrade replaces the exe.
	write(exe, "host v2")
	if _, err := installFiles(exe, dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(got); string(data) != "host v2" {
		t.Errorf("after upgrade: %q", data)
	}
	if !sameFile(got, got) || sameFile(exe, got) {
		t.Error("sameFile")
	}
}

func TestWSLAdapter(t *testing.T) {
	wsl := netif.Adapter{Name: "vEthernet (WSL (Hyper-V firewall))", Up: true, IPv4: []net.IP{net.IPv4(172, 21, 80, 1)}}
	lan := netif.Adapter{Name: "Ethernet", Up: true, IPv4: []net.IP{net.IPv4(192, 168, 1, 2)}}
	cfg := &config.Config{}
	if a, ok := wslAdapter(cfg, []netif.Adapter{lan, wsl}); !ok || a.Name != wsl.Name {
		t.Errorf("wslAdapter = %v, %v", a, ok)
	}
	if _, ok := wslAdapter(cfg, []netif.Adapter{lan}); ok {
		t.Error("found WSL without its adapter")
	}
	cfg.ListenInterfaces = []config.InterfaceListen{{Name: wsl.Name, Port: 44770}}
	if _, ok := wslAdapter(cfg, []netif.Adapter{lan, wsl}); ok {
		t.Error("offered WSL when it is already served")
	}
}
