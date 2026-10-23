# StudioLink

Lets AI agents in VMs, Docker containers, on Tailscale peers or on external
servers use Roblox Studio's MCP server, which normally only works on the
Windows machine running Studio.

**Status:** phase 1 (scaffold). Only `studiolink-host doctor` and `version` work.
See [docs/design.md](docs/design.md) for the architecture and roadmap.

## Commands
- `studiolink-host` runs on Windows next to Studio.
- `studiolink-client` runs next to the agent.

## Development
Requires Go 1.27+.

```powershell
go run ./cmd/studiolink-host doctor   # check that mcp.bat is found
.\check.ps1                           # gofmt, vet, staticcheck, tests, cross-builds
```
