package tunnel

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

var errNoHost = errors.New("studiolink-host is not connected to this relay")

// Relay is the server end of dial-out mode. It accepts studiolink-host's
// tunnel and forwards local agents' HTTP requests through it.
type Relay struct {
	Token string
	Log   *slog.Logger

	mu     sync.Mutex
	sess   *yamux.Session
	remote string
	since  time.Time
}

// TunnelHandler accepts the host's tunnel at Path. Only one host is served
// at a time; a new tunnel replaces the old one (usually a reconnect after a
// network drop the relay has not noticed yet).
func (r *Relay) TunnelHandler() http.Handler {
	want := []byte(r.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != Path {
			http.NotFound(w, req)
			return
		}
		if len(want) == 0 || subtle.ConstantTimeCompare([]byte(bearer(req)), want) != 1 {
			r.Log.Warn("refused tunnel with missing or wrong token", "remote", req.RemoteAddr)
			http.Error(w, "missing or invalid token", http.StatusUnauthorized)
			return
		}
		if got := req.Header.Get(protocolHeader); got != protocolVersion() {
			msg := fmt.Sprintf("studiolink-host speaks protocol %q but this relay speaks %s; run matching versions", got, protocolVersion())
			r.Log.Warn("refused tunnel", "remote", req.RemoteAddr, "reason", msg)
			http.Error(w, msg, http.StatusConflict)
			return
		}
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			r.Log.Warn("tunnel handshake failed", "remote", req.RemoteAddr, "err", err)
			return
		}
		sess, err := yamux.Client(netConn(context.Background(), c), yamuxConfig())
		if err != nil {
			c.CloseNow()
			return
		}
		r.attach(sess, req.RemoteAddr)
		<-sess.CloseChan()
		r.detach(sess)
	})
}

func (r *Relay) attach(sess *yamux.Session, remote string) {
	r.mu.Lock()
	old := r.sess
	r.sess, r.remote, r.since = sess, remote, time.Now()
	r.mu.Unlock()
	if old != nil {
		r.Log.Info("studiolink-host reconnected; dropping its previous tunnel", "remote", remote)
		old.Close()
	} else {
		r.Log.Info("studiolink-host connected", "remote", remote)
	}
}

func (r *Relay) detach(sess *yamux.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sess == sess {
		r.sess = nil
		r.Log.Info("studiolink-host disconnected", "remote", r.remote, "was_up", time.Since(r.since).Round(time.Second))
	}
}

// Connected reports whether a host tunnel is up, and from where.
func (r *Relay) Connected() (remote string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remote, r.sess != nil
}

func (r *Relay) open() (net.Conn, error) {
	r.mu.Lock()
	sess := r.sess
	r.mu.Unlock()
	if sess == nil {
		return nil, errNoHost
	}
	return sess.Open()
}

// AgentHandler serves local agents: /mcp (and anything else) is forwarded to
// the host, which checks the token as usual; /healthz reports whether the
// host is connected.
func (r *Relay) AgentHandler() http.Handler {
	host := &url.URL{Scheme: "http", Host: "studiolink-host"}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(host)
			pr.SetXForwarded()
		},
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) { return r.open() },
			// A stream per request: streams are cheap, and none can go
			// stale across a reconnect.
			DisableKeepAlives: true,
		},
		FlushInterval: -1, // SSE replies must not be buffered
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return // the agent hung up
			}
			r.Log.Warn("could not forward to studiolink-host", "path", req.URL.Path, "err", err)
			http.Error(w, "studiolink-host is not reachable through this relay: "+err.Error(), http.StatusBadGateway)
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if remote, ok := r.Connected(); ok {
			_, _ = io.WriteString(w, "ok: studiolink-host connected from "+remote+"\n")
			return
		}
		http.Error(w, "waiting for studiolink-host to connect", http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if _, ok := r.Connected(); !ok {
			http.Error(w, errNoHost.Error()+"; is it running with this relay in its config?", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, req)
	})
	return mux
}

// Close drops the host's tunnel.
func (r *Relay) Close() {
	r.mu.Lock()
	sess := r.sess
	r.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
}
