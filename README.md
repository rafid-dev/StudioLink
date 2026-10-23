# StudioLink

Lets AI agents in VMs, Docker containers, on Tailscale peers or on external
servers use Roblox Studio's MCP server, which normally only works on the
Windows machine running Studio.

**Status:** phase 6. Agents in local Docker containers, WSL2 and local VMs,
and on Tailscale peers can connect over HTTP with a token, directly or through
`studiolink-client` for agents that only run stdio MCP servers. Agents on
external servers connect through a relay that the Studio PC dials out to.
Tools can be limited per agent, every session and tool call is audited, and
the bridge can run from the notification area and start at login. See
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

## Agents that only run stdio MCP servers
`studiolink-client` runs next to the agent (Linux, macOS or Windows) and
gives it an ordinary stdio MCP server that forwards to the bridge:

```bash
GOOS=linux go build -o studiolink-client ./cmd/studiolink-client   # on the dev machine

export STUDIOLINK_URL=http://host.docker.internal:44770/mcp
export STUDIOLINK_TOKEN=<token>          # or pass -token-file PATH
studiolink-client check                  # ok ... RobloxStudio 1.0.0, MCP 2025-06-18, 28 tools

claude mcp add Roblox_Studio -e STUDIOLINK_URL=$STUDIOLINK_URL -e STUDIOLINK_TOKEN=$STUDIOLINK_TOKEN -- studiolink-client connect
```

There is no `-token` flag, because command lines are visible to other
processes. When the agent stops, the client ends its session on the bridge,
so Studio MCP processes don't linger until the idle timeout. It warns if the
token would cross the internet unencrypted (plain `http://` to a public
address).

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

## External servers (dial-out)
For an agent on a VPS or cloud machine, the Studio PC needs no open port: it
dials out to a relay on the server over TLS, and agents there connect to the
relay locally. On the server, run the relay with the same token:

```bash
export STUDIOLINK_TOKEN=<token>          # or pass -token-file PATH
studiolink-client relay                  # leave running; tunnel on :44771, agents on 127.0.0.1:44770
```

On first start it makes a self-signed certificate (kept in
`~/.config/StudioLink`) and prints the `relays` entry for the Studio PC,
including the certificate's fingerprint; `studiolink-client fingerprint`
prints it again. Add it to `studiolink.json` and restart `serve`:

```json
"relays": [{"url": "wss://vps.example.com:44771", "fingerprint": "sha256:..."}]
```

Port 44771 must be open on the server. Agents on the server then use
`http://127.0.0.1:44770/mcp` with the token, directly or through
`studiolink-client connect`. `GET /healthz` on the relay says whether the
Studio PC is connected.

The host only connects to a relay whose certificate matches the fingerprint,
so it never sends the token to anything else. With a domain and a
certificate from a public CA, pass `-cert` and `-key` to the relay (renewals
are picked up without a restart) and leave out the fingerprint. Behind a
TLS-terminating reverse proxy on the same server, use
`-plain-tunnel -tunnel-listen 127.0.0.1:44771` and proxy WebSocket upgrades on
`/tunnel` to it. The host reconnects with backoff (1 s to 1 min) when the
tunnel drops; a relay serves one Studio PC at a time.

## Limiting tools and agents
`tools` limits what every agent can call; each entry in `clients` gives one
agent its own token and, optionally, a narrower list. Patterns match tool
names case-insensitively with `*` and `?`, and `deny` wins over `allow`:

```json
"tools": {"deny": ["execute_luau"]},
"clients": [
  {"name": "reviewer", "token": "<token -new>", "tools": {"allow": ["list_roblox_studios", "get_studio_state", "script_*"]}}
]
```

`studiolink-host token -new` makes a client token. Agents don't see tools
they can't call in `tools/list`, and a call to one is answered with an error
without reaching Studio. The main `token` is the "owner": only it can read
`status`, and a session can only be used with the token that opened it.

## Audit log
`audit.jsonl` next to the config gets one JSON line per session opened or
closed, tool call (tool, client, result, duration) and refused request (bad
token or origin). It moves to `audit.jsonl.1` at 10 MB. Tool arguments are
left out unless `audit_arguments` is on, since they can hold whole scripts.
Set `audit_log` to another path, or to `"off"`.

## Status, tray and start at login
`studiolink-host status` shows a running bridge's addresses, relays and
sessions (`-json` for scripts). Only one bridge runs per config; a second
`serve` says which process already has it.

On Windows, `studiolink-host tray` runs the bridge in the background with an
icon in the notification area: blue while waiting, green while agents are
connected, amber when a relay is down. Its menu shows the sessions and relays
and opens the log (`studiolink.log` next to the config), the audit log and
the config folder. **Start at login** (or `studiolink-host autostart on|off`)
adds the tray to your Windows login items.

This is a login item rather than a Windows service on purpose: a service
runs in session 0, apart from your desktop, where it cannot see Studio or
your `%LOCALAPPDATA%\Roblox\mcp.bat`.

## How it behaves
- Each MCP session gets its own StudioMCP process, inside a Windows Job
  Object. Ending the session, an idle timeout (30 min) or the bridge crashing
  kills it and everything it started.
- Replies are plain JSON when quick. Slow tool calls switch to an SSE stream
  with keepalives every 15 s, so proxies and tunnels don't drop them.
- Progress and log messages Studio sends during a tool call arrive on that
  call's stream, before its result; anything else goes to the agent's GET
  stream.
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
| `relays[].url` | none | Relay to dial out to: `wss://host:44771` (`ws://` only to loopback) |
| `relays[].fingerprint` | none | The relay certificate's `sha256:` fingerprint; leave out for a certificate from a public CA |
| `tools.allow`, `tools.deny` | all allowed | Tool name patterns every agent is limited to |
| `clients[]` | none | Extra agents: `name`, `token` (32+ characters) and optional `tools` |
| `audit_log` | `audit.jsonl` next to the config | Audit log path, or `"off"` |
| `audit_arguments` | `false` | Record tool call arguments (up to 64 KB each) in the audit log |
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
