package main

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"studiolink/internal/config"
	"studiolink/internal/hostserver"
	"studiolink/internal/netif"
	"studiolink/internal/version"
)

//go:embed settings.html
var settingsPage []byte

const (
	settingsCookie = "studiolink_settings"
	loginKeyTTL    = 2 * time.Minute
	maxSessions    = 16
)

// openBrowser opens a URL in the default browser. Tests replace it.
var openBrowser = openURL

// errNoSettings means the running StudioLink has no settings page: a
// bridge started by "serve" rather than the tray.
var errNoSettings = errors.New("the running StudioLink has no settings page")

// settingsServer serves the settings page on 127.0.0.1, from the tray
// process. Only the browser that was sent a one-time login link can use it.
type settingsServer struct {
	cfgPath   string
	addr      string       // host:port it listens on, for the Host check
	ln        net.Listener // set by start
	restart   func()       // restarts the bridge after a config change
	status    func() *Status
	adapters  func() ([]netif.Adapter, error)
	autostart interface {
		Get() (string, error)
		Set(string) error
		Remove() error
	}
	autostartCmd string // autostart.Command(exe, cfgPath)
	open         func(target string)
	launcher     func() (string, error)

	now func() time.Time // a seam for tests; time.Now when nil

	mu       sync.Mutex
	keys     map[string]time.Time // one-time login key -> expiry
	sessions []string             // session cookie values, newest last
}

// start listens on a free loopback port and serves until the process ends.
// It returns the page's address.
func (s *settingsServer) start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("cannot open the settings port: %w", err)
	}
	s.ln, s.addr = ln, ln.Addr().String()
	go func() { _ = http.Serve(ln, s) }()
	return "http://" + s.addr, nil
}

func (s *settingsServer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// newLoginURL makes a one-time link that signs a browser in to the page.
func (s *settingsServer) newLoginURL() (string, error) {
	key, err := newSecret()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	if s.keys == nil {
		s.keys = map[string]time.Time{}
	}
	for k, exp := range s.keys {
		if !now.Before(exp) {
			delete(s.keys, k)
		}
	}
	s.keys[key] = now.Add(loginKeyTTL)
	return "http://" + s.addr + "/login?key=" + key, nil
}

// redeemKey uses up a login key. It is valid once, for loginKeyTTL.
func (s *settingsServer) redeemKey(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.keys[key]
	if !ok {
		return false
	}
	delete(s.keys, key)
	return s.clock().Before(exp)
}

// signedIn reports whether r carries a session cookie this server issued.
func (s *settingsServer) signedIn(r *http.Request) bool {
	c, err := r.Cookie(settingsCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := false
	for _, v := range s.sessions {
		if subtle.ConstantTimeCompare([]byte(v), []byte(c.Value)) == 1 {
			ok = true
		}
	}
	return ok
}

// bearerOK reports whether r carries the main token, read fresh from the
// config file.
func (s *settingsServer) bearerOK(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(cfg.Token)) == 1
}

// ServeHTTP checks the Host header, which stops DNS rebinding, then routes.
func (s *settingsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.addr {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/login":
		s.login(w, r)
	case "/":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.signedIn(r) {
			writeExpired(w, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(settingsPage)
	default:
		s.api(w, r)
	}
}

// login trades a one-time key for a session cookie.
func (s *settingsServer) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.redeemKey(r.URL.Query().Get("key")) {
		writeExpired(w, http.StatusForbidden)
		return
	}
	value, err := newSecret()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.sessions = append(s.sessions, value)
	if len(s.sessions) > maxSessions {
		s.sessions = s.sessions[len(s.sessions)-maxSessions:]
	}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     settingsCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// writeExpired shows the short page a browser sees without a valid link or
// session.
func writeExpired(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>StudioLink</title><body style="font-family:system-ui,sans-serif;margin:16px"><p>This link has expired. Open Settings from the StudioLink tray icon.</p>`)
}

// apiRoute is one JSON endpoint.
type apiRoute struct {
	method string
	bearer bool // authenticated by the main token instead of a session
	handle http.HandlerFunc
}

func (s *settingsServer) routes() map[string]apiRoute {
	get, post := http.MethodGet, http.MethodPost
	return map[string]apiRoute{
		"/api/key":           {method: post, bearer: true, handle: s.handleKey},
		"/api/state":         {method: get, handle: s.handleState},
		"/api/connect":       {method: get, handle: s.handleConnect},
		"/api/token":         {method: get, handle: s.handleToken},
		"/api/network":       {method: post, handle: s.handleNetwork},
		"/api/tailscale":     {method: post, handle: s.handleTailscale},
		"/api/relays/add":    {method: post, handle: s.handleRelayAdd},
		"/api/relays/remove": {method: post, handle: s.handleRelayRemove},
		"/api/autostart":     {method: post, handle: s.handleAutostart},
		"/api/token/new":     {method: post, handle: s.handleTokenNew},
		"/api/open":          {method: post, handle: s.handleOpen},
		"/api/restart":       {method: post, handle: s.handleRestart},
	}
}

// api checks a JSON request, in order: that the route exists, the method,
// who is asking, and for POST the X-StudioLink header and JSON body type.
// The header makes a browser send a CORS preflight, which is never answered,
// so a web page on another site cannot POST.
func (s *settingsServer) api(w http.ResponseWriter, r *http.Request) {
	rt, ok := s.routes()[r.URL.Path]
	if !ok {
		if !s.signedIn(r) {
			jsonError(w, http.StatusUnauthorized, "sign in first: open Settings from the StudioLink tray icon")
			return
		}
		jsonError(w, http.StatusNotFound, "no such endpoint")
		return
	}
	if r.Method != rt.method {
		w.Header().Set("Allow", rt.method)
		jsonError(w, http.StatusMethodNotAllowed, "use "+rt.method)
		return
	}
	if rt.bearer {
		if !s.bearerOK(r) {
			jsonError(w, http.StatusUnauthorized, "the main token is required")
			return
		}
	} else if !s.signedIn(r) {
		jsonError(w, http.StatusUnauthorized, "sign in first: open Settings from the StudioLink tray icon")
		return
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("X-StudioLink") != "1" {
			jsonError(w, http.StatusForbidden, "missing X-StudioLink header")
			return
		}
		if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			jsonError(w, http.StatusUnsupportedMediaType, "send Content-Type: application/json")
			return
		}
	}
	rt.handle(w, r)
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// readBody decodes a JSON request body into v, writing the error if it fails.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

// loadFresh reads the config file for a request, writing the error if it
// fails.
func (s *settingsServer) loadFresh(w http.ResponseWriter) (*config.Config, bool) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return cfg, true
}

// edit applies change to the config file, validates and saves it, then
// restarts the bridge. A change that leaves the config invalid is refused
// and the file stays as it was.
func (s *settingsServer) edit(w http.ResponseWriter, change func(*config.Config) error) bool {
	cfg, ok := s.loadFresh(w)
	if !ok {
		return false
	}
	if err := change(cfg); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := cfg.Validate(); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := cfg.Save(s.cfgPath); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if s.restart != nil {
		s.restart()
	}
	return true
}

func (s *settingsServer) handleKey(w http.ResponseWriter, _ *http.Request) {
	url, err := s.newLoginURL()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// stateView is what the page shows. It never includes the token.
type stateView struct {
	Version       string                   `json:"version"`
	BridgeRunning bool                     `json:"bridge_running"`
	Studio        studioView               `json:"studio"`
	Config        configView               `json:"config"`
	Port          int                      `json:"port"`
	Loopback      bool                     `json:"loopback"`
	Adapters      []adapterView            `json:"adapters"`
	Tailscale     tailscaleView            `json:"tailscale"`
	Relays        []relayView              `json:"relays"`
	Sessions      []hostserver.SessionInfo `json:"sessions"`
	Autostart     bool                     `json:"autostart"`
	Log           string                   `json:"log"`
	AuditLog      string                   `json:"audit_log"`
}

type studioView struct {
	Found bool   `json:"found"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

// configView is the config file without its tokens.
type configView struct {
	Path             string                   `json:"path"`
	Listen           []string                 `json:"listen"`
	ListenInterfaces []config.InterfaceListen `json:"listen_interfaces"`
	Tailscale        *config.Tailscale        `json:"tailscale,omitempty"`
	Relays           []config.Relay           `json:"relays"`
}

type adapterView struct {
	Name    string `json:"name"`
	IPv4    string `json:"ipv4"`
	Up      bool   `json:"up"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}

type tailscaleView struct {
	Enabled  bool   `json:"enabled"`
	Hostname string `json:"hostname"`
	URL      string `json:"url"`
}

type relayView struct {
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Connected   bool   `json:"connected"`
	LastError   string `json:"last_error,omitempty"`
}

func (s *settingsServer) handleState(w http.ResponseWriter, _ *http.Request) {
	cfg, ok := s.loadFresh(w)
	if !ok {
		return
	}
	var st *Status
	if s.status != nil {
		st = s.status()
	}
	found, path, errText := false, "", ""
	if s.launcher != nil {
		if p, err := s.launcher(); err != nil {
			errText = err.Error()
		} else {
			found, path = true, p
		}
	}
	autostartOn := false
	if s.autostart != nil {
		if cur, err := s.autostart.Get(); err == nil {
			autostartOn = cur == s.autostartCmd
		}
	}
	sessions := []hostserver.SessionInfo{}
	if st != nil {
		sessions = append(sessions, st.Sessions...)
	}
	view := stateView{
		Version:       version.Version,
		BridgeRunning: st != nil,
		Studio:        studioView{Found: found, Path: path, Error: errText},
		Config: configView{
			Path:             s.cfgPath,
			Listen:           cfg.Listen,
			ListenInterfaces: cfg.ListenInterfaces,
			Tailscale:        cfg.Tailscale,
			Relays:           cfg.Relays,
		},
		Port:      settingsPort(cfg),
		Loopback:  loopbackPort(cfg) != 0,
		Adapters:  s.adapterViews(cfg),
		Tailscale: tailscaleView{},
		Relays:    s.relayViews(cfg, st),
		Sessions:  sessions,
		Autostart: autostartOn,
		Log:       logPathFor(s.cfgPath),
		AuditLog:  cfg.AuditPath(s.cfgPath),
	}
	if ts := cfg.Tailscale; ts != nil {
		view.Tailscale = tailscaleView{Enabled: true, Hostname: ts.DeviceName()}
	}
	if st != nil {
		view.Tailscale.URL = st.Tailnet
	}
	writeJSON(w, http.StatusOK, view)
}

// logPathFor is where the tray writes its log, next to the config.
func logPathFor(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "studiolink.log")
}

// settingsPort is the port the bridge serves agents on, for the form.
func settingsPort(cfg *config.Config) int {
	if p := loopbackPort(cfg); p != 0 {
		return p
	}
	if len(cfg.ListenInterfaces) > 0 {
		return cfg.ListenInterfaces[0].Port
	}
	return config.DefaultPort
}

// adapterViews lists the adapters the bridge can serve on, plus configured
// ones that are not present right now. The loopback adapter is left out:
// it is the loopback checkbox.
func (s *settingsServer) adapterViews(cfg *config.Config) []adapterView {
	var list []netif.Adapter
	if s.adapters != nil {
		list, _ = s.adapters()
	}
	enabled := func(name string) bool {
		for _, li := range cfg.ListenInterfaces {
			if strings.EqualFold(li.Name, name) {
				return true
			}
		}
		return false
	}
	out := []adapterView{}
	seen := map[string]bool{}
	for _, a := range list {
		if strings.Contains(strings.ToLower(a.Name), "loopback") {
			continue
		}
		ips := make([]string, 0, len(a.IPv4))
		for _, ip := range a.IPv4 {
			ips = append(ips, ip.String())
		}
		out = append(out, adapterView{
			Name:    a.Name,
			IPv4:    strings.Join(ips, ", "),
			Up:      a.Up,
			Kind:    a.Kind(),
			Enabled: enabled(a.Name),
		})
		seen[strings.ToLower(a.Name)] = true
	}
	for _, li := range cfg.ListenInterfaces {
		key := strings.ToLower(li.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		missing := netif.Adapter{Name: li.Name}
		out = append(out, adapterView{Name: li.Name, Kind: missing.Kind(), Enabled: true})
	}
	return out
}

// relayViews pairs each configured relay with its live state.
func (s *settingsServer) relayViews(cfg *config.Config, st *Status) []relayView {
	out := []relayView{}
	for _, r := range cfg.Relays {
		v := relayView{URL: r.URL, Fingerprint: r.Fingerprint}
		if st != nil {
			for _, rs := range st.Relays {
				if rs.URL == r.TunnelURL() {
					v.Connected, v.LastError = rs.Connected, rs.LastError
				}
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *settingsServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("scenario")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "scenario is required: docker, wsl, vm, tailscale or server")
		return
	}
	cfg, ok := s.loadFresh(w)
	if !ok {
		return
	}
	text, err := connectText(s.cfgPath, cfg, name)
	if errors.Is(err, errUnknownScenario) {
		jsonError(w, http.StatusBadRequest, "unknown scenario "+name)
		return
	}
	if q.Get("reveal") != "1" {
		text = strings.ReplaceAll(text, cfg.Token, "<token>")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, text)
}

func (s *settingsServer) handleToken(w http.ResponseWriter, _ *http.Request) {
	cfg, ok := s.loadFresh(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": cfg.Token})
}

type networkRequest struct {
	Loopback bool     `json:"loopback"`
	Port     int      `json:"port"`
	Adapters []string `json:"adapters"`
}

func (s *settingsServer) handleNetwork(w http.ResponseWriter, r *http.Request) {
	var req networkRequest
	if !readBody(w, r, &req) {
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		jsonError(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}
	if s.edit(w, func(cfg *config.Config) error {
		cfg.Listen = nil
		if req.Loopback {
			cfg.Listen = []string{fmt.Sprintf("127.0.0.1:%d", req.Port)}
		}
		cfg.ListenInterfaces = nil
		seen := map[string]bool{}
		for _, name := range req.Adapters {
			name = strings.TrimSpace(name)
			key := strings.ToLower(name)
			if name == "" || seen[key] {
				continue
			}
			seen[key] = true
			cfg.ListenInterfaces = append(cfg.ListenInterfaces, config.InterfaceListen{Name: name, Port: req.Port})
		}
		return nil
	}) {
		writeOK(w)
	}
}

type tailscaleRequest struct {
	Enabled  bool   `json:"enabled"`
	Hostname string `json:"hostname"`
}

func (s *settingsServer) handleTailscale(w http.ResponseWriter, r *http.Request) {
	var req tailscaleRequest
	if !readBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Hostname)
	if name != "" && !validHostname(name) {
		jsonError(w, http.StatusBadRequest, "device name: use letters, digits and hyphens, at most 63 characters")
		return
	}
	if s.edit(w, func(cfg *config.Config) error {
		if !req.Enabled {
			cfg.Tailscale = nil
			return nil
		}
		if cfg.Tailscale == nil {
			cfg.Tailscale = &config.Tailscale{}
		}
		cfg.Tailscale.Hostname = name
		return nil
	}) {
		writeOK(w)
	}
}

// validHostname reports whether s can be a tailnet device name.
func validHostname(s string) bool {
	if len(s) == 0 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

type relayAddRequest struct {
	Command string `json:"command"`
}

func (s *settingsServer) handleRelayAdd(w http.ResponseWriter, r *http.Request) {
	var req relayAddRequest
	if !readBody(w, r, &req) {
		return
	}
	relay, err := parseRelayCommand(req.Command)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	replaced := false
	if s.edit(w, func(cfg *config.Config) error {
		replaced = mergeRelay(cfg, relay)
		return nil
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "replaced": replaced})
	}
}

type relayRemoveRequest struct {
	URL string `json:"url"`
}

func (s *settingsServer) handleRelayRemove(w http.ResponseWriter, r *http.Request) {
	var req relayRemoveRequest
	if !readBody(w, r, &req) {
		return
	}
	if s.edit(w, func(cfg *config.Config) error {
		var kept []config.Relay
		found := false
		for _, rl := range cfg.Relays {
			if rl.URL == req.URL || rl.TunnelURL() == req.URL {
				found = true
				continue
			}
			kept = append(kept, rl)
		}
		if !found {
			return fmt.Errorf("no relay %s", req.URL)
		}
		cfg.Relays = kept
		return nil
	}) {
		writeOK(w)
	}
}

type autostartRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *settingsServer) handleAutostart(w http.ResponseWriter, r *http.Request) {
	var req autostartRequest
	if !readBody(w, r, &req) {
		return
	}
	if s.autostart == nil {
		jsonError(w, http.StatusInternalServerError, "start at login is not available")
		return
	}
	var err error
	if req.Enabled {
		err = s.autostart.Set(s.autostartCmd)
	} else {
		err = s.autostart.Remove()
	}
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "start at login: "+err.Error())
		return
	}
	writeOK(w)
}

func (s *settingsServer) handleTokenNew(w http.ResponseWriter, _ *http.Request) {
	token, err := config.NewToken()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.edit(w, func(cfg *config.Config) error {
		cfg.Token = token
		return nil
	}) {
		writeOK(w)
	}
}

type openRequest struct {
	What string `json:"what"`
}

func (s *settingsServer) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req openRequest
	if !readBody(w, r, &req) {
		return
	}
	var target string
	switch req.What {
	case "log":
		target = logPathFor(s.cfgPath)
	case "audit":
		cfg, ok := s.loadFresh(w)
		if !ok {
			return
		}
		target = cfg.AuditPath(s.cfgPath)
		if target == "" {
			jsonError(w, http.StatusBadRequest, "the audit log is off")
			return
		}
	case "folder":
		target = s.cfgPath
	default:
		jsonError(w, http.StatusBadRequest, `what must be "log", "audit" or "folder"`)
		return
	}
	if s.open != nil {
		s.open(target)
	}
	writeOK(w)
}

func (s *settingsServer) handleRestart(w http.ResponseWriter, _ *http.Request) {
	if s.restart != nil {
		s.restart()
	}
	writeOK(w)
}

// openSettings asks the tray running with the config at cfgPath for a
// one-time link to its settings page and opens it in the browser.
func openSettings(cfgPath, token string) error {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	info, err := readRunFile(runFilePath(abs))
	if err != nil {
		return err
	}
	if info.SettingsURL == "" {
		return errNoSettings
	}
	if !info.alive() {
		return errNotRunning
	}
	req, err := http.NewRequest(http.MethodPost, info.SettingsURL+"/api/key", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-StudioLink", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("settings: %s", resp.Status)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.URL == "" {
		return errors.New("settings: no link in the reply")
	}
	return openBrowser(out.URL)
}

// runSettings opens the settings page of the running tray.
func runSettings(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("settings", stderr)
	if fset.Parse(args) != nil {
		return 2
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch err := openSettings(*path, cfg.Token); {
	case err == nil:
		fmt.Fprintln(stdout, "Opened StudioLink settings in your browser.")
		return 0
	case errors.Is(err, errNotRunning), errors.Is(err, errNoSettings):
		fmt.Fprintln(stderr, "Settings are part of the tray: run studiolink-host start first.")
		return 1
	default:
		fmt.Fprintln(stderr, "settings:", err)
		return 1
	}
}
