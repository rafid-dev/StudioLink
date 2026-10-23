# Agents on a VPS or cloud server (dial-out)

```powershell
studiolink-host connect server
```

prints these steps with your token and the client's path filled in.

A server usually can't reach the Studio PC, so the PC dials out: a relay on
the server accepts a TLS tunnel from the PC, and agents on the server connect
to the relay locally. The PC opens no port. The server needs TCP 44771 open.

## 1. Start the relay on the server
Either with the binary (`clients\studiolink-client-linux-amd64` in the
StudioLink folder, or from the release):

```bash
chmod +x studiolink-client && sudo mv studiolink-client /usr/local/bin/
STUDIOLINK_TOKEN=<token> studiolink-client relay
```

or with Docker, from an image built with this repo's `Dockerfile`
(`docker build -t studiolink-client .` in the repo):

```bash
printf 'STUDIOLINK_TOKEN=%s\n' '<token>' > studiolink.env && chmod 600 studiolink.env
docker run -d --name studiolink-relay --restart unless-stopped \
  -p 44771:44771 -p 127.0.0.1:44770:44770 \
  -v studiolink-relay:/data --env-file studiolink.env studiolink-client
docker logs studiolink-relay
```

The relay makes a self-signed certificate on first start (kept in
`~/.config/StudioLink`, or the `/data` volume) and prints a command with its
fingerprint:

```
studiolink-host add-relay wss://<this server>:44771 sha256:...
```

## 2. Point the Studio PC at it
Run that command on the Studio PC with the server's address in it. It adds the
relay to the config and restarts the bridge, which then dials out and
reconnects on its own when the tunnel drops. The tray turns amber while a
relay is down.

The PC only connects to a relay whose certificate matches the fingerprint, so
it never sends the token anywhere else.

## 3. Connect agents on the server
```bash
curl -fsS http://127.0.0.1:44770/healthz       # ok: studiolink-host connected from ...
claude mcp add --transport http -s user Roblox_Studio http://127.0.0.1:44770/mcp --header "Authorization: Bearer <token>"
```

Stdio-only agents:

```bash
studiolink-client save          # asks for the token, checks it, saves it
claude mcp add -s user Roblox_Studio -- studiolink-client connect
```

## Run the relay as a service
With systemd, once step 3's `studiolink-client save` has stored the token
(run the unit as the same user):

```ini
# /etc/systemd/system/studiolink-relay.service
[Unit]
Description=StudioLink relay
After=network-online.target
Wants=network-online.target

[Service]
User=<you>
ExecStart=/usr/local/bin/studiolink-client relay
Restart=always

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now studiolink-relay
```

## With a domain
With a certificate from a public CA, start the relay with `-cert` and `-key`
(renewals are picked up without a restart) and run `add-relay` without a
fingerprint. Behind a TLS-terminating reverse proxy on the same server, use
`-plain-tunnel -tunnel-listen 127.0.0.1:44771` and proxy WebSocket upgrades on
`/tunnel` to it.
