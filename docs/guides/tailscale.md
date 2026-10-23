# Agents on other machines on your tailnet

```powershell
studiolink-host connect tailscale
```

prints these steps with the bridge's tailnet address and your token.

## On Windows
The bridge joins your tailnet as its own device; Tailscale doesn't need to be
installed on the Studio PC, and no firewall rule is needed. Add to the config:

```json
"tailscale": {"hostname": "studiolink"}
```

then `studiolink-host stop` and `studiolink-host start`. The first time, the
log (tray menu: **Open log**) shows a login URL; open it to add the device.
To skip the browser, start the bridge with `TS_AUTHKEY` set to an auth key
from the Tailscale admin console.

## On the peer
Claude Code:

```bash
claude mcp add --transport http -s user Roblox_Studio http://studiolink.<tailnet>.ts.net/mcp --header "Authorization: Bearer <token>"
```

Stdio-only agents:

```bash
curl -fsSL http://studiolink.<tailnet>.ts.net/install.sh | sh
studiolink-client save -url http://studiolink.<tailnet>.ts.net/mcp
claude mcp add -s user Roblox_Studio -- studiolink-client connect
```

Tailscale encrypts the traffic. For HTTPS as well, set `"https": true` after
enabling HTTPS certificates in the Tailscale admin console.
