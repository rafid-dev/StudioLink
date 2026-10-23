# Moving Prison Wars to StudioLink

Prison Wars bridges Studio MCP with its own Node scripts today:

```
claude (container) -> tools/roblox-mcp/client.js -> host.docker.internal:44800
  -> host-bridge.js (Windows, in an open console) -> StudioMCP.exe
```

With StudioLink, the bridge runs from the notification area (and at login),
needs no Node on Windows and no console left open, checks a token, and starts
a separate Studio MCP per session:

```
claude (container or Windows) -> studiolink-client connect -> studiolink-host -> mcp.bat
```

## Once, on Windows
Unzip the StudioLink release and double-click `studiolink-host.exe`. Setup
installs it to `%LOCALAPPDATA%\Programs\StudioLink`, puts `studiolink-host` and
`studiolink-client` on your PATH, starts it and offers to start it at login.

## Changes in the repo
Copy the files in this folder over the repo's:

| File | Change |
|---|---|
| `.mcp.json` | `roblox-studio` runs `studiolink-client connect` instead of `node tools/roblox-mcp/client.js` |
| `docker-compose.yml` | Mounts the Linux client and the bridge's token file into `dev` |

Then delete `tools/roblox-mcp/` (`client.js`, `host-bridge.js`,
`start-bridge.cmd`, `README.md`) and update the Studio MCP lines in
`docs/HANDOVER_MVP.md`. Recreate the container so it gets the new mounts:

```powershell
docker compose up -d --force-recreate dev
```

## How it finds everything
- **In the container**, `studiolink-client` sees `/.dockerenv` and connects to
  `host.docker.internal:44770`. The token comes from
  `/run/secrets/studiolink_token`, the bridge's own `studiolink.env` mounted
  read-only, so SSH sessions get it too (they don't inherit the container's
  environment). A new token reaches the container without a restart.
- **On Windows**, the same `.mcp.json` runs the installed
  `studiolink-client.exe`, which connects to `127.0.0.1:44770` and reads the
  token from `%APPDATA%\StudioLink\studiolink.env`.

Nothing in the repo holds the token.

## Check
```powershell
studiolink-host status
docker exec -u node prison-wars-dev studiolink-client check
```

The second line should print `ok http://host.docker.internal:44770/mcp` and
the number of Studio tools. Claude asks once to approve `roblox-studio` from
`.mcp.json`, as before.

## Notes
- The container mounts the `linux-amd64` client. On an ARM PC, change the
  mount to `studiolink-client-linux-arm64`.
- Port 44800 is no longer used.
