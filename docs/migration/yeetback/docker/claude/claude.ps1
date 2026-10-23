# One-command launcher: Claude Code in Docker, wired to the host's Roblox Studio MCP.
#
#   .\claude.ps1                     # interactive session
#   .\claude.ps1 -p "list scripts"   # extra args go straight to claude
#
# Any args are forwarded to `claude` (the flags are fixed in compose.yaml's entrypoint).
# No param block on purpose: it keeps flags like -p from being bound as PowerShell parameters.

$ErrorActionPreference = 'Stop'

$here      = $PSScriptRoot
$compose   = Join-Path $here 'compose.yaml'

. (Join-Path $here 'ensure-studiolink.ps1')

# Docker first, so a stopped Docker does not leave a bridge running for nothing.
cmd.exe /c "docker info >nul 2>&1"
if ($LASTEXITCODE -ne 0) {
    Write-Error 'Docker is not running. Start Docker Desktop and wait for it to finish starting, then re-run this script.'
    exit 1
}

Assert-StudioLink

# docker writes progress lines to stderr; with 'Stop' PowerShell would turn them into
# terminating errors, so relax the preference for the native calls only.
$ErrorActionPreference = 'Continue'

# --service-ports publishes the compose `ports:` (Rojo serve on 127.0.0.1:34872).
# claude-ssh publishes that same port, so skip --service-ports while it is running.
$sshRunning = [bool](& docker compose -f $compose ps --status running -q claude-ssh 2>$null)
$servicePorts = @()
if ($sshRunning) {
    Write-Output 'claude-ssh is running and holds 127.0.0.1:34872, so this session starts without the Rojo port.'
} else {
    $servicePorts = @('--service-ports')
}

& docker compose -f $compose run --rm @servicePorts claude @args
exit $LASTEXITCODE
