package netif

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"time"

	"studiolink/internal/config"
)

// Watcher keeps one listener open on each current IPv4 address of each
// configured adapter, rechecking every Interval.
type Watcher struct {
	Specs    []config.InterfaceListen
	Serve    func(net.Listener) // called in its own goroutine per listener
	Log      *slog.Logger
	Interval time.Duration

	// Overridable in tests.
	lookup func(name string) ([]net.IP, error)
	listen func(addr string) (net.Listener, error)

	active map[string]net.Listener // by address
	state  map[string]string       // last logged problem per adapter
}

// Run binds and rebinds until ctx ends, then closes every listener.
func (w *Watcher) Run(ctx context.Context) {
	if w.lookup == nil {
		w.lookup = IPv4
	}
	if w.listen == nil {
		w.listen = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
	}
	if w.Interval == 0 {
		w.Interval = 30 * time.Second
	}
	w.active = make(map[string]net.Listener)
	w.state = make(map[string]string)
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		w.sync()
		select {
		case <-ctx.Done():
			for addr, ln := range w.active {
				ln.Close()
				delete(w.active, addr)
			}
			return
		case <-ticker.C:
		}
	}
}

// sync opens listeners for new addresses and closes ones that went away.
func (w *Watcher) sync() {
	want := make(map[string]string) // address -> adapter name
	for _, spec := range w.Specs {
		ips, err := w.lookup(spec.Name)
		switch {
		case errors.As(err, new(ErrNotFound)):
			w.note(spec.Name, "adapter not found yet; will keep checking", err)
		case err != nil:
			w.note(spec.Name, "cannot read adapter", err)
		case len(ips) == 0:
			w.note(spec.Name, "adapter has no usable IPv4 address yet; will keep checking", nil)
		default:
			w.note(spec.Name, "", nil)
		}
		for _, ip := range ips {
			want[net.JoinHostPort(ip.String(), strconv.Itoa(spec.Port))] = spec.Name
		}
	}
	for addr, ln := range w.active {
		if _, ok := want[addr]; !ok {
			w.Log.Info("adapter address gone; stopped listening", "addr", addr)
			ln.Close()
			delete(w.active, addr)
		}
	}
	for addr, name := range want {
		if _, ok := w.active[addr]; ok {
			continue
		}
		ln, err := w.listen(addr)
		if err != nil {
			w.Log.Warn("cannot listen on adapter; will retry", "adapter", name, "addr", addr, "err", err)
			continue
		}
		w.active[addr] = ln
		w.Log.Info("listening", "adapter", name, "url", "http://"+addr+"/mcp")
		go w.Serve(ln)
	}
}

// note logs an adapter's problem once, and logs again when it clears.
func (w *Watcher) note(name, problem string, err error) {
	if w.state[name] == problem {
		return
	}
	prev := w.state[name]
	w.state[name] = problem
	switch {
	case problem != "":
		w.Log.Warn(problem, "adapter", name, "err", err)
	case prev != "":
		w.Log.Info("adapter available", "adapter", name)
	}
}
