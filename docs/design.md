# StudioLink: design

## Problem
Roblox's Studio MCP server (`StudioMCP.exe`, launched by
`%LOCALAPPDATA%\Roblox\mcp.bat`) speaks MCP over stdio only and runs only on
the Windows machine with Studio open. AI agents running in VMs, Docker
containers, on Tailscale peers or on external servers cannot reach it.

## Architecture
```
Agent (VM / Docker / Tailscale peer / external server)
   │  Streamable HTTP + bearer token      (or stdio via studiolink-client)
   ▼
studiolink-host (Windows, next to Studio)
   │  one mcp.bat process per MCP session, in a Windows Job Object
   ▼
StudioMCP.exe ⇄ Roblox Studio
```

- **studiolink-host** serves `/mcp` over Streamable HTTP. Each MCP session gets its
  own StudioMCP process; the Job Object kills it if the bridge dies.
- **studiolink-client** is optional: a stdio shim for agents without HTTP MCP
  support, and the server end of dial-out mode.

## Connection modes
| Target | Mode | Host binds to | Agent connects to |
|---|---|---|---|
| Docker on this PC | Listen | `127.0.0.1` | `host.docker.internal:PORT` |
| Local VM / WSL2 | Listen | VM adapter IP | Host IP on that network |
| Tailscale | Listen (`tsnet`) | Its own tailnet node | `http://<node>.<tailnet>.ts.net/mcp` |
| External server | Dial-out | Nothing | `127.0.0.1:PORT` on the server |

Dial-out: studiolink-host opens one outbound TLS WebSocket to `studiolink-client relay` and
multiplexes sessions over it with yamux. Reconnects with backoff; heartbeats.

## Security
- Bearer token required on every request (`Authorization: Bearer` or
  `X-MCP-Auth`); `studiolink-host init` generates it. No token, no start.
- Loopback by default; other interfaces only when named. `0.0.0.0` needs an
  explicit override.
- TLS required for dial-out; Tailscale's encryption covers listen-over-tailnet.
- Optional tool allow/deny lists, for every agent and per client token (for
  example, no `execute_luau`). Denied tools are hidden from `tools/list` and
  refused before they reach Studio; sessions are bound to their token.
- Audit log (JSON lines) of sessions, tool calls and refused requests; tool
  arguments only when opted in.

## Phases
1. Scaffold (done): Go module, CLI skeleton, `doctor`, lint and test setup.
2. Listen mode (done): Streamable HTTP, process per session, Job Objects,
   token auth. Verified from Docker with MCP Inspector against live Studio.
3. Interfaces (done): adapter binding by name that follows address changes,
   `interfaces` command, Tailscale via `tsnet` with peer identity in the log.
   Verified against live Studio from WSL2 and over the tailnet.
4. studiolink-client stdio shim (done): `connect` forwards stdio to the
   bridge (concurrent calls, SSE, server messages via GET, batch splitting,
   DELETE on exit); `check` probes a bridge. Verified against live Studio
   from WSL2 and from MCP Inspector in Docker.
5. Dial-out mode (done): the host dials `studiolink-client relay` over a TLS
   WebSocket (certificate pinned by fingerprint, or a public CA) and serves
   yamux streams with the normal bridge handler; the relay forwards agents'
   HTTP through it. Reconnects with backoff. Verified against live Studio
   with the relay in Docker (probe, Inspector tool call, relay restart, wrong
   certificate refused).
6. Hardening (done): tool policy with per-client tokens, audit log,
   `status` with a single instance per config, tray icon and start at login.
   A login item (HKCU Run key) replaced the planned Windows service, because
   a service runs in session 0 and cannot reach the user's Studio or
   `mcp.bat`. Server messages sent during a call now stay on that call's
   stream. Verified against live Studio (policy, audit, status, tray).
7. Packaging: release binaries, client Docker image, per-scenario guides,
   migration configs for Yeetback and Prison Wars.

## Prior art
- Yeetback (`docker/claude/`): Python `mcp-proxy` over HTTP on 127.0.0.1:8765.
- Prison Wars (`tools/roblox-mcp/`): Node raw TCP pipe on 127.0.0.1:44800.
  Lesson: a bridge orphaned from its console broke connections; pipe child
  stderr and never let log writes kill the process.
- Chrrxs/robloxstudio-mcp: token file plus `Authorization`/`X-MCP-Auth` headers.
- mcptunnels, IBM Context Forge: outbound WebSocket tunnels with multiplexing.
- supergateway, stephenlacy/mcp-proxy: generic stdio to HTTP MCP proxies.
