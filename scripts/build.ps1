param(
    [string]$OutputPath = "output\sing-box-dover-go.exe"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$OutputFile = [System.IO.Path]::GetFullPath($OutputPath, $Root)
$ResourceFile = Join-Path $Root "cmd\sing-box-drover\resource_windows_amd64.syso"
$OutputDirectory = Split-Path -Parent $OutputFile

Push-Location -LiteralPath $Root
try {
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    $env:GOTOOLCHAIN = "go1.25.6"
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
    Pop-Location
}
