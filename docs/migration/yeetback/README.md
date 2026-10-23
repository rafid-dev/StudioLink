# Moving Yeetback to StudioLink

Yeetback bridges Studio MCP with a Python `mcp-proxy` today:
`ensure-mcp-bridge.ps1` builds a venv under `%LOCALAPPDATA%\yeetback-mcp-bridge`
and starts `start-mcp-bridge.ps1` hidden on `127.0.0.1:8765`, without a token.

With StudioLink, `studiolink-host start` replaces all of that: no Python, a
token on every request, the tray and start at login, and a separate Studio MCP
per session.

## Once, on Windows
Unzip the StudioLink release and double-click `studiolink-host.exe`. Setup
installs it to `%LOCALAPPDATA%\Programs\StudioLink`, puts it on your PATH,
starts it and offers to start it at login.

## Changes in the repo
Copy the files under `docker/claude/` here over the repo's:

| File | Change |
|---|---|
| `mcp.json` | Port 44770, and `Authorization: Bearer ${STUDIOLINK_TOKEN}`; Claude Code fills in the variable |
| `compose.yaml` | Both services load `${APPDATA}/StudioLink/studiolink.env` with `env_file`, which sets `STUDIOLINK_TOKEN` |
| `ensure-studiolink.ps1` | New: `Assert-StudioLink` runs `studiolink-host start` (it does nothing if the bridge is up) |
| `claude.ps1`, `ssh-up.ps1` | Dot-source `ensure-studiolink.ps1` and call `Assert-StudioLink` |
| `register-studio-mcp.sh` | Registers the user-scope entry with the token header, replacing it on every container start so a new token takes effect |

Then delete `ensure-mcp-bridge.ps1` and `start-mcp-bridge.ps1`, update the
bridge line in `docs/ui/revamp/HANDOVER.md` (`host.docker.internal:44770`,
through StudioLink), and recreate `claude-ssh`:

```powershell
.\docker\claude\ssh-up.ps1
```

`%LOCALAPPDATA%\yeetback-mcp-bridge` (the old venv) can be deleted. The image
doesn't change.

## Check
```powershell
studiolink-host status
.\docker\claude\claude.ps1 -p "Call list_roblox_studios and print the result."
```

## Notes
- `env_file` needs the file to exist; `Assert-StudioLink` runs before Docker
  Compose, and the bridge writes the file when it starts.
- The token is in `~/.claude.json` in the `claude-home` volume for SSH
  sessions, as Claude Code keeps user-scope MCP entries there. The volume is
  private to this PC's Docker.
- After `studiolink-host token`'s token changes (a new config), run
  `ssh-up.ps1` again so `claude-ssh` picks it up; `claude.ps1` sessions pick
  it up on their own.
