# Agents in WSL2

```powershell
studiolink-host connect wsl
```

prints these steps for your setup.

## On Windows
Setup asks "WSL2 is installed. Let its distros use Studio?" when it finds the
WSL adapter; say yes. Otherwise add the adapter from `studiolink-host interfaces`
to `listen_interfaces` and restart the bridge (`studiolink-host stop`,
`studiolink-host start`).

In mirrored networking mode (`networkingMode=mirrored` in `%USERPROFILE%\.wslconfig`)
distros share Windows' loopback, and the default `127.0.0.1:44770` is enough.

## In the distro
```bash
curl -fsSL http://$(ip route show default | awk '{print $3}'):44770/install.sh | sh
studiolink-client check
claude mcp add -s user Roblox_Studio -- studiolink-client connect
```

(`studiolink-host connect wsl` prints the install line with the right address.)

There is nothing to configure: `studiolink-client` tries `127.0.0.1` and then
the default gateway (Windows, in NAT mode), which follows the address WSL2
gets after each reboot, and reads the token from the bridge's own
`/mnt/c/Users/<you>/AppData/Roaming/StudioLink/studiolink.env`.

If several Windows users have StudioLink, the client can't tell which file is
yours; run `studiolink-client save` once and paste the token.
