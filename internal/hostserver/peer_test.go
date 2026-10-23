package hostserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// lockedBuffer is a bytes.Buffer safe to log into from several goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSessionLogNamesPeer(t *testing.T) {
	var logs lockedBuffer
	h := newHarness(t, func(o *Options) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	// Stand in for tailnet.IdentifyPeers.
	h.http.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.srv.Handler().ServeHTTP(w, r.WithContext(WithPeer(r.Context(), "rafid@ on laptop")))
	})
	h.initialize()
	if !strings.Contains(logs.String(), `peer="rafid@ on laptop"`) {
		t.Errorf("session log does not name the peer:\n%s", logs.String())
	}
}
