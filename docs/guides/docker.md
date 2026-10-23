# Agents in Docker on the Studio PC

```powershell
studiolink-host connect docker
```

prints these steps with your token and port filled in.

Containers reach the Studio PC's loopback as `host.docker.internal` (Docker
Desktop; on Linux engines add `extra_hosts: ["host.docker.internal:host-gateway"]`).
The bridge serves `127.0.0.1:44770` by default, so nothing else is needed.

## Without copying the token
The bridge keeps `%APPDATA%\StudioLink\studiolink.env` current
(`STUDIOLINK_TOKEN=...`). Hand it to the container:

```yaml
services:
  agent:
    env_file:
      - ${APPDATA}/StudioLink/studiolink.env
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

or `docker run --env-file "%APPDATA%\StudioLink\studiolink.env" ...`.

Claude Code fills in environment variables in `.mcp.json`:

```json
{"mcpServers": {"Roblox_Studio": {"type": "http",
  "url": "http://host.docker.internal:44770/mcp",
  "headers": {"Authorization": "Bearer ${STUDIOLINK_TOKEN}"}}}}
```

## SSH into the container
Sessions started over SSH don't get the container's environment. Mount the
token file instead; `studiolink-client` reads `/run/secrets/studiolink_token`:

```yaml
    volumes:
      - type: bind
        source: ${APPDATA}/StudioLink/studiolink.env
        target: /run/secrets/studiolink_token
        read_only: true
```

and use the stdio client below.

## Stdio-only agents
Install the client in the container (from the bridge itself, no other
download):

```bash
curl -fsSL http://host.docker.internal:44770/install.sh | sh
studiolink-client check
```

or mount the one the Studio PC already has:
`${LOCALAPPDATA}/Programs/StudioLink/clients/studiolink-client-linux-amd64`
at `/usr/local/bin/studiolink-client`. The agent's command is then
`studiolink-client connect`, with no flags: it finds `host.docker.internal`
by itself and reads the token from `STUDIOLINK_TOKEN` or the mounted file.

[migration/prison-wars](../migration/prison-wars/README.md) is a complete
example.
