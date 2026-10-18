param(
    [string]$OutputPath = "output\sing-box-drover-go.exe"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
# The two-argument overload is unavailable in Windows PowerShell 5.1.
if ([System.IO.Path]::IsPathRooted($OutputPath)) {
    $OutputFile = [System.IO.Path]::GetFullPath($OutputPath)
} else {
    $OutputFile = [System.IO.Path]::GetFullPath((Join-Path $Root $OutputPath))
}
$ResourceFile = Join-Path $Root "cmd\sing-box-drover\resource_windows_amd64.syso"
$OutputDirectory = Split-Path -Parent $OutputFile
$OverlayFile = Join-Path $Root "output\.go-overlay\overlay.json"
$BuildEnvironmentNames = @("GOTOOLCHAIN", "GOROOT", "GOOS", "GOARCH", "CGO_ENABLED", "GOFIPS140")
$PreviousBuildEnvironment = @{}
foreach ($name in $BuildEnvironmentNames) {
    $existing = Get-Item -Path ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
    $PreviousBuildEnvironment[$name] = if ($null -eq $existing) {
        [pscustomobject]@{ Exists = $false; Value = $null }
    } else {
        [pscustomobject]@{ Exists = $true; Value = $existing.Value }
    }
}
$LocationPushed = $false

try {
    Push-Location -LiteralPath $Root
    $LocationPushed = $true
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    $env:GOTOOLCHAIN = "go1.27.2"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"

    $GoEnvironment = @(& go env GOROOT GOMODCACHE)
    if ($LASTEXITCODE -ne 0 -or $GoEnvironment.Count -ne 2) {
        throw "Failed to locate the Go 1.27.2 toolchain"
    }
    $GoRoot = [System.IO.Path]::GetFullPath($GoEnvironment[0])
    $ModuleCache = [System.IO.Path]::GetFullPath($GoEnvironment[1]).TrimEnd([System.IO.Path]::DirectorySeparatorChar)
    if ($GoRoot.StartsWith($ModuleCache + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
        # Go rejects overlays beneath GOMODCACHE, including downloaded toolchains.
        $StandaloneRoot = Join-Path $Root "output\.toolchains\go1.27.2"
        if (-not (Test-Path -LiteralPath $StandaloneRoot)) {
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $StandaloneRoot) | Out-Null
            Copy-Item -LiteralPath $GoRoot -Destination $StandaloneRoot -Recurse
        }
        $GoRoot = $StandaloneRoot
    }
    $env:GOROOT = $GoRoot
    $env:GOTOOLCHAIN = "local"
    $GoBinary = Join-Path $GoRoot "bin\go.exe"

    # The generator is a host tool and must never be cross-compiled: GOOS/GOARCH
    # above target the program being built, and building the generator for that
    # target fails with "exec format error" unless it happens to match the host.
    $HostOS = & $GoBinary env GOHOSTOS
    $HostArch = & $GoBinary env GOHOSTARCH
    if ($LASTEXITCODE -ne 0 -or -not $HostOS -or -not $HostArch) {
        throw "Failed to determine the host platform"
    }
    $env:GOOS = $HostOS
    $env:GOARCH = $HostArch
    & $GoBinary run .\tools\go-overlay -goroot $GoRoot -output $OverlayFile
    if ($LASTEXITCODE -ne 0) {
        throw "Go memory workaround preparation failed"
    }
    # Restore the target platform for the actual build below.
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:GOFIPS140 = "off"

    & $GoBinary run github.com/akavel/rsrc@v0.10.2 `
        -arch amd64 `
        -ico resources\app.ico `
        -manifest resources\app.manifest `
        -o $ResourceFile
    if ($LASTEXITCODE -ne 0) {
        throw "Windows resource generation failed"
    }

    & $GoBinary build `
        -overlay $OverlayFile `
        -trimpath `
        -ldflags="-s -w -H=windowsgui" `
        -o $OutputFile `
        .\cmd\sing-box-drover
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed"
    }

    Write-Host "Build complete: $OutputFile"
}
finally {
    Remove-Item -LiteralPath $ResourceFile -Force -ErrorAction SilentlyContinue
    foreach ($name in $BuildEnvironmentNames) {
        $saved = $PreviousBuildEnvironment[$name]
        if ($saved.Exists) {
            Set-Item -Path ("Env:{0}" -f $name) -Value $saved.Value
        } else {
            Remove-Item -Path ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        }
    }
    if ($LocationPushed) {
        Pop-Location
    }
}
