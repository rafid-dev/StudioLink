// Package hostserver serves Roblox Studio's stdio MCP server over MCP
// Streamable HTTP. Each MCP session gets its own StudioMCP process, and
// messages pass through unchanged apart from routing.
package hostserver

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Process is a running StudioMCP (or a stand-in in tests).
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Kill() error
	Done() <-chan struct{}
}

// Spawner starts a new StudioMCP process for a new session.
type Spawner func() (Process, error)

// Options configures a Server.
type Options struct {
	Token          string
	AllowedOrigins []string
	MaxSessions    int
	IdleTimeout    time.Duration
	Spawn          Spawner
	Logger         *slog.Logger
	// KeepAlive is how long a request may wait silently before the reply
	// switches to an SSE stream with keepalive comments, so proxies and
	// tunnels do not drop slow tool calls. Defaults to 15s.
	KeepAlive time.Duration
}

const (
	sessionHeader = "Mcp-Session-Id"
	maxBodyBytes  = 32 << 20
)

// Server is the HTTP side of studiolink-host.
type Server struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool

	stopReaper chan struct{}
}

// New creates a Server and starts its idle-session reaper.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.KeepAlive == 0 {
		opts.KeepAlive = 15 * time.Second
	}
	s := &Server{
		opts:       opts,
		log:        opts.Logger,
		sessions:   make(map[string]*session),
		stopReaper: make(chan struct{}),
	}
	if opts.IdleTimeout > 0 {
		go s.reapIdle()
	}
	return s
}

// Handler returns the HTTP handler: /mcp (authenticated) and /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.Handle("/mcp", s.guard(http.HandlerFunc(s.serveMCP)))
	return mux
}

// Close ends every session and stops the reaper.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	sessions := s.sessions
	s.sessions = map[string]*session{}
	s.mu.Unlock()
	close(s.stopReaper)
	for _, sess := range sessions {
		sess.close("server shutting down")
	}
}

// guard enforces the Origin allowlist and the bearer token.
func (s *Server) guard(next http.Handler) http.Handler {
	want := []byte(s.opts.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !slices.Contains(s.opts.AllowedOrigins, origin) {
			s.log.Warn("refused request from disallowed origin", "origin", origin, "remote", r.RemoteAddr)
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		got := r.Header.Get("X-MCP-Auth")
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got = bearer
		}
		// No WWW-Authenticate header on purpose: it would send MCP clients
		// into an OAuth discovery flow the bridge does not offer.
		if len(want) == 0 || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			s.log.Warn("refused request with missing or wrong token", "remote", r.RemoteAddr)
			http.Error(w, "missing or invalid token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handlePost(w, r)
	case http.MethodGet:
		s.handleGet(w, r)
	case http.MethodDelete:
		s.handleDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse(nil, codeInvalidRequest, "request body too large or unreadable"))
		return
	}
	// StudioMCP reads one message per line, so strip any newlines.
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(nil, codeParseError, "invalid JSON"))
		return
	}
	msg := compact.Bytes()
	if len(msg) > 0 && msg[0] == '[' {
		writeJSON(w, http.StatusBadRequest, errorResponse(nil, codeInvalidRequest, "JSON-RPC batches are not supported"))
		return
	}
	env, err := parseEnvelope(msg)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse(nil, codeInvalidRequest, "not a JSON-RPC message"))
		return
	}

	var sess *session
	isNew := false
	if id := r.Header.Get(sessionHeader); id != "" {
		if sess = s.lookup(id); sess == nil {
			http.Error(w, "unknown or expired session", http.StatusNotFound)
			return
		}
	} else {
		if env.Method != "initialize" {
			writeJSON(w, http.StatusBadRequest, errorResponse(env.ID, codeInvalidRequest, "missing "+sessionHeader+" header (send initialize first)"))
			return
		}
		if sess, err = s.newSession(r); err != nil {
			s.log.Error("could not start a session", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, errorResponse(env.ID, codeInternalError, err.Error()))
			return
		}
		isNew = true
		w.Header().Set(sessionHeader, sess.id)
	}

	if !env.isRequest() {
		// Notifications and responses to server requests need no reply.
		sess.touch()
		if err := sess.send(msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if tool := env.toolName(); tool != "" {
		sess.log.Info("tool call", "tool", tool)
	} else {
		sess.log.Debug("request", "method", env.Method)
	}
	id := string(env.ID)
	wt := sess.expect(id)
	if err := sess.send(msg); err != nil {
		sess.forget(id, wt)
		writeJSON(w, http.StatusBadGateway, errorResponse(env.ID, codeInternalError, "studio MCP is not running"))
		return
	}
	answered := s.awaitResponse(w, r, sess, env.ID, wt)
	if !answered {
		sess.forget(id, wt)
		if isNew {
			// The client never learned this session's ID, so nobody can use it.
			s.drop(sess, "initialize did not complete")
		}
	}
}

// awaitResponse waits for the response to one request. It replies with plain
// JSON when the response comes first, and switches to SSE when server
// messages arrive first or the wait passes the keepalive interval. It
// reports whether a response reached the client.
func (s *Server) awaitResponse(w http.ResponseWriter, r *http.Request, sess *session, reqID json.RawMessage, wt *waiter) bool {
	canStream := acceptsSSE(r)
	var sse *sseWriter
	ticker := time.NewTicker(s.opts.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-wt.resp:
			if !ok {
				failure := errorResponse(reqID, codeInternalError, "studio MCP process exited")
				if sse != nil {
					sse.event(failure)
				} else {
					writeJSON(w, http.StatusBadGateway, failure)
				}
				return false
			}
			if sse != nil {
				return sse.event(msg) == nil
			}
			writeJSON(w, http.StatusOK, msg)
			return true
		case msg := <-wt.events:
			if !canStream {
				sess.log.Debug("dropping server message: client cannot take SSE")
				continue
			}
			if sse == nil {
				sse = startSSE(w)
			}
			if sse.event(msg) != nil {
				return false
			}
		case <-ticker.C:
			if !canStream {
				continue
			}
			if sse == nil {
				sse = startSSE(w)
			}
			if sse.comment("keepalive") != nil {
				return false
			}
		case <-r.Context().Done():
			return false
		}
	}
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	if !acceptsSSE(r) {
		http.Error(w, "GET needs Accept: text/event-stream", http.StatusMethodNotAllowed)
		return
	}
	sess := s.lookup(r.Header.Get(sessionHeader))
	if sess == nil {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	g := sess.openGet()
	defer sess.closeGet(g)
	sse := startSSE(w)
	ticker := time.NewTicker(s.opts.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case msg := <-g.events:
			if sse.event(msg) != nil {
				return
			}
		case <-ticker.C:
			if sse.comment("keepalive") != nil {
				return
			}
		case <-g.gone:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sess := s.lookup(r.Header.Get(sessionHeader))
	if sess == nil {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}
	s.drop(sess, "client ended the session")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) newSession(r *http.Request) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("server is shutting down")
	}
	if s.opts.MaxSessions > 0 && len(s.sessions) >= s.opts.MaxSessions {
		return nil, errors.New("too many open sessions; close one or raise max_sessions")
	}
	p, err := s.opts.Spawn()
	if err != nil {
		return nil, err
	}
	id := newSessionID()
	sess := newSession(id, p, s.log.With("session", id[:8]))
	s.sessions[id] = sess
	sess.log.Info("session opened", "remote", r.RemoteAddr, "sessions", len(s.sessions))
	go func() {
		<-sess.closed
		s.mu.Lock()
		if s.sessions[id] == sess {
			delete(s.sessions, id)
		}
		s.mu.Unlock()
	}()
	return sess, nil
}

func (s *Server) lookup(id string) *session {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) drop(sess *session, reason string) {
	s.mu.Lock()
	if s.sessions[sess.id] == sess {
		delete(s.sessions, sess.id)
	}
	s.mu.Unlock()
	sess.close(reason)
}

// SessionCount returns the number of open sessions.
func (s *Server) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Server) reapIdle() {
	interval := min(s.opts.IdleTimeout/4, time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopReaper:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		var idle []*session
		for _, sess := range s.sessions {
			if last, busy := sess.idleSince(); !busy && time.Since(last) > s.opts.IdleTimeout {
				idle = append(idle, sess)
			}
		}
		s.mu.Unlock()
		for _, sess := range idle {
			s.drop(sess, "idle timeout")
		}
	}
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func acceptsSSE(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func writeJSON(w http.ResponseWriter, status int, msg []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(msg)
}

// sseWriter writes a text/event-stream response.
type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func startSSE(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	sse := &sseWriter{w: w, rc: http.NewResponseController(w)}
	_ = sse.rc.Flush()
	return sse
}

func (s *sseWriter) event(msg []byte) error {
	if _, err := io.WriteString(s.w, "event: message\ndata: "); err != nil {
		return err
	}
	if _, err := s.w.Write(msg); err != nil {
		return err
	}
	if _, err := io.WriteString(s.w, "\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}

func (s *sseWriter) comment(text string) error {
	if _, err := io.WriteString(s.w, ": "+text+"\n\n"); err != nil {
		return err
	}
	return s.rc.Flush()
}
