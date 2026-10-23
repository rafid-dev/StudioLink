package hostserver

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// maxBacklog caps server-initiated messages held while no stream is open.
const maxBacklog = 100

// errSessionClosed is returned when writing to a session whose StudioMCP
// process has gone.
var errSessionClosed = errors.New("session closed")

// session is one MCP session: a single StudioMCP process plus the routing
// state that maps its stdout back to HTTP requests.
type session struct {
	id   string
	proc Process
	log  *slog.Logger

	// Set once when the session opens.
	client       *client
	remote, peer string
	opened       time.Time

	toolCalls   atomic.Int64
	closeReason string // set before closed is closed

	writeMu sync.Mutex // serialises writes to proc.Stdin

	mu       sync.Mutex
	pending  map[string]*waiter // in-flight requests by JSON-RPC id
	seq      uint64
	get      *getStream // the open GET stream, if any
	backlog  [][]byte   // server-initiated messages with nowhere to go yet
	lastUsed time.Time

	closeOnce sync.Once
	closed    chan struct{}
}

// waiter receives the messages for one in-flight request: server-initiated
// messages sent while it waits, then its response.
type waiter struct {
	seq    uint64
	events chan []byte // server-initiated messages, best effort
	resp   chan []byte // the response, exactly once
	// rewrite, if set, edits the response before the client sees it.
	rewrite func([]byte) []byte
}

type getStream struct {
	events chan []byte
	gone   chan struct{} // closed when replaced or the session closes
}

func newSession(id string, p Process, log *slog.Logger) *session {
	s := &session{
		id:       id,
		proc:     p,
		log:      log,
		pending:  make(map[string]*waiter),
		lastUsed: time.Now(),
		opened:   time.Now(),
		closed:   make(chan struct{}),
	}
	go s.readStdout()
	go s.drainStderr()
	go func() {
		<-p.Done()
		s.close("studio MCP process exited")
	}()
	return s
}

// send writes one message (a single line of compact JSON) to StudioMCP.
func (s *session) send(msg []byte) error {
	select {
	case <-s.closed:
		return errSessionClosed
	default:
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	line := make([]byte, 0, len(msg)+1)
	line = append(append(line, msg...), '\n')
	if _, err := s.proc.Stdin().Write(line); err != nil {
		s.close("write to studio MCP failed: " + err.Error())
		return errSessionClosed
	}
	return nil
}

// expect registers a waiter for the response to request id. Call it before
// sending the request so a fast response cannot be missed.
func (s *session) expect(id string) *waiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	w := &waiter{seq: s.seq, events: make(chan []byte, 256), resp: make(chan []byte, 1)}
	s.pending[id] = w
	s.lastUsed = time.Now()
	return w
}

// forget drops the waiter for id, for example when the client hangs up.
func (s *session) forget(id string, w *waiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending[id] == w {
		delete(s.pending, id)
	}
}

// openGet makes this the session's GET stream, replacing any older one, and
// hands it the backlog.
func (s *session) openGet() *getStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.get != nil {
		close(s.get.gone)
	}
	g := &getStream{events: make(chan []byte, maxBacklog+256), gone: make(chan struct{})}
	for _, msg := range s.backlog {
		g.events <- msg
	}
	s.backlog = nil
	s.get = g
	s.lastUsed = time.Now()
	return g
}

func (s *session) closeGet(g *getStream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.get == g {
		close(g.gone)
		s.get = nil
	}
}

func (s *session) touch() {
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

// idleSince reports when the session was last used, and whether it is busy
// (a request in flight or a GET stream open).
func (s *session) idleSince() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsed, len(s.pending) > 0 || s.get != nil
}

func (s *session) readStdout() {
	r := bufio.NewReaderSize(s.proc.Stdout(), 64*1024)
	for {
		// ReadBytes, not Scanner: tool results (screenshots) can be many MB.
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			s.route(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Debug("studio stdout read failed", "err", err)
			}
			s.close("studio MCP closed its output")
			return
		}
	}
}

func (s *session) drainStderr() {
	sc := bufio.NewScanner(s.proc.Stderr())
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		s.log.Debug("studio stderr", "line", sc.Text())
	}
}

// route delivers one message from StudioMCP: a response goes to the request
// waiting for it; anything else goes to the oldest waiting request (Studio
// sends progress and logs while it works on a call, and that call's stream
// keeps them in order with its result), else the GET stream, else the
// backlog.
func (s *session) route(msg []byte) {
	env, err := parseEnvelope(msg)
	if err != nil {
		s.log.Debug("ignoring non-JSON studio output", "line", string(msg))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if env.isResponse() {
		id := string(env.ID)
		if w, ok := s.pending[id]; ok {
			delete(s.pending, id)
			w.resp <- msg
		} else {
			s.log.Debug("dropping response nobody is waiting for", "id", id)
		}
		return
	}
	var oldest *waiter
	for _, w := range s.pending {
		if oldest == nil || w.seq < oldest.seq {
			oldest = w
		}
	}
	if oldest != nil {
		select {
		case oldest.events <- msg:
			return
		default:
		}
	}
	if s.get != nil {
		select {
		case s.get.events <- msg:
			return
		default:
		}
	}
	if len(s.backlog) >= maxBacklog {
		s.log.Warn("dropping server message: backlog full", "method", env.Method)
		return
	}
	s.backlog = append(s.backlog, msg)
}

// close kills StudioMCP and fails everything waiting on the session.
func (s *session) close(reason string) {
	s.closeOnce.Do(func() {
		s.log.Info("session closed", "reason", reason)
		s.closeReason = reason
		close(s.closed)
		_ = s.proc.Kill()
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, w := range s.pending {
			close(w.resp)
			delete(s.pending, id)
		}
		if s.get != nil {
			close(s.get.gone)
			s.get = nil
		}
	})
}
