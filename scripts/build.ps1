param(
    [string]$OutputPath = "output\sing-box-dover-go.exe"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$OutputFile = [System.IO.Path]::GetFullPath($OutputPath, $Root)
$ResourceFile = Join-Path $Root "cmd\sing-box-drover\resource_windows_amd64.syso"
$OutputDirectory = Split-Path -Parent $OutputFile
$BuildEnvironmentNames = @("GOTOOLCHAIN", "GOOS", "GOARCH", "CGO_ENABLED")
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
    $env:GOTOOLCHAIN = "go1.25.14"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"

    & go run github.com/akavel/rsrc@v0.10.2 `
        -arch amd64 `
        -ico resources\app.ico `
        -manifest resources\app.manifest `
        -o $ResourceFile
    if ($LASTEXITCODE -ne 0) {
        throw "Windows resource generation failed"
    }

    & go build `
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
