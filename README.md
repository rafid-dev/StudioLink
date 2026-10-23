# StudioLink

Lets AI agents in VMs, Docker containers, on Tailscale peers or on external
servers use Roblox Studio's MCP server, which normally only works on the
Windows machine running Studio.

**Status:** phase 7. Agents in local Docker containers, WSL2 and local VMs,
and on Tailscale peers can connect over HTTP with a token, directly or through
`studiolink-client` for agents that only run stdio MCP servers. Agents on
external servers connect through a relay that the Studio PC dials out to.
Tools can be limited per agent, every session and tool call is audited, and
the bridge runs from the notification area and can start at login. See
[docs/design.md](docs/design.md) for the architecture and roadmap.

## Quick start
On the Windows PC with Roblox Studio:

1. Download `StudioLink-<version>-windows-amd64.zip` from the
   [latest release](https://github.com/rafid-dev/StudioLink/releases/latest),
   unzip it and double-click
   `studiolink-host.exe`. Setup installs it to
   `%LOCALAPPDATA%\Programs\StudioLink`, puts it on your PATH, makes a config
   with a new token, offers WSL2 access and start at login, and starts the
   bridge in the notification area. Run it again any time; it keeps your
   config and token.
2. In a new terminal, print the commands for where your agent runs, with
   your address and token filled in:

```powershell
studiolink-host connect            # lists: docker, wsl, vm, tailscale, server
studiolink-host connect docker
```

For Claude Code in a container on this PC that is one line:

```bash
claude mcp add --transport http -s user Roblox_Studio http://host.docker.internal:44770/mcp --header "Authorization: Bearer <token>"
```

Guides: [Docker](docs/guides/docker.md), [WSL2](docs/guides/wsl.md),
[VMs](docs/guides/vm.md), [Tailscale](docs/guides/tailscale.md),
[external servers](docs/guides/server.md). Moving an existing game:
[Yeetback](docs/migration/yeetback/README.md),
[Prison Wars](docs/migration/prison-wars/README.md).

`studiolink-host setup -yes` does the same without questions, for scripts.
Its `-no-install`, `-no-path`, `-no-autostart` and `-no-start` flags skip
steps. `studiolink-host start` starts the bridge unless it is running (for
scripts that need it), and `studiolink-host stop` stops it.

## The token, without copying it
The bridge writes `%APPDATA%\StudioLink\studiolink.env` (`STUDIOLINK_TOKEN=...`)
and keeps it current. Containers load it with `env_file` or
`docker run --env-file`, and Claude Code fills `${STUDIOLINK_TOKEN}` into
`.mcp.json` headers. `studiolink-client` reads it directly on the Studio PC,
from `/mnt/c/...` in WSL2, or mounted at `/run/secrets/studiolink_token`.

## Agents that only run stdio MCP servers
`studiolink-client` runs next to the agent (Linux, macOS or Windows) and
gives it an ordinary stdio MCP server that forwards to the bridge. Install it
from the bridge itself:

```bash
curl -fsSL http://<bridge>:44770/install.sh | sh
studiolink-client check                  # ok ... RobloxStudio 1.0.0, MCP 2025-06-18, 28 tools
claude mcp add -s user Roblox_Studio -- studiolink-client connect
codex mcp add Roblox_Studio -- studiolink-client connect   # Codex
```

Release pages also have each client on its own
(`studiolink-client-linux-amd64` and so on).

With no flags, the client looks for the bridge on `host.docker.internal` in a
container, on Windows' side of WSL2 (loopback, then the default gateway) and
on `127.0.0.1`. It takes the token from `-token-file`, `STUDIOLINK_TOKEN`,
`STUDIOLINK_TOKEN_FILE`, `/run/secrets/studiolink_token`, its saved
`studiolink.env`, and in WSL2 the bridge's own file. Elsewhere (a VM, a
tailnet peer), run once:

```bash
studiolink-client save -url http://<bridge>:44770/mcp   # asks for the token, checks it, saves it
```

`-url` and `STUDIOLINK_URL` still work. There is no `-token` flag, because
command lines are visible to other processes. When the agent stops, the
client ends its session on the bridge, so Studio MCP processes don't linger
until the idle timeout. It warns if the token would cross the internet
unencrypted (plain `http://` to a public address).

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

Restart the bridge (`studiolink-host stop`, `studiolink-host start`). The
first time, the log (tray menu: **Open log**) prints a login URL; open it to add the device (or
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
`~/.config/StudioLink`) and prints the command to run on the Studio PC,
including the certificate's fingerprint (`studiolink-client fingerprint`
prints it again). It adds the relay to the config and restarts the bridge:

```powershell
studiolink-host add-relay wss://vps.example.com:44771 sha256:...
```

The relay also runs in Docker; see [docs/guides/server.md](docs/guides/server.md).

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
.\release.ps1 -Version v0.7.0         # dist\: host zips with every client, client binaries, SHA256SUMS
docker build -t studiolink-client .   # the client image (runs the relay by default)
```

CI runs `check.ps1` on Windows and the tests on Linux. Pushing a `v*` tag
(`git tag v0.7.1; git push origin v0.7.1`) runs `release.ps1` on GitHub
Actions and publishes the release.

A `go build` of the host has no `clients` folder next to it, so it can't
serve `/install.sh`; `connect` says how to build the client instead.

## License
[MIT](LICENSE)
