package hostserver

import (
	"bufio"
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

	"studiolink/internal/mcptest"
)

const testToken = "test-token-0123456789abcdef0123456789"

type harness struct {
	t      *testing.T
	srv    *Server
	http   *httptest.Server
	mu     sync.Mutex
	procs  []*mcptest.FakeProc
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
			p := mcptest.NewFakeProc(300 * time.Millisecond)
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
	if resp.StatusCode != 200 || !strings.Contains(body, `"name":"list_roblox_studios"`) {
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

	// A notification sent during a call stays on that call's stream, so it
	// arrives before the result even with a GET stream open.
	_, body := h.do("POST", call(1, "notify"), withSession(id))
	if !strings.Contains(body, "notifications/message") {
		t.Errorf("notification sent during the call missing from its POST:\n%s", body)
	}
	// One sent when no call is waiting goes to the GET stream.
	h.do("POST", call(2, "announce"), withSession(id))
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("GET stream ended without the notification: %v", err)
		}
		if strings.Contains(line, "notifications/message") {
			t.Fatalf("notification for a call reached the GET stream: %s", line)
		}
		if strings.Contains(line, "list_changed") {
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

func waitClosed(t *testing.T, p *mcptest.FakeProc) {
	t.Helper()
	select {
	case <-p.Done():
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
