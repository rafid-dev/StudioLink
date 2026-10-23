package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Dialer keeps studiolink-host connected to one relay, reconnecting with
// backoff whenever the tunnel drops.
type Dialer struct {
	URL         string // wss://relay:44771/tunnel
	Fingerprint string // pins the relay's certificate; empty trusts public CAs
	Token       string
	// Serve serves HTTP on the tunnel. It must return once the listener
	// closes, which happens when the tunnel drops.
	Serve func(net.Listener)
	Log   *slog.Logger

	MinBackoff, MaxBackoff time.Duration // default 1s and 1m

	mu      sync.Mutex
	up      bool
	since   time.Time // when up last changed
	lastErr error
}

// DialerState is a snapshot of a Dialer for status output.
type DialerState struct {
	Connected bool
	Since     time.Time // when the tunnel came up or went down
	LastError string    // why the last attempt or tunnel failed
}

// State reports whether the tunnel is up.
func (d *Dialer) State() DialerState {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := DialerState{Connected: d.up, Since: d.since}
	if d.lastErr != nil {
		st.LastError = d.lastErr.Error()
	}
	return st
}

func (d *Dialer) setState(up bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if up != d.up || d.since.IsZero() {
		d.since = time.Now()
	}
	d.up = up
	if err != nil {
		d.lastErr = err
	}
}

// Run connects and reconnects until ctx ends.
func (d *Dialer) Run(ctx context.Context) {
	if d.MinBackoff == 0 {
		d.MinBackoff = time.Second
	}
	if d.MaxBackoff == 0 {
		d.MaxBackoff = time.Minute
	}
	backoff := d.MinBackoff
	for {
		up, err := d.once(ctx)
		d.setState(false, err)
		if ctx.Err() != nil {
			return
		}
		if up > time.Minute {
			backoff = d.MinBackoff // it was working; retry promptly
		}
		// Jitter so many hosts behind one relay don't reconnect in step.
		wait := backoff/2 + rand.N(backoff/2+1)
		d.Log.Warn("relay tunnel down; will retry", "relay", d.URL, "err", err, "in", wait.Round(time.Second))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, d.MaxBackoff)
	}
}

// once runs one tunnel and reports how long it was up.
func (d *Dialer) once(ctx context.Context) (time.Duration, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		return 0, err
	}
	sess, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		conn.Close()
		return 0, err
	}
	start := time.Now()
	d.setState(true, nil)
	d.Log.Info("connected to relay", "relay", d.URL)
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()
	d.Serve(sess)
	sess.Close()
	return time.Since(start), errors.New("tunnel closed")
}

func (d *Dialer) dial(ctx context.Context) (net.Conn, error) {
	u, err := url.Parse(d.URL)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if u.Scheme == "wss" {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		if d.Fingerprint != "" {
			if tr.TLSClientConfig, err = PinnedTLS(d.Fingerprint); err != nil {
				return nil, err
			}
		}
	}
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dialCtx, d.URL, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: tr},
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + d.Token},
			protocolHeader:  {protocolVersion()},
		},
	})
	if err != nil {
		if resp != nil && resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return nil, fmt.Errorf("relay refused the tunnel: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		return nil, err
	}
	return relayConn{netConn(ctx, c), relayAddr(u.Host)}, nil
}

// relayConn names the relay as the remote address, so sessions that arrive
// through the tunnel log where they came from.
type relayConn struct {
	net.Conn
	addr relayAddr
}

func (c relayConn) RemoteAddr() net.Addr { return c.addr }

type relayAddr string

func (relayAddr) Network() string  { return "relay" }
func (a relayAddr) String() string { return "relay " + string(a) }
