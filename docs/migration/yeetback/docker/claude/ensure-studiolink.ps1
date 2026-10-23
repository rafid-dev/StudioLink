# Shared by claude.ps1 and ssh-up.ps1. Dot-source it, then call Assert-StudioLink.
# Starts the StudioLink bridge (studiolink-host) in the background unless it is
# already running. It also writes %APPDATA%\StudioLink\studiolink.env, which
# compose.yaml hands to the containers as STUDIOLINK_TOKEN.

function Find-StudioLinkHost {
    $cmd = Get-Command studiolink-host -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    # Installed by setup, but PATH not refreshed in this terminal yet.
    $installed = Join-Path $env:LOCALAPPDATA 'Programs\StudioLink\studiolink-host.exe'
    if (Test-Path -LiteralPath $installed) { return $installed }
    return $null
}

function Assert-StudioLink {
    $exe = Find-StudioLinkHost
    if (-not $exe) {
        throw 'StudioLink is not installed. Download the StudioLink zip, double-click studiolink-host.exe, then re-run this script.'
    }
    & $exe start
    if ($LASTEXITCODE -ne 0) {
        throw "studiolink-host start failed. See %APPDATA%\StudioLink\studiolink.log."
    }
}
