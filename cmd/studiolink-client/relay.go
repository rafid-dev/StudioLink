package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"studiolink/internal/tunnel"
)

func defaultStateDir(getenv func(string) string) string {
	if dir := getenv("STUDIOLINK_STATE_DIR"); dir != "" {
		return dir
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "StudioLink")
	}
	return "."
}

func runRelay(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fset := flag.NewFlagSet("studiolink-client relay", flag.ContinueOnError)
	fset.SetOutput(stderr)
	listen := fset.String("listen", fmt.Sprintf("127.0.0.1:%d", 44770), "address agents on this server connect to")
	tunnelListen := fset.String("tunnel-listen", fmt.Sprintf(":%d", tunnel.DefaultPort), "address studiolink-host dials (must be reachable from the Studio PC)")
	tokenFile := fset.String("token-file", "", "file holding the token (default: STUDIOLINK_TOKEN)")
	certPath := fset.String("cert", "", "TLS certificate (PEM) for the tunnel; default: a self-signed one, pinned by fingerprint")
	keyPath := fset.String("key", "", "TLS key (PEM) for -cert")
	stateDir := fset.String("state-dir", defaultStateDir(getenv), "where the self-signed certificate is kept (or STUDIOLINK_STATE_DIR)")
	plain := fset.Bool("plain-tunnel", false, "serve the tunnel without TLS, for a TLS-terminating reverse proxy on this machine (loopback -tunnel-listen only)")
	verbose := fset.Bool("v", false, "debug logging")
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	token, err := readToken(*tokenFile, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "relay:", err)
		return 2
	}
	if (*certPath == "") != (*keyPath == "") {
		fmt.Fprintln(stderr, "relay: -cert and -key go together")
		return 2
	}
	if *plain && !isLoopbackAddr(*tunnelListen) {
		fmt.Fprintln(stderr, "relay: -plain-tunnel needs a loopback -tunnel-listen (for example 127.0.0.1:44771); the tunnel carries the token")
		return 2
	}
	log := newLogger(stderr, *verbose)

	var tlsConfig *tls.Config
	fingerprint := ""
	switch {
	case *plain:
	case *certPath != "":
		reloader := &certReloader{certPath: *certPath, keyPath: *keyPath}
		if _, err := reloader.get(nil); err != nil {
			log.Error("cannot load the TLS certificate", "err", err)
			return 1
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: reloader.get}
	default:
		cert, err := tunnel.LoadOrCreateCert(*stateDir)
		if err != nil {
			log.Error("cannot create the relay certificate", "err", err)
			return 1
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
		fingerprint = tunnel.CertFingerprint(cert)
	}

	relay := &tunnel.Relay{Token: token, Log: log}
	agentLn, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("cannot listen for agents", "addr", *listen, "err", err)
		return 1
	}
	tunnelLn, err := net.Listen("tcp", *tunnelListen)
	if err != nil {
		agentLn.Close()
		log.Error("cannot listen for the tunnel", "addr", *tunnelListen, "err", err)
		return 1
	}
	if tlsConfig != nil {
		tunnelLn = tls.NewListener(tunnelLn, tlsConfig)
	}
	// TLS handshake failures (scanners, a host with a stale pin) are noise
	// on a public port; show them only with -v.
	serverLog := slog.NewLogLogger(log.Handler(), slog.LevelDebug)
	agentSrv := &http.Server{Handler: relay.AgentHandler(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: serverLog}
	tunnelSrv := &http.Server{Handler: relay.TunnelHandler(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: serverLog}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 2)
	go func() { errs <- agentSrv.Serve(agentLn) }()
	go func() { errs <- tunnelSrv.Serve(tunnelLn) }()

	scheme := "wss"
	if *plain {
		scheme = "ws"
	}
	log.Info("relay ready",
		"agents", "http://"+agentLn.Addr().String()+"/mcp",
		"tunnel", scheme+"://"+tunnelLn.Addr().String()+tunnel.Path)
	fmt.Fprintf(stdout, "Waiting for studiolink-host. On the Studio PC, add this relay to studiolink.json:\n\n")
	if fingerprint != "" {
		fmt.Fprintf(stdout, "  \"relays\": [{\"url\": \"wss://<this server>:%s\", \"fingerprint\": \"%s\"}]\n\n", portOf(tunnelLn.Addr()), fingerprint)
	} else {
		fmt.Fprintf(stdout, "  \"relays\": [{\"url\": \"wss://<this server's domain>\"}]\n\n")
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errs:
		log.Error("relay stopped", "err", err)
	}
	relay.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = agentSrv.Shutdown(shutdownCtx)
	_ = tunnelSrv.Shutdown(shutdownCtx)
	return 0
}

func runFingerprint(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fset := flag.NewFlagSet("studiolink-client fingerprint", flag.ContinueOnError)
	fset.SetOutput(stderr)
	stateDir := fset.String("state-dir", defaultStateDir(getenv), "where the self-signed certificate is kept (or STUDIOLINK_STATE_DIR)")
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cert, err := tunnel.LoadOrCreateCert(*stateDir)
	if err != nil {
		fmt.Fprintln(stderr, "fingerprint:", err)
		return 1
	}
	fmt.Fprintln(stdout, tunnel.CertFingerprint(cert))
	return 0
}

// certReloader serves a certificate from files, picking up renewals (for
// example by certbot) without a restart.
type certReloader struct {
	certPath, keyPath string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
}

func (c *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	info, err := os.Stat(c.certPath)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert != nil && info.ModTime().Equal(c.modTime) {
		return c.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(c.certPath, c.keyPath)
	if err != nil {
		if c.cert != nil {
			return c.cert, nil // mid-renewal; keep serving the old one
		}
		return nil, err
	}
	c.cert, c.modTime = &cert, info.ModTime()
	return c.cert, nil
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func portOf(a net.Addr) string {
	_, port, _ := net.SplitHostPort(a.String())
	return port
}

// readToken reads the token from a file or STUDIOLINK_TOKEN.
func readToken(tokenFile string, getenv func(string) string) (string, error) {
	token := strings.TrimSpace(getenv("STUDIOLINK_TOKEN"))
	if tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("token file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return "", errors.New("no token: set STUDIOLINK_TOKEN or pass -token-file (studiolink-host token prints it)")
	}
	return token, nil
}
