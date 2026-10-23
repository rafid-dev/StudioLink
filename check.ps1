# Runs every check CI would: formatting, vet, staticcheck, tests, and a
# cross-compile of both commands for each target platform.
#
#   .\check.ps1

$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    function Invoke-Step([string]$Name, [scriptblock]$Body) {
        Write-Output "== $Name"
        & $Body
        if ($LASTEXITCODE -ne 0) { throw "$Name failed" }
    }

    Invoke-Step 'gofmt' {
        $unformatted = gofmt -l .
        if ($unformatted) {
            Write-Output "Not gofmt-ed:`n$unformatted"
            $global:LASTEXITCODE = 1
        }
    }
    Invoke-Step 'go vet' { go vet ./... }
    Invoke-Step 'staticcheck' { go tool staticcheck ./... }
    # No -race: on Windows it needs cgo and a C compiler.
    Invoke-Step 'go test' { go test ./... }

    # studiolink-host only targets Windows (Studio's platform); studiolink-client goes
    # wherever agents run.
    $targets = @(
        @{ Os = 'windows'; Arch = 'amd64'; Pkgs = @('./cmd/studiolink-host', './cmd/studiolink-client') },
        @{ Os = 'windows'; Arch = 'arm64'; Pkgs = @('./cmd/studiolink-host', './cmd/studiolink-client') },
        @{ Os = 'linux';   Arch = 'amd64'; Pkgs = @('./cmd/studiolink-client') },
        @{ Os = 'linux';   Arch = 'arm64'; Pkgs = @('./cmd/studiolink-client') },
        @{ Os = 'darwin';  Arch = 'arm64'; Pkgs = @('./cmd/studiolink-client') }
    )
    # Binaries go to a throwaway folder so none land in the repo.
    $outDir = Join-Path ([IO.Path]::GetTempPath()) 'studiolink-check'
    foreach ($t in $targets) {
        Invoke-Step "build $($t.Os)/$($t.Arch)" {
            $env:GOOS = $t.Os; $env:GOARCH = $t.Arch; $env:CGO_ENABLED = '0'
            go build -o "$outDir$([IO.Path]::DirectorySeparatorChar)" @($t.Pkgs)
        }
    }
    Remove-Item -Recurse -Force $outDir -ErrorAction SilentlyContinue
    Write-Output 'All checks passed.'
}
finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
