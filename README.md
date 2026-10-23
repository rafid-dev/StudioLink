# StudioLink

Lets AI agents in VMs, Docker containers, on Tailscale peers or on external
servers use Roblox Studio's MCP server, which normally only works on the
Windows machine running Studio.

**Status:** phase 3. Agents in local Docker containers, WSL2 and local VMs,
and on Tailscale peers can connect over HTTP with a token. See
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

## WSL2 and local VMs
`studiolink-host interfaces` lists this PC's network adapters and what each
connects to (WSL2, Hyper-V, VMware, VirtualBox, LAN). Add the adapter by name:

```json
"listen_interfaces": [{"name": "vEthernet (WSL (Hyper-V firewall))", "port": 44770}]
```

The bridge rechecks adapters every 30 s, so it follows the address when it
changes (WSL2 gets a new one after a reboot) and waits for adapters that are
not up yet. In the VM or distro, connect to the host's address on that
network, for example `http://172.21.80.1:44770/mcp` (in WSL2 NAT mode, the
default gateway from `ip route`). WSL2 worked without a firewall rule here;
other VM networks may need you to allow `studiolink-host` in Windows Firewall.

## Tailscale
The bridge can join your tailnet as its own device. Tailscale does not need to
be installed on the Studio PC, and no firewall rule is needed:

```json
"tailscale": {"hostname": "studiolink"}
```

On first `serve`, the log prints a login URL; open it to add the device (or
set `TS_AUTHKEY` in the environment). Peers then connect to
`http://studiolink.<tailnet>.ts.net/mcp` with the token, and the log names the
tailnet user and device behind each session.

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
| `listen_interfaces` | none | Adapters to serve on, by name: `[{"name": ..., "port": ...}]` |
| `tailscale.hostname` | `studiolink` | Device name on the tailnet |
| `tailscale.port` | `80` (`443` with HTTPS) | Port on the tailnet |
| `tailscale.https` | `false` | Serve HTTPS with the tailnet's certificate (enable HTTPS certificates in the Tailscale admin console first) |
| `tailscale.ephemeral` | `false` | Remove the device from the tailnet when the bridge stops |
| `tailscale.state_dir` | `tsnet` next to the config | The device's identity; keep it private |
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
