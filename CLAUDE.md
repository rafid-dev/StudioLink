# StudioLink

Makes Roblox Studio's stdio-only MCP server usable from VMs, Docker, Tailscale
peers and external servers. Design and roadmap: `docs/design.md`; update its
phase list when a phase lands.

## Code
- Go 1.27, standard library first. Module path `studiolink`.
- `cmd/studiolink-host` (Windows only, next to Studio), `cmd/studiolink-client`
  (any OS). Shared code lives in `internal/`.
- Always start Studio MCP through `%LOCALAPPDATA%\Roblox\mcp.bat`, never a
  versioned `StudioMCP.exe` path.
- Security defaults are not negotiable: token auth on every request, loopback
  binding unless an interface is named, TLS for dial-out.
- Lint tools are pinned in `go.mod` (`tool` directive); run them with
  `go tool staticcheck ./...`.

## Checking
- Before finishing a change, run `.\check.ps1`.
- When a change touches the Studio side, test against a real open Studio using
  the `Roblox_Studio` MCP tools in the session.
