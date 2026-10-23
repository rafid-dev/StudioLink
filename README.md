# StudioLink

Lets AI agents in VMs, Docker containers, on Tailscale peers or on external
servers use Roblox Studio's MCP server, which normally only works on the
Windows machine running Studio.

**Status:** phase 2 (listen mode). Agents on this PC and in local Docker
containers can connect over HTTP with a token. See
[docs/design.md](docs/design.md) for the architecture and roadmap.

## Quick start (Docker on this PC)
On Windows, with Roblox Studio open:

```powershell
go build -o studiolink-host.exe ./cmd/studiolink-host
.\studiolink-host.exe init     # writes %APPDATA%\StudioLink\studiolink.json with a new token
.\studiolink-host.exe serve    # leave running; -v logs every request
.\studiolink-host.exe token    # the token clients must send
```

In the container, point any Streamable HTTP MCP client at the bridge. For
Claude Code:

```bash
claude mcp add --transport http Roblox_Studio http://host.docker.internal:44770/mcp --header "Authorization: Bearer <token>"
```

Clients may send the token as `Authorization: Bearer <token>` or
`X-MCP-Auth: <token>`.

## How it behaves
- Each MCP session gets its own StudioMCP process, inside a Windows Job
  Object. Ending the session, an idle timeout (30 min) or the bridge crashing
  kills it and everything it started.
- Replies are plain JSON when quick. Slow tool calls switch to an SSE stream
  with keepalives every 15 s, so proxies and tunnels don't drop them.
- `GET /healthz` answers `ok` without a token, for health checks.

## Config (`studiolink.json`)
| Field | Default | Meaning |
|---|---|---|
| `token` | generated | Shared secret, at least 32 characters |
| `listen` | `["127.0.0.1:44770"]` | Addresses to serve on. Wildcards like `0.0.0.0` are refused |
| `allow_all_interfaces` | `false` | Permit wildcard listen addresses |
| `allowed_origins` | none | Browser origins allowed to call the bridge |
| `max_sessions` | `16` | Concurrent sessions (StudioMCP processes) |
| `idle_timeout_minutes` | `30` | Close sessions unused for this long |

## Development
Requires Go 1.27+.

```powershell
go run ./cmd/studiolink-host doctor   # check that mcp.bat and the config are found
.\check.ps1                           # gofmt, vet, staticcheck, tests, cross-builds
```
