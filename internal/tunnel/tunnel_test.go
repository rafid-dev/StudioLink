package tunnel

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"studiolink/internal/hostserver"
	"studiolink/internal/mcptest"
	"studiolink/internal/shim"
)

const testToken = "test-token-0123456789abcdef0123456789"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// setup runs a relay (tunnel over TLS with a self-signed certificate, plus
// the agent side) and returns it with its tunnel URL, the certificate's
// fingerprint and the agents' /mcp URL.
func setup(t *testing.T) (relay *Relay, tunnelURL, fingerprint, agentURL string) {
	t.Helper()
	cert, err := LoadOrCreateCert(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	relay = &Relay{Token: testToken, Log: quiet}
	tunnelSrv := httptest.NewUnstartedServer(relay.TunnelHandler())
	tunnelSrv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	tunnelSrv.StartTLS()
	agentSrv := httptest.NewServer(relay.AgentHandler())
	t.Cleanup(func() {
		relay.Close()
		agentSrv.Close()
		tunnelSrv.Close()
	})
	return relay, "wss://" + tunnelSrv.Listener.Addr().String() + Path, CertFingerprint(cert), agentSrv.URL + "/mcp"
}

// startHost runs a bridge (with fake StudioMCPs) that dials the relay.
func startHost(t *testing.T, d *Dialer) *hostserver.Server {
	t.Helper()
	srv := hostserver.New(hostserver.Options{
		Token:     testToken,
		KeepAlive: 50 * time.Millisecond,
		Logger:    quiet,
		Spawn:     func() (hostserver.Process, error) { return mcptest.NewFakeProc(300 * time.Millisecond), nil },
	})
	d.Log = quiet
	d.MinBackoff, d.MaxBackoff = 10*time.Millisecond, 50*time.Millisecond
	d.Serve = func(ln net.Listener) { _ = http.Serve(ln, srv.Handler()) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		srv.Close()
	})
	return srv
}

func waitConnected(t *testing.T, r *Relay) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := r.Connected(); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("host never connected to the relay")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func probe(t *testing.T, agentURL string) (*shim.ProbeResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return shim.Probe(ctx, shim.Options{URL: agentURL, Token: testToken, Log: quiet})
}

func TestAgentReachesStudioThroughTunnel(t *testing.T) {
	relay, tunnelURL, fp, agentURL := setup(t)
	host := startHost(t, &Dialer{URL: tunnelURL, Fingerprint: fp, Token: testToken})
	waitConnected(t, relay)

	res, err := probe(t, agentURL)
	if err != nil {
		t.Fatal(err)
	}
	if res.Server != "fake" {
		t.Errorf("probe = %+v", res)
	}
	if n := host.SessionCount(); n != 0 {
		t.Errorf("probe through the tunnel left %d sessions open", n)
	}

	// A slow call outlasts the keepalive, so the reply is SSE; it must
	// stream through the relay unbuffered.
	sid := initialize(t, agentURL)
	req, _ := http.NewRequest("POST", agentURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(string(body), "slow done") {
		t.Errorf("slow call through tunnel: %s %q", resp.Header.Get("Content-Type"), body)
	}
}

func initialize(t *testing.T, agentURL string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", agentURL, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	sid := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != 200 || sid == "" {
		t.Fatalf("initialize through tunnel: %s", resp.Status)
	}
	return sid
}

func TestHostReconnects(t *testing.T) {
	relay, tunnelURL, fp, agentURL := setup(t)
	startHost(t, &Dialer{URL: tunnelURL, Fingerprint: fp, Token: testToken})
	waitConnected(t, relay)

	relay.Close() // as if the network dropped
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := probe(t, agentURL); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("host did not reconnect")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentWhileHostAway(t *testing.T) {
	_, _, _, agentURL := setup(t)
	resp, err := http.Post(agentURL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "not connected") {
		t.Errorf("agent request with no host: %s %q", resp.Status, body)
	}
	health, err := http.Get(strings.TrimSuffix(agentURL, "/mcp") + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("healthz with no host = %s", health.Status)
	}
}

func TestDialRefusals(t *testing.T) {
	_, tunnelURL, fp, _ := setup(t)
	other, err := LoadOrCreateCert(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		d    Dialer
		want string
	}{
		{"wrong token", Dialer{URL: tunnelURL, Fingerprint: fp, Token: "wrong"}, "401"},
		{"wrong fingerprint", Dialer{URL: tunnelURL, Fingerprint: CertFingerprint(other), Token: testToken}, "not the pinned fingerprint"},
		{"self-signed without pin", Dialer{URL: tunnelURL, Token: testToken}, "certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.d.Log = quiet
			tt.d.Serve = func(net.Listener) { t.Error("served on a refused tunnel") }
			_, err := tt.d.once(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestProtocolMismatch(t *testing.T) {
	_, tunnelURL, fp, _ := setup(t)
	cfg, _ := PinnedTLS(fp)
	req, _ := http.NewRequest("GET", "https"+strings.TrimPrefix(tunnelURL, "wss"), nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(protocolHeader, "999")
	resp, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "matching versions") {
		t.Errorf("protocol mismatch: %s %q", resp.Status, body)
	}
}

func TestCertIsReused(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateCert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if CertFingerprint(a) != CertFingerprint(b) {
		t.Error("second load made a new certificate")
	}
}

func TestValidFingerprint(t *testing.T) {
	good := "sha256:" + strings.Repeat("ab", 32)
	cases := map[string]bool{
		good:                  true,
		strings.ToUpper(good): true,
		"sha256:" + strings.Repeat("AB:", 31) + "AB": true,
		strings.Repeat("ab", 32):                     false,
		"sha256:abcd":                                false,
		"sha256:" + strings.Repeat("zz", 32):         false,
	}
	for fp, want := range cases {
		if got := ValidFingerprint(fp); got != want {
			t.Errorf("ValidFingerprint(%q) = %v, want %v", fp, got, want)
		}
	}
}
