package shim

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"studiolink/internal/hostserver"
	"studiolink/internal/mcptest"
)

const testToken = "test-token-0123456789abcdef0123456789"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// bridge is a real hostserver whose sessions talk to fake StudioMCPs.
type bridge struct {
	srv   *hostserver.Server
	http  *httptest.Server
	mu    sync.Mutex
	procs []*mcptest.FakeProc
}

func newBridge(t *testing.T) *bridge {
	b := &bridge{}
	b.srv = hostserver.New(hostserver.Options{
		Token:     testToken,
		KeepAlive: 50 * time.Millisecond,
		Logger:    quiet,
		Spawn: func() (hostserver.Process, error) {
			p := mcptest.NewFakeProc(300 * time.Millisecond)
			b.mu.Lock()
			b.procs = append(b.procs, p)
			b.mu.Unlock()
			return p, nil
		},
	})
	b.http = httptest.NewServer(b.srv.Handler())
	t.Cleanup(func() {
		b.http.Close()
		b.srv.Close()
	})
	return b
}

func (b *bridge) url() string { return b.http.URL + "/mcp" }

// agent drives a shim through its stdio, like an MCP client would.
type agent struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan string
	done  chan error
}

func startAgent(t *testing.T, opts Options) *agent {
	t.Helper()
	if opts.Log == nil {
		opts.Log = quiet
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	a := &agent{t: t, in: inW, lines: make(chan string, 100), done: make(chan error, 1)}
	go func() {
		a.done <- Run(context.Background(), opts, inR, outW)
		outW.Close()
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			a.lines <- sc.Text()
		}
		close(a.lines)
	}()
	t.Cleanup(func() { inW.Close() })
	return a
}

func (a *agent) send(msg string) {
	a.t.Helper()
	if _, err := io.WriteString(a.in, msg+"\n"); err != nil {
		a.t.Fatal(err)
	}
}

// expect waits for the next line from the shim and checks it contains want.
func (a *agent) expect(want string) string {
	a.t.Helper()
	select {
	case line, ok := <-a.lines:
		if !ok {
			a.t.Fatalf("shim output ended; wanted %q", want)
		}
		if !strings.Contains(line, want) {
			a.t.Fatalf("got %s\nwant it to contain %q", line, want)
		}
		return line
	case <-time.After(3 * time.Second):
		a.t.Fatalf("no output; wanted %q", want)
	}
	return ""
}

// stop closes the agent's stdin, as a client shutting down does.
func (a *agent) stop() {
	a.t.Helper()
	a.in.Close()
	select {
	case err := <-a.done:
		if err != nil {
			a.t.Errorf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		a.t.Fatal("Run did not return after stdin closed")
	}
}

const initMsg = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`

func call(id, tool string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"` + tool + `"}}`
}

func TestRoundTripThroughBridge(t *testing.T) {
	b := newBridge(t)
	a := startAgent(t, Options{URL: b.url(), Token: testToken})

	a.send(initMsg)
	a.expect(`"protocolVersion":"2025-06-18"`)
	a.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// A server notification sent mid-call reaches the agent before the result.
	a.send(call("1", "notify"))
	a.expect(`"method":"notifications/message"`)
	a.expect(`"result":"notified"`)

	// Calls run concurrently: a quick call overtakes a slow one, and the slow
	// one arrives over SSE once it outlasts the keepalive.
	a.send(call("2", "slow"))
	a.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	a.expect(`"id":3`)
	a.expect(`"result":"slow done"`)

	// Batches are split, since the bridge takes one message per request.
	a.send(`[{"jsonrpc":"2.0","id":4,"method":"ping"},{"jsonrpc":"2.0","id":5,"method":"ping"}]`)
	got := a.expect(`"id":`) + a.expect(`"id":`)
	if !strings.Contains(got, `"id":4`) || !strings.Contains(got, `"id":5`) {
		t.Errorf("batch replies = %s", got)
	}

	a.stop()
	// Closing stdin ends the session on the bridge and kills its StudioMCP.
	if n := b.srv.SessionCount(); n != 0 {
		t.Errorf("sessions after stop = %d, want 0", n)
	}
	select {
	case <-b.procs[0].Done():
	case <-time.After(2 * time.Second):
		t.Error("StudioMCP was not stopped")
	}
}

func TestWrongToken(t *testing.T) {
	b := newBridge(t)
	a := startAgent(t, Options{URL: b.url(), Token: "wrong"})
	a.send(initMsg)
	a.expect(`rejected the token`)
	a.stop()
}

func TestBridgeUnreachable(t *testing.T) {
	a := startAgent(t, Options{URL: "http://127.0.0.1:1/mcp", Token: testToken})
	a.send(initMsg)
	line := a.expect(`cannot reach studiolink-host`)
	if !strings.Contains(line, `"id":0`) {
		t.Errorf("error reply lost the request id: %s", line)
	}
	a.stop()
}

func TestSessionGone(t *testing.T) {
	b := newBridge(t)
	a := startAgent(t, Options{URL: b.url(), Token: testToken})
	a.send(initMsg)
	a.expect(`"result"`)
	b.srv.Close() // the bridge restarting looks the same to the shim
	a.send(call("1", "echo"))
	a.expect(`no longer has this session`)
	a.stop()
}

func TestProbe(t *testing.T) {
	b := newBridge(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := Probe(ctx, Options{URL: b.url(), Token: testToken, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if res.Server != "fake" || res.Protocol != "2025-06-18" {
		t.Errorf("probe = %+v", res)
	}
	if n := b.srv.SessionCount(); n != 0 {
		t.Errorf("probe left %d sessions open", n)
	}
	if _, err := Probe(ctx, Options{URL: b.url(), Token: "wrong", Log: quiet}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("probe with wrong token: %v", err)
	}
}

func TestReadSSE(t *testing.T) {
	stream := ": keepalive\r\n\r\n" +
		"event: message\r\ndata: {\"a\":1}\r\n\r\n" +
		"data: {\"b\":\ndata: 2}\n\n" +
		"data: {\"c\":3}" // no closing blank line
	var got []string
	if err := readSSE(strings.NewReader(stream), func(b []byte) { got = append(got, string(b)) }); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"a":1}`, "{\"b\":\n2}", `{"c":3}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("events = %q, want %q", got, want)
	}
}
