$ErrorActionPreference = "Stop"
$sourceRoot = $PSScriptRoot
$packageRoot = Split-Path -Parent $sourceRoot
$previousGOOS = $env:GOOS
$previousGOARCH = $env:GOARCH

try {
    foreach ($target in @("windows-amd64", "linux-amd64", "darwin-amd64", "darwin-arm64")) {
        $parts = $target.Split("-")
        $env:GOOS = $parts[0]
        $env:GOARCH = $parts[1]
        $executable = if ($env:GOOS -eq "windows") { "showcase.exe" } else { "showcase" }
        $destination = Join-Path $packageRoot "backend\$target\$executable"
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
        go build -C $sourceRoot -trimpath -o $destination .
        if ($LASTEXITCODE -ne 0) { throw "Go build failed for $target" }
    }
} finally {
    $env:GOOS = $previousGOOS
    $env:GOARCH = $previousGOARCH
}
