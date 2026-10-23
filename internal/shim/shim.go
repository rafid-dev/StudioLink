// Package shim gives a stdio-only MCP client (an agent that can only start MCP
// servers as local commands) a local server that forwards every message to
// studiolink-host over Streamable HTTP.
package shim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionHeader  = "Mcp-Session-Id"
	protocolHeader = "MCP-Protocol-Version"
	maxBodyBytes   = 64 << 20
)

// Options configures a shim.
type Options struct {
	URL   string // the bridge's /mcp endpoint
	Token string
	// Client sends the HTTP requests. Defaults to one with no overall
	// timeout, since tool calls can run for minutes.
	Client *http.Client
	Log    *slog.Logger
}

// NewClient returns an HTTP client suited to the bridge: no overall timeout,
// but a bound on waiting for response headers (the bridge sends them within
// its keepalive interval even for slow tool calls).
func NewClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = time.Minute
	return &http.Client{Transport: tr}
}

type shim struct {
	opts Options
	log  *slog.Logger

	outMu sync.Mutex
	out   io.Writer

	mu        sync.Mutex
	sessionID string
	protocol  string
	getOnce   sync.Once
}

// Run forwards messages read from in to the bridge and writes everything the
// bridge sends back to out, one JSON message per line. It returns when in
// ends or ctx is cancelled, after ending the session on the bridge.
func Run(ctx context.Context, opts Options, in io.Reader, out io.Writer) error {
	if opts.Client == nil {
		opts.Client = NewClient()
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	s := &shim{opts: opts, log: opts.Log, out: out}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(in, 64<<10)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	var inflight sync.WaitGroup
	var err error
loop:
	for {
		select {
		case line := <-lines:
			s.handleLine(ctx, line, &inflight)
		case err = <-readErr:
			if errors.Is(err, io.EOF) {
				err = nil
			}
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	// The agent has gone: abandon outstanding calls and end the session.
	cancel()
	inflight.Wait()
	s.endSession()
	return err
}

// handleLine forwards one line from the agent, splitting batches, which the
// bridge does not accept.
func (s *shim) handleLine(ctx context.Context, line []byte, inflight *sync.WaitGroup) {
	line = bytes.TrimSpace(line)
	if line[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(line, &batch); err != nil {
			s.writeMessage(errorResponse(nil, codeParseError, "invalid JSON batch"))
			return
		}
		for _, msg := range batch {
			s.handleMessage(ctx, msg, inflight)
		}
		return
	}
	s.handleMessage(ctx, line, inflight)
}

func (s *shim) handleMessage(ctx context.Context, msg []byte, inflight *sync.WaitGroup) {
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		s.writeMessage(errorResponse(nil, codeParseError, "invalid JSON"))
		return
	}
	// initialize must finish first so later messages carry the session ID,
	// and notifications are quick (202), so sending them in line keeps them
	// in order. Other requests run concurrently, as over stdio.
	if env.Method == "initialize" || !env.isRequest() {
		s.exchange(ctx, msg, env)
		return
	}
	inflight.Go(func() { s.exchange(ctx, msg, env) })
}

// exchange POSTs one message and relays whatever comes back.
func (s *shim) exchange(ctx context.Context, msg []byte, env envelope) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.opts.URL, bytes.NewReader(msg))
	if err != nil {
		s.fail(env, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	s.setHeaders(req)
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			s.fail(env, "cannot reach studiolink-host: "+err.Error())
		}
		return
	}
	defer resp.Body.Close()

	relay := s.writeMessage
	if env.Method == "initialize" && resp.StatusCode == http.StatusOK {
		s.mu.Lock()
		s.sessionID = resp.Header.Get(sessionHeader)
		s.mu.Unlock()
		relay = func(m []byte) {
			s.noteProtocol(m)
			s.writeMessage(m)
		}
		defer s.getOnce.Do(func() { go s.listen(ctx) })
	}

	switch {
	case resp.StatusCode == http.StatusAccepted:
	case resp.StatusCode == http.StatusOK:
		if isSSE(resp) {
			if err := readSSE(resp.Body, relay); err != nil && ctx.Err() == nil {
				s.fail(env, "lost the reply stream from studiolink-host: "+err.Error())
			}
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			s.fail(env, "reading reply from studiolink-host: "+err.Error())
			return
		}
		relay(body)
	case resp.StatusCode == http.StatusUnauthorized:
		s.fail(env, "studiolink-host rejected the token (check STUDIOLINK_TOKEN or -token-file)")
	case resp.StatusCode == http.StatusNotFound && req.Header.Get(sessionHeader) != "":
		s.fail(env, "studiolink-host no longer has this session (it restarted or the session idled out); restart this MCP server")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		// The bridge reports most failures as JSON-RPC errors; pass those on.
		if resp.Header.Get("Content-Type") == "application/json" && json.Valid(body) {
			s.writeMessage(body)
			return
		}
		s.fail(env, fmt.Sprintf("studiolink-host answered %s: %s", resp.Status, bytes.TrimSpace(body)))
	}
}

// listen holds open the GET stream that carries messages the bridge sends
// outside any request, reconnecting until ctx ends.
func (s *shim) listen(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opts.URL, nil)
		if err != nil {
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		s.setHeaders(req)
		resp, err := s.opts.Client.Do(req)
		if err == nil {
			switch resp.StatusCode {
			case http.StatusOK:
				backoff = time.Second
				err = readSSE(resp.Body, s.writeMessage)
			case http.StatusMethodNotAllowed, http.StatusNotFound:
				// No GET stream on this server, or the session is gone.
				resp.Body.Close()
				s.log.Debug("no server message stream", "status", resp.Status)
				return
			default:
				err = errors.New(resp.Status)
			}
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return
		}
		s.log.Debug("server message stream dropped; reconnecting", "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// endSession tells the bridge to stop this session's StudioMCP process now
// rather than at its idle timeout.
func (s *shim) endSession() {
	s.mu.Lock()
	id := s.sessionID
	s.mu.Unlock()
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.opts.URL, nil)
	if err != nil {
		return
	}
	s.setHeaders(req)
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		s.log.Debug("could not end the session", "err", err)
		return
	}
	resp.Body.Close()
}

func (s *shim) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+s.opts.Token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" {
		req.Header.Set(sessionHeader, s.sessionID)
	}
	if s.protocol != "" {
		req.Header.Set(protocolHeader, s.protocol)
	}
}

// noteProtocol records the protocol version the server chose, which later
// requests must send in a header.
func (s *shim) noteProtocol(msg []byte) {
	var m struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(msg, &m) == nil && m.Result.ProtocolVersion != "" {
		s.mu.Lock()
		s.protocol = m.Result.ProtocolVersion
		s.mu.Unlock()
	}
}

// writeMessage writes one message to the agent on a single line.
func (s *shim) writeMessage(msg []byte) {
	var line bytes.Buffer
	if err := json.Compact(&line, msg); err != nil {
		s.log.Warn("dropping malformed message from studiolink-host", "err", err)
		return
	}
	line.WriteByte('\n')
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if _, err := s.out.Write(line.Bytes()); err != nil {
		s.log.Debug("cannot write to the agent", "err", err)
	}
}

// fail answers a request with an error so the agent does not wait forever.
// Failed notifications have nobody to tell, so they are only logged.
func (s *shim) fail(env envelope, msg string) {
	s.log.Warn(msg, "method", env.Method)
	if env.isRequest() {
		s.writeMessage(errorResponse(env.ID, codeInternalError, msg))
	}
}

func isSSE(resp *http.Response) bool {
	return strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
}
