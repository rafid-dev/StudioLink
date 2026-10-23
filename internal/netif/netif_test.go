package netif

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"studiolink/internal/config"
)

func TestListIncludesLoopback(t *testing.T) {
	adapters, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range adapters {
		for _, ip := range a.IPv4 {
			if ip.IsLoopback() {
				if ips, err := IPv4(a.Name); err != nil || len(ips) == 0 {
					t.Errorf("IPv4(%q) = %v, %v", a.Name, ips, err)
				}
				return
			}
		}
	}
	t.Errorf("no loopback adapter in %v", adapters)
}

func TestIPv4NotFound(t *testing.T) {
	if _, err := IPv4("no such adapter 1234"); !errors.As(err, new(ErrNotFound)) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestKind(t *testing.T) {
	cases := map[string]string{
		"vEthernet (WSL (Hyper-V firewall))": "WSL2",
		"vEthernet (Default Switch)":         "Hyper-V VMs",
		"VMware Network Adapter VMnet8":      "VMware NAT",
		"Tailscale":                          "Tailscale",
		"Wi-Fi":                              "physical",
	}
	for name, want := range cases {
		if got := (Adapter{Name: name}).Kind(); !strings.Contains(got, want) {
			t.Errorf("Kind(%q) = %q, want it to mention %q", name, got, want)
		}
	}
	vbox := Adapter{Name: "Ethernet 2", IPv4: []net.IP{net.ParseIP("192.168.56.1")}}
	if !strings.Contains(vbox.Kind(), "VirtualBox") {
		t.Errorf("192.168.56.1 adapter Kind = %q", vbox.Kind())
	}
}

// fakeListener records whether it was closed.
type fakeListener struct {
	addr   string
	closed chan struct{}
	once   sync.Once
}

func (f *fakeListener) Accept() (net.Conn, error) { <-f.closed; return nil, net.ErrClosed }
func (f *fakeListener) Close() error              { f.once.Do(func() { close(f.closed) }); return nil }
func (f *fakeListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestWatcherFollowsAddressChanges(t *testing.T) {
	var mu sync.Mutex
	ips := map[string][]net.IP{} // adapter -> current addresses; missing = not found
	opened := map[string]*fakeListener{}
	served := make(chan string, 10)

	w := &Watcher{
		Specs:    []config.InterfaceListen{{Name: "WSL", Port: 44770}},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Interval: 10 * time.Millisecond,
		Serve:    func(ln net.Listener) { served <- ln.(*fakeListener).addr },
		lookup: func(name string) ([]net.IP, error) {
			mu.Lock()
			defer mu.Unlock()
			got, ok := ips[name]
			if !ok {
				return nil, ErrNotFound{name}
			}
			return got, nil
		},
		listen: func(addr string) (net.Listener, error) {
			mu.Lock()
			defer mu.Unlock()
			ln := &fakeListener{addr: addr, closed: make(chan struct{})}
			opened[addr] = ln
			return ln, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	// Adapter appears (WSL started).
	mu.Lock()
	ips["WSL"] = []net.IP{net.ParseIP("172.21.80.1")}
	mu.Unlock()
	expectServed(t, served, "172.21.80.1:44770")

	// Address changes (reboot): old listener closes, new one opens.
	mu.Lock()
	ips["WSL"] = []net.IP{net.ParseIP("172.30.0.1")}
	first := opened["172.21.80.1:44770"]
	mu.Unlock()
	expectServed(t, served, "172.30.0.1:44770")
	expectClosed(t, first)

	cancel()
	<-done
	mu.Lock()
	second := opened["172.30.0.1:44770"]
	mu.Unlock()
	expectClosed(t, second)
}

func expectServed(t *testing.T, served chan string, want string) {
	t.Helper()
	select {
	case got := <-served:
		if got != want {
			t.Fatalf("served %s, want %s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("never served %s", want)
	}
}

func expectClosed(t *testing.T, ln *fakeListener) {
	t.Helper()
	select {
	case <-ln.closed:
	case <-time.After(2 * time.Second):
		t.Fatalf("listener %s was not closed", ln.addr)
	}
}
