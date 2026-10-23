// Package tunnel implements dial-out mode. studiolink-host opens a TLS
// WebSocket to a relay (studiolink-client relay) on a server it can reach,
// and runs yamux over it. The relay opens one stream per agent connection and
// the host serves each with the ordinary bridge handler, so agents on the
// server reach Studio without the Studio PC accepting any connection.
package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"studiolink/internal/version"
)

const (
	// Path is where a relay accepts the host's tunnel.
	Path = "/tunnel"
	// DefaultPort is the relay's default tunnel port.
	DefaultPort = 44771

	protocolHeader = "X-StudioLink-Protocol"
	// yamux frames are at most its 256 KiB stream window plus a header, so
	// a larger WebSocket message means a broken or hostile peer.
	maxMessage = 1 << 20
)

func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	// Notice a dead tunnel within about half a minute.
	c.KeepAliveInterval = 15 * time.Second
	c.ConnectionWriteTimeout = 15 * time.Second
	c.LogOutput = io.Discard
	return c
}

// netConn adapts a WebSocket to the byte stream yamux runs on. ctx bounds the
// connection's lifetime.
func netConn(ctx context.Context, c *websocket.Conn) net.Conn {
	nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
	c.SetReadLimit(maxMessage) // NetConn removes the limit; put one back
	return nc
}

func protocolVersion() string { return strconv.Itoa(version.ProtocolVersion) }

func bearer(r *http.Request) string {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return token
}
