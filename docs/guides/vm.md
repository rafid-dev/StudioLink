# Agents in local VMs (Hyper-V, VMware, VirtualBox)

```powershell
studiolink-host connect vm
```

prints these steps with your addresses and token filled in.

## On Windows
`studiolink-host interfaces` lists the adapters and what each connects to.
Add the one your VMs use, by name, to the config
(`%APPDATA%\StudioLink\studiolink.json`):

```json
"listen_interfaces": [{"name": "vEthernet (Default Switch)", "port": 44770}]
```

then `studiolink-host stop` and `studiolink-host start`. The bridge follows the
adapter's address when it changes.

Windows Firewall may block the VM. Allow `studiolink-host.exe` on that
network (Windows asks the first time; choose Private networks only).

## In the VM
Claude Code:

```bash
claude mcp add --transport http -s user Roblox_Studio http://<host>:44770/mcp --header "Authorization: Bearer <token>"
```

Stdio-only agents:

```bash
curl -fsSL http://<host>:44770/install.sh | sh
studiolink-client save -url http://<host>:44770/mcp    # asks for the token, checks it, saves it
claude mcp add -s user Roblox_Studio -- studiolink-client connect
```

`save` keeps the URL and token in `~/.config/StudioLink/studiolink.env`
(readable by you only), so the agent's command needs no flags or
environment. On a Windows VM, download
`http://<host>:44770/download/studiolink-client-windows-amd64.exe` instead.
