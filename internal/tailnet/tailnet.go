// Package tailnet runs studiolink-host as its own device on a Tailscale
// tailnet using tsnet. Tailscale does not need to be installed, and because
// tsnet runs in userspace, Windows Firewall rules are not involved.
package tailnet

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"tailscale.com/tsnet"

	"studiolink/internal/config"
)

// Node is a running tailnet device.
type Node struct {
	srv *tsnet.Server
	log *slog.Logger
	cfg *config.Tailscale
}

// New prepares the device. stateDir holds its identity; keep it private.
// Authenticate with TS_AUTHKEY in the environment, or by opening the login
// URL that Up logs.
func New(cfg *config.Tailscale, stateDir string, log *slog.Logger) (*Node, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("tailscale state dir: %w", err)
	}
	var lastUserMsg string
	srv := &tsnet.Server{
		Hostname:  cfg.DeviceName(),
		Dir:       stateDir,
		Ephemeral: cfg.Ephemeral,
		// tsnet's chatter goes to debug; UserLogf carries the login URL,
		// which tsnet repeats every few seconds, so log each message once.
		Logf: func(format string, args ...any) { log.Debug(fmt.Sprintf(format, args...)) },
		UserLogf: func(format string, args ...any) {
			msg := strings.TrimSpace(fmt.Sprintf(format, args...))
			if msg != lastUserMsg {
				lastUserMsg = msg
				log.Info(msg)
			}
		},
	}
	return &Node{srv: srv, log: log, cfg: cfg}, nil
}

// Up waits until the device is on the tailnet (logging the login URL if it
// needs one), then returns a listener for the configured port and the URL
// clients should use.
func (n *Node) Up(ctx context.Context) (net.Listener, string, error) {
	n.log.Info("joining tailnet", "device", n.cfg.DeviceName())
	st, err := n.srv.Up(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("join tailnet: %w", err)
	}
	host := strings.TrimSuffix(st.Self.DNSName, ".")
	port := n.cfg.ListenPort()
	addr := ":" + strconv.Itoa(port)

	if n.cfg.HTTPS {
		if len(st.CertDomains) == 0 {
			return nil, "", fmt.Errorf("tailscale.https is set but the tailnet has no HTTPS certificates; enable HTTPS in the Tailscale admin console (DNS page)")
		}
		lc, err := n.srv.LocalClient()
		if err != nil {
			return nil, "", err
		}
		ln, err := n.srv.Listen("tcp", addr)
		if err != nil {
			return nil, "", err
		}
		tlsLn := tls.NewListener(ln, &tls.Config{GetCertificate: lc.GetCertificate})
		return tlsLn, fmt.Sprintf("https://%s%s/mcp", host, portSuffix(port, 443)), nil
	}
	ln, err := n.srv.Listen("tcp", addr)
	if err != nil {
		return nil, "", err
	}
	return ln, fmt.Sprintf("http://%s%s/mcp", host, portSuffix(port, 80)), nil
}

// IdentifyPeers wraps h so each request's context carries the tailnet user
// and device that sent it, added with withPeer.
func (n *Node) IdentifyPeers(h http.Handler, withPeer func(context.Context, string) context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lc, err := n.srv.LocalClient(); err == nil {
			if who, err := lc.WhoIs(r.Context(), r.RemoteAddr); err == nil && who.Node != nil {
				peer := strings.TrimSuffix(who.Node.Name, ".")
				if who.UserProfile != nil && who.UserProfile.LoginName != "" {
					peer = who.UserProfile.LoginName + " on " + peer
				}
				r = r.WithContext(withPeer(r.Context(), peer))
			}
		}
		h.ServeHTTP(w, r)
	})
}

// Close takes the device off the tailnet (removing it if ephemeral).
func (n *Node) Close() error { return n.srv.Close() }

func portSuffix(port, def int) string {
	if port == def {
		return ""
	}
	return ":" + strconv.Itoa(port)
}
