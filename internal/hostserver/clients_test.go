package hostserver

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"studiolink/internal/audit"
	"studiolink/internal/mcptest"
	"studiolink/internal/policy"
)

const readerToken = "reader-token-0123456789abcdef01234567"

func asReader() reqOpt { return withHeader("Authorization", "Bearer "+readerToken) }

// newPolicyHarness has the owner token (no policy) and a "reader" client
// limited to list_roblox_studios and notify.
func newPolicyHarness(t *testing.T, mod func(*Options)) *harness {
	return newHarness(t, func(o *Options) {
		o.Clients = []Client{{Name: "reader", Token: readerToken, Tools: &policy.Tools{Allow: []string{"list_roblox_studios", "notify"}}}}
		if mod != nil {
			mod(o)
		}
	})
}

func (h *harness) initializeAs(opts ...reqOpt) string {
	h.t.Helper()
	resp, body := h.do("POST", initBody, opts...)
	if resp.StatusCode != 200 {
		h.t.Fatalf("initialize: %d %s", resp.StatusCode, body)
	}
	return resp.Header.Get(sessionHeader)
}

func toolNames(t *testing.T, body string) []string {
	t.Helper()
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("tools/list reply %q: %v", body, err)
	}
	var names []string
	for _, tool := range resp.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestToolPolicy(t *testing.T) {
	h := newPolicyHarness(t, nil)
	const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	sid := h.initializeAs(asReader())
	_, body := h.do("POST", list, withSession(sid), asReader())
	if got := strings.Join(toolNames(t, body), ","); got != "notify,list_roblox_studios" {
		t.Errorf("reader sees tools %s", got)
	}
	resp, body := h.do("POST", call(2, "execute_luau"), withSession(sid), asReader())
	if resp.StatusCode != 200 || !strings.Contains(body, `"code":-32602`) || !strings.Contains(body, "disabled") {
		t.Errorf("denied call: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "echo") {
		t.Error("denied call reached StudioMCP")
	}
	if _, body := h.do("POST", call(3, "list_roblox_studios"), withSession(sid), asReader()); !strings.Contains(body, "echo") {
		t.Errorf("allowed call: %s", body)
	}

	// The owner token has no policy.
	owner := h.initialize()
	if _, body := h.do("POST", list, withSession(owner)); len(toolNames(t, body)) != len(mcptest.FakeTools) {
		t.Errorf("owner sees %s", body)
	}
}

func TestSessionsBelongToTheirClient(t *testing.T) {
	h := newPolicyHarness(t, nil)
	owner := h.initialize()
	// The reader must not get around its policy with the owner's session.
	if resp, _ := h.do("POST", call(1, "execute_luau"), withSession(owner), asReader()); resp.StatusCode != http.StatusNotFound {
		t.Errorf("reader using owner's session: %s", resp.Status)
	}
	if resp, _ := h.do("DELETE", "", withSession(owner), asReader()); resp.StatusCode != http.StatusNotFound {
		t.Errorf("reader deleting owner's session: %s", resp.Status)
	}
	if n := h.srv.SessionCount(); n != 1 {
		t.Errorf("sessions = %d", n)
	}
}

func TestSessionsInfo(t *testing.T) {
	h := newPolicyHarness(t, nil)
	sid := h.initializeAs(asReader())
	h.do("POST", call(1, "list_roblox_studios"), withSession(sid), asReader())
	h.do("POST", call(2, "execute_luau"), withSession(sid), asReader())
	infos := h.srv.Sessions()
	if len(infos) != 1 || infos[0].Client != "reader" || infos[0].ToolCalls != 2 || infos[0].ID != sid[:8] || infos[0].Remote == "" {
		t.Errorf("Sessions() = %+v", infos)
	}
}

func TestOwnerOnly(t *testing.T) {
	h := newPolicyHarness(t, nil)
	srv := httpServer(t, h.srv.OwnerOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})))
	for _, tt := range []struct {
		token string
		want  int
	}{{testToken, 200}, {readerToken, 403}, {"nope", 401}} {
		req, _ := http.NewRequest("GET", srv, nil)
		req.Header.Set("Authorization", "Bearer "+tt.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("token %.6s: %s, want %d", tt.token, resp.Status, tt.want)
		}
	}
}

func TestAuditLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log, err := audit.Open(path, 0, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	h := newPolicyHarness(t, func(o *Options) { o.Audit, o.AuditArguments = log, true })

	sid := h.initializeAs(asReader())
	h.do("POST", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_roblox_studios","arguments":{"x":1}}}`, withSession(sid), asReader())
	h.do("POST", call(2, "execute_luau"), withSession(sid), asReader())
	h.do("DELETE", "", withSession(sid), asReader())
	h.do("POST", initBody, withHeader("Authorization", "Bearer wrong"))

	var events []audit.Event
	waitFor(t, func() bool {
		events = readAudit(t, path)
		return len(events) >= 5
	})
	want := []string{"session_open reader", "tool_call reader list_roblox_studios ok", "tool_call reader execute_luau denied", "session_close reader", "request_refused  "}
	for i, w := range want {
		e := events[i]
		got := strings.TrimSpace(strings.Join([]string{e.Event, e.Client, e.Tool, e.Result}, " "))
		if got != strings.TrimSpace(w) {
			t.Errorf("event %d = %q, want %q (%+v)", i, got, w, e)
		}
	}
	if string(events[1].Arguments) != `{"x":1}` {
		t.Errorf("arguments = %s", events[1].Arguments)
	}
	if events[3].ToolCalls != 2 || events[3].Reason != "client ended the session" {
		t.Errorf("session_close = %+v", events[3])
	}
}

func readAudit(t *testing.T, path string) []audit.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var events []audit.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e audit.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			events = append(events, e)
		}
	}
	return events
}

func TestFilterToolListPassesThroughOddReplies(t *testing.T) {
	p := &policy.Tools{Deny: []string{"x"}}
	for _, msg := range []string{`{"error":{"code":1}}`, `{"result":"text"}`, `not json`} {
		if got := string(filterToolList([]byte(msg), p)); got != msg {
			t.Errorf("filterToolList(%s) = %s", msg, got)
		}
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func httpServer(t *testing.T, h http.Handler) string {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}
