package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"studiolink/internal/config"
	"studiolink/internal/netif"
)

// fakeAutostart keeps the Run value in memory, never the real registry.
type fakeAutostart struct{ value string }

func (f *fakeAutostart) Get() (string, error) { return f.value, nil }
func (f *fakeAutostart) Set(c string) error   { f.value = c; return nil }
func (f *fakeAutostart) Remove() error        { f.value = ""; return nil }

// testSettings is a settings server on a temp config, with fakes behind it.
type testSettings struct {
	s        *settingsServer
	url      string
	cfgPath  string
	cfg      *config.Config
	client   *http.Client
	cookie   string // the session cookie, once signed in
	clock    time.Time
	restarts int
	status   *Status
	adapters []netif.Adapter
	opened   []string
	autostrt *fakeAutostart
}

const testAutostartCmd = `"studiolink-host.exe" tray -config "x"`

func newTestSettings(t *testing.T) *testSettings {
	t.Helper()
	path, cfg := newTestConfig(t)
	ts := &testSettings{
		cfgPath:  path,
		cfg:      cfg,
		clock:    time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		autostrt: &fakeAutostart{},
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	ts.s = &settingsServer{
		cfgPath:      path,
		restart:      func() { ts.restarts++ },
		status:       func() *Status { return ts.status },
		adapters:     func() ([]netif.Adapter, error) { return ts.adapters, nil },
		autostart:    ts.autostrt,
		autostartCmd: testAutostartCmd,
		open:         func(target string) { ts.opened = append(ts.opened, target) },
		launcher:     func() (string, error) { return `C:\Roblox\mcp.bat`, nil },
		now:          func() time.Time { return ts.clock },
	}
	url, err := ts.s.start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ts.s.ln.Close() })
	ts.url = url
	return ts
}

// do sends a request. The session cookie is attached once signed in.
func (ts *testSettings) do(t *testing.T, method, path, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if ts.cookie != "" {
		req.AddCookie(&http.Cookie{Name: settingsCookie, Value: ts.cookie})
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, string(data)
}

// post sends a JSON POST with the headers the page sends.
func (ts *testSettings) post(t *testing.T, path string, v any) (*http.Response, string) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return ts.do(t, http.MethodPost, path, string(data), map[string]string{
		"X-StudioLink": "1",
		"Content-Type": "application/json",
	})
}

// loginKey asks for a login link with the main token and returns its key.
func (ts *testSettings) loginKey(t *testing.T) string {
	t.Helper()
	resp, body := ts.do(t, http.MethodPost, "/api/key", "{}", map[string]string{
		"Authorization": "Bearer " + ts.cfg.Token,
		"X-StudioLink":  "1",
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/key: %d %s", resp.StatusCode, body)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/login" || u.Host != ts.s.addr {
		t.Fatalf("login URL = %s", out.URL)
	}
	return u.Query().Get("key")
}

// signIn redeems a fresh login key, which sets the session cookie.
func (ts *testSettings) signIn(t *testing.T) {
	t.Helper()
	resp, _ := ts.do(t, http.MethodGet, "/login?key="+ts.loginKey(t), "", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == settingsCookie {
			ts.cookie = c.Value
		}
	}
	if ts.cookie == "" {
		t.Fatal("login set no session cookie")
	}
}

func (ts *testSettings) reload(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(ts.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSettingsHostCheck(t *testing.T) {
	ts := newTestSettings(t)
	req, _ := http.NewRequest(http.MethodGet, ts.url+"/", nil)
	req.Host = "evil.example.com"
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: %d, want 403", resp.StatusCode)
	}
}

func TestSettingsNeedsSignIn(t *testing.T) {
	ts := newTestSettings(t)
	if resp, body := ts.do(t, http.MethodGet, "/", "", nil); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "expired") {
		t.Errorf("page without a session: %d %q", resp.StatusCode, body)
	}
	if resp, body := ts.do(t, http.MethodGet, "/api/state", "", nil); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"error"`) {
		t.Errorf("state without a session: %d %q", resp.StatusCode, body)
	}
	if resp, _ := ts.do(t, http.MethodGet, "/api/nope", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown endpoint without a session: %d, want 401", resp.StatusCode)
	}
}

func TestSettingsKeyNeedsMainToken(t *testing.T) {
	ts := newTestSettings(t)
	hdr := map[string]string{"X-StudioLink": "1", "Content-Type": "application/json"}
	if resp, _ := ts.do(t, http.MethodPost, "/api/key", "{}", hdr); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", resp.StatusCode)
	}
	hdr["Authorization"] = "Bearer wrong"
	if resp, _ := ts.do(t, http.MethodPost, "/api/key", "{}", hdr); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d, want 401", resp.StatusCode)
	}
}

func TestSettingsLoginKeyIsOneUse(t *testing.T) {
	ts := newTestSettings(t)
	key := ts.loginKey(t)
	resp, _ := ts.do(t, http.MethodGet, "/login?key="+key, "", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("first use: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == settingsCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Errorf("cookie flags: HttpOnly=%v SameSite=%v Path=%q", cookie.HttpOnly, cookie.SameSite, cookie.Path)
	}
	if !cookie.Expires.IsZero() || cookie.MaxAge != 0 {
		t.Errorf("cookie persists: Expires=%v MaxAge=%d", cookie.Expires, cookie.MaxAge)
	}
	if len(cookie.Value) < 40 {
		t.Errorf("session value %q is too short", cookie.Value)
	}

	resp, body := ts.do(t, http.MethodGet, "/login?key="+key, "", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "This link has expired") {
		t.Errorf("second use: %d %q, want 403 with the expired page", resp.StatusCode, body)
	}
}

func TestSettingsLoginKeyExpires(t *testing.T) {
	ts := newTestSettings(t)
	key := ts.loginKey(t)
	ts.clock = ts.clock.Add(3 * time.Minute)
	if resp, _ := ts.do(t, http.MethodGet, "/login?key="+key, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("expired key: %d, want 403", resp.StatusCode)
	}
}

func TestSettingsPageAfterSignIn(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	resp, body := ts.do(t, http.MethodGet, "/", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "<title>StudioLink</title>") {
		t.Errorf("page: %d", resp.StatusCode)
	}
	if strings.Contains(body, ts.cfg.Token) {
		t.Error("the page embeds the token")
	}
}

func TestSettingsMutatingRules(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	if resp, _ := ts.do(t, http.MethodGet, "/api/network", "", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET on a POST endpoint: %d, want 405", resp.StatusCode)
	}
	body := `{"loopback":true,"port":44770,"adapters":[]}`
	if resp, _ := ts.do(t, http.MethodPost, "/api/network", body, map[string]string{"Content-Type": "application/json"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST without X-StudioLink: %d, want 403", resp.StatusCode)
	}
	if resp, _ := ts.do(t, http.MethodPost, "/api/network", body, map[string]string{"X-StudioLink": "1", "Content-Type": "text/plain"}); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("POST as text/plain: %d, want 415", resp.StatusCode)
	}
	if ts.restarts != 0 {
		t.Errorf("refused requests restarted the bridge %d times", ts.restarts)
	}
}

func TestSettingsStateHidesToken(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	ts.adapters = []netif.Adapter{
		{Name: "Ethernet", Up: true, IPv4: []net.IP{net.ParseIP("192.168.1.5").To4()}},
		{Name: "vEthernet (WSL (Hyper-V firewall))", Up: true, IPv4: []net.IP{net.ParseIP("172.20.0.1").To4()}},
		{Name: "Loopback Pseudo-Interface 1", Up: true, IPv4: []net.IP{net.ParseIP("127.0.0.1").To4()}},
	}
	ts.cfg.ListenInterfaces = []config.InterfaceListen{{Name: "vethernet (default switch)", Port: 44770}}
	if err := ts.cfg.Save(ts.cfgPath); err != nil {
		t.Fatal(err)
	}
	ts.status = &Status{Version: "dev", Tailnet: "https://studiolink.tailnet.ts.net/mcp"}
	resp, body := ts.do(t, http.MethodGet, "/api/state", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, ts.cfg.Token) {
		t.Fatal("state exposes the token")
	}
	var st stateView
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if !st.BridgeRunning || !st.Studio.Found || st.Port != 44770 {
		t.Errorf("state = running %v studio %+v port %d", st.BridgeRunning, st.Studio, st.Port)
	}
	names := map[string]adapterView{}
	for _, a := range st.Adapters {
		names[a.Name] = a
	}
	if _, ok := names["Loopback Pseudo-Interface 1"]; ok {
		t.Error("loopback adapter is listed")
	}
	if a, ok := names["Ethernet"]; !ok || a.Enabled || a.IPv4 != "192.168.1.5" {
		t.Errorf("Ethernet = %+v", a)
	}
	// A configured adapter that is not present is still listed.
	missing, ok := names["vethernet (default switch)"]
	if !ok || missing.Up || !missing.Enabled {
		t.Errorf("configured adapter not merged: %+v", missing)
	}
	if st.Tailscale.URL == "" {
		t.Error("tailnet URL missing")
	}
}

func TestSettingsNetwork(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	resp, body := ts.post(t, "/api/network", map[string]any{
		"loopback": true, "port": 44771, "adapters": []string{"vEthernet (WSL (Hyper-V firewall))"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("network: %d %s", resp.StatusCode, body)
	}
	if ts.restarts != 1 {
		t.Errorf("restarts = %d, want 1", ts.restarts)
	}
	cfg := ts.reload(t)
	if len(cfg.Listen) != 1 || cfg.Listen[0] != "127.0.0.1:44771" {
		t.Errorf("listen = %v", cfg.Listen)
	}
	if len(cfg.ListenInterfaces) != 1 || cfg.ListenInterfaces[0].Name != "vEthernet (WSL (Hyper-V firewall))" || cfg.ListenInterfaces[0].Port != 44771 {
		t.Errorf("listen_interfaces = %+v", cfg.ListenInterfaces)
	}

	before, _ := os.ReadFile(ts.cfgPath)
	resp, body = ts.post(t, "/api/network", map[string]any{"loopback": false, "port": 44771, "adapters": []string{}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "nothing to serve on") {
		t.Errorf("no listeners: %d %s", resp.StatusCode, body)
	}
	after, _ := os.ReadFile(ts.cfgPath)
	if !bytes.Equal(before, after) || ts.restarts != 1 {
		t.Error("a refused change changed the config or restarted the bridge")
	}
}

func TestSettingsTailscale(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	if resp, body := ts.post(t, "/api/tailscale", map[string]any{"enabled": true, "hostname": "my-box"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("enable: %d %s", resp.StatusCode, body)
	}
	cfg := ts.reload(t)
	if cfg.Tailscale == nil || cfg.Tailscale.Hostname != "my-box" {
		t.Fatalf("tailscale = %+v", cfg.Tailscale)
	}
	if resp, _ := ts.post(t, "/api/tailscale", map[string]any{"enabled": true, "hostname": "bad name"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad hostname: %d, want 400", resp.StatusCode)
	}
	if resp, body := ts.post(t, "/api/tailscale", map[string]any{"enabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("disable: %d %s", resp.StatusCode, body)
	}
	if ts.reload(t).Tailscale != nil {
		t.Error("tailscale still set after disabling")
	}
	if ts.restarts != 2 {
		t.Errorf("restarts = %d, want 2", ts.restarts)
	}
}

func TestSettingsRelays(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	fp1 := "sha256:" + strings.Repeat("ab", 32)
	fp2 := "sha256:" + strings.Repeat("cd", 32)

	add := func(command string) (int, string) {
		t.Helper()
		resp, body := ts.post(t, "/api/relays/add", map[string]any{"command": command})
		return resp.StatusCode, body
	}
	if code, body := add("studiolink-host add-relay wss://vps.example.com:44771 " + fp1); code != http.StatusOK {
		t.Fatalf("full command: %d %s", code, body)
	}
	if code, body := add("wss://other.example.com:44771 " + fp2); code != http.StatusOK {
		t.Fatalf("url and fingerprint: %d %s", code, body)
	}
	if code, body := add("bare.example.com:44771"); code != http.StatusOK {
		t.Fatalf("bare url: %d %s", code, body)
	}
	cfg := ts.reload(t)
	if len(cfg.Relays) != 3 || cfg.Relays[2].URL != "wss://bare.example.com:44771" {
		t.Fatalf("relays = %+v", cfg.Relays)
	}

	// The same tunnel address replaces the relay instead of adding one.
	_, body := ts.post(t, "/api/relays/add", map[string]any{"command": "wss://vps.example.com:44771 " + fp2})
	if !strings.Contains(body, `"replaced":true`) {
		t.Errorf("re-add reply = %s, want replaced", body)
	}
	cfg = ts.reload(t)
	if len(cfg.Relays) != 3 || cfg.Relays[0].Fingerprint != fp2 {
		t.Errorf("after replace: %+v", cfg.Relays)
	}

	if code, _ := add("   "); code != http.StatusBadRequest {
		t.Errorf("empty command: %d, want 400", code)
	}
	if resp, _ := ts.post(t, "/api/relays/remove", map[string]any{"url": "wss://bare.example.com:44771"}); resp.StatusCode != http.StatusOK {
		t.Errorf("remove: %d", resp.StatusCode)
	}
	if n := len(ts.reload(t).Relays); n != 2 {
		t.Errorf("relays after remove = %d, want 2", n)
	}
	if resp, _ := ts.post(t, "/api/relays/remove", map[string]any{"url": "wss://nowhere.example.com"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("remove unknown: %d, want 400", resp.StatusCode)
	}
}

func TestSettingsTokenNew(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	old := ts.cfg.Token
	if resp, body := ts.post(t, "/api/token/new", map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("token/new: %d %s", resp.StatusCode, body)
	}
	cfg := ts.reload(t)
	if cfg.Token == old || len(cfg.Token) < 32 {
		t.Errorf("token not replaced")
	}
	if ts.restarts != 1 {
		t.Errorf("restarts = %d, want 1", ts.restarts)
	}
	// The main token now changes what /api/token shows, and the old one no
	// longer opens a login.
	resp, body := ts.do(t, http.MethodGet, "/api/token", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, cfg.Token) {
		t.Errorf("/api/token = %d %s", resp.StatusCode, body)
	}
}

func TestSettingsAutostart(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	if resp, _ := ts.post(t, "/api/autostart", map[string]any{"enabled": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("on: %d", resp.StatusCode)
	}
	if ts.autostrt.value != testAutostartCmd {
		t.Errorf("registered %q", ts.autostrt.value)
	}
	if _, body := ts.do(t, http.MethodGet, "/api/state", "", nil); !strings.Contains(body, `"autostart":true`) {
		t.Errorf("state does not show autostart on: %s", body)
	}
	if resp, _ := ts.post(t, "/api/autostart", map[string]any{"enabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("off: %d", resp.StatusCode)
	}
	if ts.autostrt.value != "" {
		t.Errorf("still registered: %q", ts.autostrt.value)
	}
}

func TestSettingsConnectMasksToken(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	resp, body := ts.do(t, http.MethodGet, "/api/connect?scenario=docker", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "host.docker.internal") {
		t.Fatalf("connect docker: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, ts.cfg.Token) || !strings.Contains(body, "<token>") {
		t.Error("connect without reveal shows the token, or does not mask it")
	}
	_, body = ts.do(t, http.MethodGet, "/api/connect?scenario=docker&reveal=1", "", nil)
	if !strings.Contains(body, ts.cfg.Token) {
		t.Error("connect with reveal=1 hides the token")
	}
	if resp, _ := ts.do(t, http.MethodGet, "/api/connect?scenario=moon", "", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown scenario: %d, want 400", resp.StatusCode)
	}
	if resp, _ := ts.do(t, http.MethodGet, "/api/connect", "", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no scenario: %d, want 400", resp.StatusCode)
	}
}

func TestSettingsOpen(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	dir := filepath.Dir(ts.cfgPath)
	for what, want := range map[string]string{
		"log":    filepath.Join(dir, "studiolink.log"),
		"folder": ts.cfgPath,
		"audit":  filepath.Join(dir, "audit.jsonl"),
	} {
		if resp, body := ts.post(t, "/api/open", map[string]any{"what": what}); resp.StatusCode != http.StatusOK {
			t.Fatalf("open %s: %d %s", what, resp.StatusCode, body)
		}
		if got := ts.opened[len(ts.opened)-1]; got != want {
			t.Errorf("open %s opened %q, want %q", what, got, want)
		}
	}
	if resp, _ := ts.post(t, "/api/open", map[string]any{"what": "registry"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown target: %d, want 400", resp.StatusCode)
	}
}

func TestSettingsRestart(t *testing.T) {
	ts := newTestSettings(t)
	ts.signIn(t)
	if resp, _ := ts.post(t, "/api/restart", map[string]any{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("restart: %d", resp.StatusCode)
	}
	if ts.restarts != 1 {
		t.Errorf("restarts = %d, want 1", ts.restarts)
	}
}

// TestOpenSettingsCommand runs the path "studiolink-host settings" takes: a
// run file that names the settings page, a key request, and the browser.
func TestOpenSettingsCommand(t *testing.T) {
	ts := newTestSettings(t)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			_, _ = io.WriteString(w, "studiolink-host\n")
		}
	}))
	defer live.Close()
	info := runInfo{PID: os.Getpid(), StatusURL: live.URL, SettingsURL: ts.url}
	data, _ := json.Marshal(info)
	if err := os.WriteFile(runFilePath(ts.cfgPath), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var opened string
	old := openBrowser
	openBrowser = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { openBrowser = old })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"settings", "-config", ts.cfgPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("settings: %d %s", code, stderr.String())
	}
	if !strings.HasPrefix(opened, ts.url+"/login?key=") {
		t.Errorf("opened %q", opened)
	}
	if !strings.Contains(stdout.String(), "Opened StudioLink settings") {
		t.Errorf("stdout = %q", stdout.String())
	}

	// Without a settings URL (a bridge from "serve"), the command says so.
	info.SettingsURL = ""
	data, _ = json.Marshal(info)
	_ = os.WriteFile(runFilePath(ts.cfgPath), data, 0o600)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"settings", "-config", ts.cfgPath}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "run studiolink-host start first") {
		t.Errorf("no settings page: %d %q", code, stderr.String())
	}
}
