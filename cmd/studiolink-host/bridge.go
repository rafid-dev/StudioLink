package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"studiolink/internal/audit"
	"studiolink/internal/config"
	"studiolink/internal/hostserver"
	"studiolink/internal/netif"
	"studiolink/internal/proc"
	"studiolink/internal/studio"
	"studiolink/internal/tailnet"
	"studiolink/internal/tokenfile"
	"studiolink/internal/tunnel"
	"studiolink/internal/version"
)

// bridge is a running studiolink-host: the MCP server on every configured
// listener, relay tunnel and the tailnet, plus a loopback status port. Both
// serve and tray run one.
type bridge struct {
	cfg     *config.Config
	cfgPath string
	log     *slog.Logger
	started time.Time

	srv     *hostserver.Server
	audit   *audit.Log
	dialers []*tunnel.Dialer
	run     *runFile

	stop context.CancelFunc // ends the bridge, as Ctrl+C does

	mu         sync.Mutex
	tailnetURL string
}

// runBridge serves until ctx ends or a listener fails. settingsURL is the
// settings page the tray serves, recorded in the run file; "" when there is
// none. ready, if set, is called once everything has started.
func runBridge(ctx context.Context, cfgPath string, log *slog.Logger, settingsURL string, ready func(*bridge)) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return fmt.Errorf("cannot resolve config path: %w", err)
	}
	launcher, err := studio.FindLauncher()
	if err != nil {
		return fmt.Errorf("cannot find Studio MCP: %w", err)
	}
	b := &bridge{cfg: cfg, cfgPath: abs, log: log, started: time.Now()}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.stop = cancel

	// The status port goes first: claiming the run file is how a second
	// copy of the bridge finds out it would fight the first one.
	ctrlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("cannot open the status port: %w", err)
	}
	defer ctrlLn.Close()
	b.run, err = claimRunFile(runFilePath(abs), "http://"+ctrlLn.Addr().String(), settingsURL)
	if err != nil {
		return err
	}
	defer b.run.release()

	// Agents' setups read the token from here; keep it in step with the config.
	if err := tokenfile.Write(tokenfile.PathFor(abs), cfg.Token); err != nil {
		log.Warn("cannot write the token file for agents", "path", tokenfile.PathFor(abs), "err", err)
	}

	if path := cfg.AuditPath(abs); path != "" {
		if b.audit, err = audit.Open(path, 0, log); err != nil {
			return err
		}
		defer b.audit.Close()
	}
	clients := make([]hostserver.Client, 0, len(cfg.Clients))
	for _, c := range cfg.Clients {
		clients = append(clients, hostserver.Client{Name: c.Name, Token: c.Token, Tools: c.Tools})
	}
	spec := studio.Spec(launcher)
	b.srv = hostserver.New(hostserver.Options{
		Token:          cfg.Token,
		Tools:          cfg.Tools,
		Clients:        clients,
		Audit:          b.audit,
		AuditArguments: cfg.AuditArguments,
		AllowedOrigins: cfg.AllowedOrigins,
		MaxSessions:    cfg.SessionLimit(),
		IdleTimeout:    cfg.IdleTimeout(),
		Logger:         log,
		Spawn: func() (hostserver.Process, error) {
			return proc.Start(spec)
		},
	})
	defer b.srv.Close()

	files := newDownloads()
	httpSrv := &http.Server{
		Handler:           files.wrap(b.srv.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctrlMux := http.NewServeMux()
	ctrlMux.Handle("/status", b.srv.OwnerOnly(http.HandlerFunc(b.serveStatus)))
	ctrlMux.Handle("POST /stop", b.srv.OwnerOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		log.Info("stop requested")
		w.WriteHeader(http.StatusNoContent)
		b.stop()
	})))
	// /ping tells a second copy that this one is alive without a token, so
	// the check does not land in the audit log as a refused request.
	ctrlMux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "studiolink-host\n")
	})
	ctrlSrv := &http.Server{Handler: ctrlMux, ReadHeaderTimeout: 10 * time.Second}

	// Fixed addresses must bind now; adapters and the tailnet may come later.
	errs := make(chan error, len(cfg.Listen)+2)
	for _, addr := range cfg.Listen {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("cannot listen on %s: %w", addr, err)
		}
		log.Info("listening", "url", "http://"+ln.Addr().String()+"/mcp")
		go func() { errs <- httpSrv.Serve(ln) }()
	}
	go func() { errs <- ctrlSrv.Serve(ctrlLn) }()

	var background sync.WaitGroup
	defer func() {
		cancel()
		background.Wait()
	}()

	if len(cfg.ListenInterfaces) > 0 {
		w := &netif.Watcher{
			Specs: cfg.ListenInterfaces,
			Log:   log,
			// Serve returns when the watcher closes a listener; that is expected.
			Serve: func(ln net.Listener) { _ = httpSrv.Serve(ln) },
		}
		background.Go(func() { w.Run(ctx) })
	}

	var tailSrv *http.Server
	if ts := cfg.Tailscale; ts != nil {
		stateDir := ts.StateDir
		if stateDir == "" {
			stateDir = filepath.Join(filepath.Dir(abs), "tsnet")
		}
		node, err := tailnet.New(ts, stateDir, log.With("tailnet", ts.DeviceName()))
		if err != nil {
			return fmt.Errorf("cannot start tailnet device: %w", err)
		}
		defer node.Close()
		tailSrv = &http.Server{
			Handler:           files.wrap(node.IdentifyPeers(b.srv.Handler(), hostserver.WithPeer)),
			ReadHeaderTimeout: 10 * time.Second,
		}
		background.Go(func() {
			ln, url, err := node.Up(ctx)
			if err != nil {
				if ctx.Err() == nil {
					errs <- err
				}
				return
			}
			b.mu.Lock()
			b.tailnetURL = url
			b.mu.Unlock()
			log.Info("listening on tailnet", "url", url)
			if err := tailSrv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		})
	}
	for _, relay := range cfg.Relays {
		d := &tunnel.Dialer{
			URL:         relay.TunnelURL(),
			Fingerprint: relay.Fingerprint,
			Token:       cfg.Token,
			Log:         log,
			// Serve returns when the tunnel drops; the dialer reconnects.
			Serve: func(ln net.Listener) { _ = httpSrv.Serve(ln) },
		}
		b.dialers = append(b.dialers, d)
		background.Go(func() { d.Run(ctx) })
	}
	attrs := []any{"version", version.Version, "launcher", launcher}
	if files.available() {
		attrs = append(attrs, "client_downloads", true)
	}
	if b.audit != nil {
		attrs = append(attrs, "audit_log", b.audit.Path())
	}
	log.Info("studiolink-host ready", attrs...)
	if ready != nil {
		ready(b)
	}

	var stopErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case stopErr = <-errs:
		log.Error("server stopped", "err", stopErr)
	}
	cancel()
	b.srv.Close()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = ctrlSrv.Shutdown(shutdownCtx)
	if tailSrv != nil {
		_ = tailSrv.Shutdown(shutdownCtx)
	}
	return stopErr
}

// Status describes the running bridge.
func (b *bridge) Status() Status {
	st := Status{
		Version:  version.Version,
		PID:      os.Getpid(),
		Started:  b.started,
		Config:   b.cfgPath,
		Serving:  listenParts(b.cfg),
		AuditLog: b.audit.Path(),
		Sessions: b.srv.Sessions(),
	}
	b.mu.Lock()
	st.Tailnet = b.tailnetURL
	b.mu.Unlock()
	for _, d := range b.dialers {
		ds := d.State()
		st.Relays = append(st.Relays, RelayStatus{URL: d.URL, Connected: ds.Connected, Since: ds.Since, LastError: ds.LastError})
	}
	return st
}
