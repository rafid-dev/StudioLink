# Starts the claude-ssh container that Claude Desktop connects to (Code > SSH).
#
#   .\ssh-up.ps1
#
# Ensures Docker and the StudioLink bridge are up, builds and starts claude-ssh, then
# prints the host name to enter in Claude Desktop. sshd listens on 127.0.0.1:2222 only.

$ErrorActionPreference = 'Stop'

$here    = $PSScriptRoot
$compose = Join-Path $here 'compose.yaml'
$pubKey  = Join-Path $env:USERPROFILE '.ssh\yeetback_container.pub'

. (Join-Path $here 'ensure-studiolink.ps1')

cmd.exe /c "docker info >nul 2>&1"
if ($LASTEXITCODE -ne 0) {
    Write-Error 'Docker is not running. Start Docker Desktop and wait for it to finish starting, then re-run this script.'
    exit 1
}

if (-not (Test-Path -LiteralPath $pubKey)) {
    Write-Error "Client public key not found: $pubKey. Generate it with: ssh-keygen -t ed25519 -N '' -f `"$env:USERPROFILE\.ssh\yeetback_container`""
    exit 1
}

Assert-StudioLink

# docker writes progress to stderr; keep it from becoming a terminating error.
$ErrorActionPreference = 'Continue'
& docker compose -f $compose up -d --build claude-ssh
if ($LASTEXITCODE -ne 0) {
    Write-Error 'docker compose up failed for claude-ssh.'
    exit 1
}

$ErrorActionPreference = 'Stop'
$deadline = (Get-Date).AddSeconds(30)
while (-not (Get-NetTCPConnection -LocalPort 2222 -State Listen -ErrorAction SilentlyContinue)) {
    if ((Get-Date) -gt $deadline) {
        Write-Error 'claude-ssh is running but 127.0.0.1:2222 is not listening after 30 s. Check: docker compose logs claude-ssh'
        exit 1
    }
    Start-Sleep -Milliseconds 500
}

Write-Output ''
Write-Output 'claude-ssh is running. SSH: 127.0.0.1:2222 (loopback only).'
Write-Output 'In Claude Desktop: Code > SSH > connect to host  yeetback-docker'
Write-Output 'Repo inside the container: /workspace.   Stop it with: .\ssh-down.ps1'
