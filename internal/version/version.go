// Package version holds build information, set at link time:
//
//	go build -ldflags "-X studiolink/internal/version.Version=v0.1.0"
package version

// Version is the release version. Builds without ldflags report "dev".
var Version = "dev"

// ProtocolVersion is the bridge's own wire protocol version (host <-> client),
// separate from the MCP protocol version. Bump it on incompatible changes.
const ProtocolVersion = 1
