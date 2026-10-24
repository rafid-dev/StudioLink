# Agents in local VMs (Hyper-V, VMware, VirtualBox)

## On Windows
Open Settings from the tray icon (**Settings…**) or with
`studiolink-host settings`. Under **Where agents can connect**, tick the
adapter your VMs use (Hyper-V, VMware NAT or VirtualBox) and apply. The bridge
restarts by itself and follows the adapter's address when it changes.

Windows Firewall may block the VM. Allow `studiolink-host.exe` on that
network (Windows asks the first time; choose Private networks only).

To set it by hand instead, `studiolink-host interfaces` lists the adapters and
what each connects to. Add the one your VMs use, by name, to the config
(`%APPDATA%\StudioLink\studiolink.json`):

```json
"listen_interfaces": [{"name": "vEthernet (Default Switch)", "port": 44770}]
```

then `studiolink-host stop` and `studiolink-host start`.

## In the VM
Install the client from the bridge, then run setup:

```bash
curl -fsSL http://<host>:44770/install.sh | sh
studiolink-client setup
```

`setup` finds the bridge at the host's address, or asks for it (the settings
page shows it). It asks for the token without showing it, checks the token
against the bridge and saves it. It adds Studio to Claude Code or Codex when
they are installed.

On a Windows VM, download
`http://<host>:44770/download/studiolink-client-windows-amd64.exe` and
double-click it. That runs setup too.

## Manual alternative
Claude Code:

```bash
claude mcp add --transport http -s user Roblox_Studio http://<host>:44770/mcp --header "Authorization: Bearer <token>"
```

Stdio-only agents, such as Codex:

```bash
curl -fsSL http://<host>:44770/install.sh | sh
studiolink-client save -url http://<host>:44770/mcp    # asks for the token, checks it, saves it
codex mcp add Roblox_Studio -- studiolink-client connect
```

or in `~/.codex/config.toml`:

```toml
[mcp_servers.Roblox_Studio]
command = "studiolink-client"
args = ["connect"]
```

If the VM can't reach the bridge's `/install.sh`, download the client from
the [latest release](https://github.com/rafid-dev/StudioLink/releases/latest)
instead (`studiolink-client-linux-amd64`, or `-linux-arm64`):

```bash
curl -fsSLo studiolink-client https://github.com/rafid-dev/StudioLink/releases/latest/download/studiolink-client-linux-amd64
chmod +x studiolink-client && sudo mv studiolink-client /usr/local/bin/
```

`save` keeps the URL and token in `~/.config/StudioLink/studiolink.env`
(readable by you only), so the agent's command needs no flags or
environment. On a Windows VM, download
`http://<host>:44770/download/studiolink-client-windows-amd64.exe` instead.

For the same commands with your addresses and token filled in, run
`studiolink-host connect vm` on the Studio PC.
