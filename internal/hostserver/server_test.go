package hostserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "test-token-0123456789abcdef0123456789"

// fakeProc is an in-memory stand-in for StudioMCP. It answers initialize,
// echoes other requests, and has tools that exercise the bridge:
// "notify" sends a notification before its result, "slow" answers late,
// "crash" exits without answering.
type fakeProc struct {
	inR, outR, errR *io.PipeReader
	inW, outW, errW *io.PipeWriter
	done            chan struct{}
	once            sync.Once
	slow            time.Duration
}

func newFakeProc(slow time.Duration) *fakeProc {
	p := &fakeProc{done: make(chan struct{}), slow: slow}
	p.inR, p.inW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	p.errR, p.errW = io.Pipe()
	go p.serve()
	return p
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.inW }
func (p *fakeProc) Stdout() io.Reader     { return p.outR }
func (p *fakeProc) Stderr() io.Reader     { return p.errR }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Kill() error {
	p.once.Do(func() {
		close(p.done)
		p.inR.Close()
		p.outW.Close()
		p.errW.Close()
	})
	return nil
}

func (p *fakeProc) write(v any) {
	b, _ := json.Marshal(v)
	_, _ = p.outW.Write(append(b, '\n'))
}

func (p *fakeProc) serve() {
	sc := bufio.NewScanner(p.inR)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			fmt.Fprintln(p.errW, "bad line:", sc.Text())
			continue
		}
		if len(m.ID) == 0 || m.Method == "" {
			continue // notification or response
		}
		reply := func(result any) {
			p.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		}
		switch {
		case m.Method == "initialize":
			reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]string{"name": "fake"}})
		case m.Params.Name == "notify":
			p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]string{"data": "hi"}})
			reply("notified")
		case m.Params.Name == "slow":
			go func() { time.Sleep(p.slow); reply("slow done") }()
		case m.Params.Name == "crash":
			p.Kill()
			return
		default:
			reply(map[string]string{"echo": m.Method})
		}
	}
}

type harness struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	mu     sync.Mutex
	procs  []*fakeProc
	spawns atomic.Int32
}

func newHarness(t *testing.T, mod func(*Options)) *harness {
	h := &harness{t: t}
	opts := Options{
		Token:     testToken,
		KeepAlive: 50 * time.Millisecond,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Spawn: func() (Process, error) {
			h.spawns.Add(1)
			p := newFakeProc(300 * time.Millisecond)
			h.mu.Lock()
			h.procs = append(h.procs, p)
			h.mu.Unlock()
			return p, nil
		},
	}
	if mod != nil {
		mod(&opts)
	}
	h.srv = New(opts)
	h.http = httptest.NewServer(h.srv.Handler())
	t.Cleanup(func() {
		h.http.Close()
		h.srv.Close()
	})
	return h
}

type reqOpt func(*http.Request)

func withSession(id string) reqOpt  { return func(r *http.Request) { r.Header.Set(sessionHeader, id) } }
func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func (h *harness) do(method, body string, opts ...reqOpt) (*http.Response, string) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.http.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for _, o := range opts {
		o(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const initBody = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`

func (h *harness) initialize() string {
	h.t.Helper()
	resp, body := h.do("POST", initBody)
	if resp.StatusCode != 200 {
		h.t.Fatalf("initialize: %d %s", resp.StatusCode, body)
	}
	id := resp.Header.Get(sessionHeader)
	if id == "" {
		h.t.Fatal("initialize response has no session ID")
	}
	return id
}

func call(id int, tool string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q}}`, id, tool)
}

func TestAuth(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.AllowedOrigins = []string{"https://ok.test"} })
	cases := []struct {
		name string
		opts []reqOpt
		want int
	}{
		{"no token", []reqOpt{withHeader("Authorization", "")}, 401},
		{"wrong token", []reqOpt{withHeader("Authorization", "Bearer nope")}, 401},
		{"x-mcp-auth", []reqOpt{withHeader("Authorization", ""), withHeader("X-MCP-Auth", testToken)}, 200},
		{"bad origin", []reqOpt{withHeader("Origin", "https://evil.test")}, 403},
		{"allowed origin", []reqOpt{withHeader("Origin", "https://ok.test")}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := h.do("POST", initBody, c.opts...)
			if resp.StatusCode != c.want {
				t.Errorf("status = %d, want %d (%s)", resp.StatusCode, c.want, body)
			}
		})
	}
	resp, err := http.Get(h.http.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz without token: %v %v", resp, err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	id := h.initialize()

	resp, body := h.do("POST", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, withSession(id))
	if resp.StatusCode != 200 || !strings.Contains(body, `"echo":"tools/list"`) {
		t.Fatalf("tools/list: %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Pretty-printed bodies must reach StudioMCP as a single line.
	resp, body = h.do("POST", "{\n  \"jsonrpc\": \"2.0\",\n  \"id\": 2,\n  \"method\": \"ping\"\n}", withSession(id))
	if resp.StatusCode != 200 || !strings.Contains(body, `"echo":"ping"`) {
		t.Fatalf("multi-line body: %d %s", resp.StatusCode, body)
	}

	resp, _ = h.do("POST", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, withSession(id))
	if resp.StatusCode != 202 {
		t.Errorf("notification status = %d, want 202", resp.StatusCode)
	}

	resp, _ = h.do("DELETE", "", withSession(id))
	if resp.StatusCode != 204 {
		t.Errorf("DELETE status = %d, want 204", resp.StatusCode)
	}
	waitClosed(t, h.procs[0])
	resp, _ = h.do("POST", call(3, "x"), withSession(id))
	if resp.StatusCode != 404 {
		t.Errorf("request after DELETE = %d, want 404", resp.StatusCode)
	}
}

func TestBadRequests(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		name string
		body string
		opts []reqOpt
		want int
	}{
		{"no session, not initialize", call(1, "x"), nil, 400},
		{"unknown session", call(1, "x"), []reqOpt{withSession("nope")}, 404},
		{"invalid JSON", `{"jsonrpc":`, nil, 400},
		{"batch", `[` + initBody + `]`, nil, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := h.do("POST", c.body, c.opts...)
			if resp.StatusCode != c.want {
				t.Errorf("status = %d, want %d (%s)", resp.StatusCode, c.want, body)
			}
		})
	}
	if n := h.spawns.Load(); n != 0 {
		t.Errorf("bad requests spawned %d processes", n)
	}
}

func TestNotificationSwitchesToSSE(t *testing.T) {
	h := newHarness(t, nil)
	id := h.initialize()
	resp, body := h.do("POST", call(1, "notify"), withSession(id))
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	note := strings.Index(body, "notifications/message")
	result := strings.Index(body, `"result":"notified"`)
	if note < 0 || result < 0 || note > result {
		t.Errorf("want the notification then the result, got:\n%s", body)
	}
}

func TestSlowCallSendsKeepalive(t *testing.T) {
	h := newHarness(t, nil)
	id := h.initialize()
	resp, body := h.do("POST", call(1, "slow"), withSession(id))
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("want SSE for a slow call, got %q", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, ": keepalive") || !strings.Contains(body, "slow done") {
		t.Errorf("want keepalives then the result, got:\n%s", body)
	}

	// A client that cannot take SSE waits for plain JSON instead.
	resp, body = h.do("POST", call(2, "slow"), withSession(id), withHeader("Accept", "application/json"))
	if resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(body, "slow done") {
		t.Errorf("JSON-only client: %q %s", resp.Header.Get("Content-Type"), body)
	}
}

func TestCrashFailsRequestAndEndsSession(t *testing.T) {
	h := newHarness(t, nil)
	id := h.initialize()
	resp, body := h.do("POST", call(1, "crash"), withSession(id), withHeader("Accept", "application/json"))
	if resp.StatusCode != 502 || !strings.Contains(body, "exited") {
		t.Errorf("crash: %d %s", resp.StatusCode, body)
	}
	waitFor(t, func() bool { return h.srv.SessionCount() == 0 })
	resp, _ = h.do("POST", call(2, "x"), withSession(id))
	if resp.StatusCode != 404 {
		t.Errorf("request after crash = %d, want 404", resp.StatusCode)
	}
}

func TestGetStreamReceivesServerMessages(t *testing.T) {
	h := newHarness(t, nil)
	id := h.initialize()

	req, _ := http.NewRequest("GET", h.http.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(sessionHeader, id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	waitFor(t, func() bool { _, busy := h.srv.lookup(id).idleSince(); return busy })

	// With a GET stream open, the notification goes there, not to the POST.
	_, body := h.do("POST", call(1, "notify"), withSession(id))
	if strings.Contains(body, "notifications/message") {
		t.Errorf("notification went to the POST, want the GET stream:\n%s", body)
	}
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("GET stream ended without the notification: %v", err)
		}
		if strings.Contains(line, "notifications/message") {
			break
		}
	}
}

func TestMaxSessions(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxSessions = 1 })
	h.initialize()
	resp, body := h.do("POST", initBody)
	if resp.StatusCode != 503 || !strings.Contains(body, "too many") {
		t.Errorf("second session: %d %s", resp.StatusCode, body)
	}
}

func TestIdleSessionsAreReaped(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.IdleTimeout = 100 * time.Millisecond })
	h.initialize()
	waitFor(t, func() bool { return h.srv.SessionCount() == 0 })
	waitClosed(t, h.procs[0])
}

func TestCloseKillsEverySession(t *testing.T) {
	h := newHarness(t, nil)
	h.initialize()
	h.initialize()
	h.srv.Close()
	for _, p := range h.procs {
		waitClosed(t, p)
	}
	resp, _ := h.do("POST", initBody)
	if resp.StatusCode != 503 {
		t.Errorf("initialize after Close = %d, want 503", resp.StatusCode)
	}
}

func waitClosed(t *testing.T, p *fakeProc) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("process was not killed")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
