# Builds a release into dist\:
#
#   StudioLink-<version>-windows-<arch>.zip   studiolink-host.exe, every client
#                                             build in clients\, README.md
#   studiolink-client-<os>-<arch>[.exe]       the client on its own, for servers
#   SHA256SUMS
#
#   .\release.ps1                  # version from git describe
#   .\release.ps1 -Version v0.7.0
#
# The host serves the clients in its zip to agents (curl .../install.sh | sh),
# so a release needs no other download on the agent's side.

param([string]$Version)

$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    if (-not $Version) {
        $Version = (git describe --tags --always --dirty 2>$null)
        if (-not $Version) { $Version = 'dev' }
    }
    $dist = Join-Path $PSScriptRoot 'dist'
    Remove-Item -Recurse -Force $dist -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force (Join-Path $dist 'clients') | Out-Null
    $ldflags = "-s -w -X studiolink/internal/version.Version=$Version"

    function Build([string]$Os, [string]$Arch, [string]$Pkg, [string]$Out) {
        $env:GOOS = $Os; $env:GOARCH = $Arch; $env:CGO_ENABLED = '0'
        go build -trimpath -ldflags $ldflags -o $Out $Pkg
        if ($LASTEXITCODE -ne 0) { throw "build $Pkg for $Os/$Arch failed" }
    }

    $clients = @('linux-amd64', 'linux-arm64', 'darwin-arm64', 'darwin-amd64', 'windows-amd64', 'windows-arm64')
    foreach ($p in $clients) {
        $os, $arch = $p -split '-'
        $name = "studiolink-client-$p"
        if ($os -eq 'windows') { $name += '.exe' }
        Write-Output "== client $p"
        Build $os $arch './cmd/studiolink-client' (Join-Path $dist "clients\$name")
    }

    foreach ($arch in @('amd64', 'arm64')) {
        Write-Output "== host windows/$arch"
        $stage = Join-Path $dist "stage-$arch\StudioLink"
        New-Item -ItemType Directory -Force $stage | Out-Null
        Build 'windows' $arch './cmd/studiolink-host' (Join-Path $stage 'studiolink-host.exe')
        Copy-Item -Recurse (Join-Path $dist 'clients') (Join-Path $stage 'clients')
        Copy-Item README.md $stage
        $zip = Join-Path $dist "StudioLink-$Version-windows-$arch.zip"
        Compress-Archive -Path $stage -DestinationPath $zip
        Remove-Item -Recurse -Force (Split-Path $stage)
    }

    Move-Item (Join-Path $dist 'clients\*') $dist
    Remove-Item (Join-Path $dist 'clients')

    $sums = Get-ChildItem $dist -File | Sort-Object Name | ForEach-Object {
        "$((Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower())  $($_.Name)"
    }
    # LF line endings, as sha256sum -c expects.
    [IO.File]::WriteAllText((Join-Path $dist 'SHA256SUMS'), (($sums -join "`n") + "`n"))

    Write-Output "Release $Version in $dist"
    Get-ChildItem $dist | Format-Table Name, Length -AutoSize | Out-String | Write-Output
}
finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
