param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $EchoArguments
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version 2.0

$RepositoryRoot = Split-Path -Parent $PSScriptRoot
$ToolsRoot = Join-Path $RepositoryRoot ".echo-tools"
$GoRoot = Join-Path $ToolsRoot "go"
$NodeRoot = Join-Path $ToolsRoot "node"
$NpmCache = Join-Path $ToolsRoot "npm-cache"
$GoCache = Join-Path $ToolsRoot "go-cache"
$GoModuleCache = Join-Path $ToolsRoot "go-mod-cache"
$GoTemp = Join-Path $ToolsRoot "go-tmp"
$BuildRoot = Join-Path $RepositoryRoot "build\bin"
$EchoBinary = Join-Path $BuildRoot "echo.exe"

function Write-Step([string] $Message) {
    Write-Host ""
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Test-MinimumVersion([string] $Version, [int] $MinimumMajor, [int] $MinimumMinor) {
    if ($Version -notmatch "^(?:go|v)?(?<major>\d+)\.(?<minor>\d+)") {
        return $false
    }

    $major = [int] $Matches.major
    $minor = [int] $Matches.minor
    return $major -gt $MinimumMajor -or ($major -eq $MinimumMajor -and $minor -ge $MinimumMinor)
}

function Get-GoVersion {
    if (-not (Get-Command "go.exe" -ErrorAction SilentlyContinue)) {
        return $null
    }

    $output = & go.exe version 2>$null
    if ($LASTEXITCODE -ne 0 -or "$output" -notmatch "\bgo(?<version>\d+\.\d+(?:\.\d+)?)\b") {
        return $null
    }
    return $Matches.version
}

function Get-NodeVersion {
    if (-not (Get-Command "node.exe" -ErrorAction SilentlyContinue)) {
        return $null
    }

    $output = & node.exe --version 2>$null
    if ($LASTEXITCODE -ne 0 -or "$output" -notmatch "^v?(?<version>\d+\.\d+(?:\.\d+)?)") {
        return $null
    }
    return $Matches.version
}

function Invoke-Checked([scriptblock] $Command, [string] $FailureMessage) {
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$FailureMessage (exit code $LASTEXITCODE)."
    }
}

function Install-PortableGo([string] $Architecture) {
    Write-Step "Installing a portable copy of Go 1.26 or newer"
    $tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("echo-go-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tempRoot | Out-Null

    try {
        $versionResponse = (Invoke-WebRequest -UseBasicParsing -Uri "https://go.dev/VERSION?m=text").Content
        $goTag = (($versionResponse -split "`n")[0]).Trim()
        if ($goTag -notmatch "^go(?<version>\d+\.\d+(?:\.\d+)?)$") {
            throw "go.dev returned an unexpected current version: $goTag"
        }
        if (-not (Test-MinimumVersion $Matches.version 1 26)) {
            throw "The current Go release ($($Matches.version)) is older than Echo's required Go 1.26."
        }

        $archiveName = "$goTag.windows-$Architecture.zip"
        $archivePath = Join-Path $tempRoot $archiveName
        $checksumUri = "https://go.dev/dl/$archiveName.sha256"
        Invoke-WebRequest -UseBasicParsing -Uri "https://go.dev/dl/$archiveName" -OutFile $archivePath
        $expectedHash = ((Invoke-WebRequest -UseBasicParsing -Uri $checksumUri).Content).Trim().ToLowerInvariant()
        $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $expectedHash) {
            throw "The downloaded Go archive failed its SHA-256 verification."
        }

        $extractRoot = Join-Path $tempRoot "extract"
        Expand-Archive -LiteralPath $archivePath -DestinationPath $extractRoot
        $extractedGo = Join-Path $extractRoot "go"
        if (-not (Test-Path -LiteralPath (Join-Path $extractedGo "bin\go.exe") -PathType Leaf)) {
            throw "The downloaded Go archive did not contain bin\go.exe."
        }

        New-Item -ItemType Directory -Force -Path $ToolsRoot | Out-Null
        if (Test-Path -LiteralPath $GoRoot) {
            Remove-Item -LiteralPath $GoRoot -Recurse -Force
        }
        Move-Item -LiteralPath $extractedGo -Destination $GoRoot
    }
    finally {
        if (Test-Path -LiteralPath $tempRoot) {
            Remove-Item -LiteralPath $tempRoot -Recurse -Force
        }
    }
}

function Install-PortableNode([string] $Architecture) {
    Write-Step "Installing a portable copy of Node.js 22 with npm"
    $tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("echo-node-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tempRoot | Out-Null

    try {
        $releases = @(Invoke-RestMethod -Uri "https://nodejs.org/dist/index.json")
        $release = @($releases | Where-Object { $_.version -match "^v22\." } | Select-Object -First 1)
        if ($release.Count -eq 0) {
            throw "nodejs.org did not list a Node.js 22 release."
        }

        $version = [string] $release[0].version
        $archiveName = "node-$version-win-$Architecture.zip"
        $baseUri = "https://nodejs.org/dist/$version"
        $archivePath = Join-Path $tempRoot $archiveName
        Invoke-WebRequest -UseBasicParsing -Uri "$baseUri/$archiveName" -OutFile $archivePath

        $checksumLines = ((Invoke-WebRequest -UseBasicParsing -Uri "$baseUri/SHASUMS256.txt").Content -split "`n")
        $escapedArchiveName = [regex]::Escape($archiveName)
        $checksumLine = @($checksumLines | Where-Object { $_ -match "^(?<hash>[0-9a-fA-F]{64})\s+\*?$escapedArchiveName\s*$" } | Select-Object -First 1)
        if ($checksumLine.Count -eq 0) {
            throw "Could not find $archiveName in Node.js's checksum file."
        }
        $null = $checksumLine[0] -match "^(?<hash>[0-9a-fA-F]{64})"
        $expectedHash = $Matches.hash.ToLowerInvariant()
        $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $expectedHash) {
            throw "The downloaded Node.js archive failed its SHA-256 verification."
        }

        $extractRoot = Join-Path $tempRoot "extract"
        Expand-Archive -LiteralPath $archivePath -DestinationPath $extractRoot
        $extractedNode = Join-Path $extractRoot "node-$version-win-$Architecture"
        if (-not (Test-Path -LiteralPath (Join-Path $extractedNode "node.exe") -PathType Leaf)) {
            throw "The downloaded Node.js archive did not contain node.exe."
        }

        New-Item -ItemType Directory -Force -Path $ToolsRoot | Out-Null
        if (Test-Path -LiteralPath $NodeRoot) {
            Remove-Item -LiteralPath $NodeRoot -Recurse -Force
        }
        Move-Item -LiteralPath $extractedNode -Destination $NodeRoot
    }
    finally {
        if (Test-Path -LiteralPath $tempRoot) {
            Remove-Item -LiteralPath $tempRoot -Recurse -Force
        }
    }
}

try {
    if ([System.Environment]::Is64BitOperatingSystem -eq $false) {
        throw "Echo requires a 64-bit version of Windows."
    }

    $runtimeArchitecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    switch ($runtimeArchitecture) {
        "X64" { $downloadArchitecture = "amd64"; $nodeArchitecture = "x64" }
        "Arm64" { $downloadArchitecture = "arm64"; $nodeArchitecture = "arm64" }
        default { throw "Unsupported Windows architecture: $runtimeArchitecture" }
    }

    # Prefer portable tools previously installed by this launcher, then fall back to PATH.
    $env:PATH = "$(Join-Path $GoRoot 'bin');$NodeRoot;$env:PATH"
    New-Item -ItemType Directory -Force -Path $NpmCache, $GoCache, $GoModuleCache, $GoTemp | Out-Null
    $env:npm_config_cache = $NpmCache
    $env:GOCACHE = $GoCache
    $env:GOMODCACHE = $GoModuleCache
    $env:GOTMPDIR = $GoTemp
    [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12

    $goVersion = Get-GoVersion
    if (-not $goVersion -or -not (Test-MinimumVersion $goVersion 1 26)) {
        Install-PortableGo $downloadArchitecture
        $goVersion = Get-GoVersion
    }
    if (-not $goVersion -or -not (Test-MinimumVersion $goVersion 1 26)) {
        throw "Go 1.26 or newer is unavailable after installation."
    }
    Write-Host "Using Go $goVersion"

    $nodeVersion = Get-NodeVersion
    $npmAvailable = [bool] (Get-Command "npm.cmd" -ErrorAction SilentlyContinue)
    if (-not $nodeVersion -or -not (Test-MinimumVersion $nodeVersion 22 0) -or -not $npmAvailable) {
        Install-PortableNode $nodeArchitecture
        $nodeVersion = Get-NodeVersion
        $npmAvailable = [bool] (Get-Command "npm.cmd" -ErrorAction SilentlyContinue)
    }
    if (-not $nodeVersion -or -not (Test-MinimumVersion $nodeVersion 22 0) -or -not $npmAvailable) {
        throw "Node.js 22 or newer with npm is unavailable after installation."
    }
    Write-Host "Using Node.js $nodeVersion"

    Write-Step "Installing frontend packages"
    Push-Location (Join-Path $RepositoryRoot "web")
    try {
        Invoke-Checked { & npm.cmd ci } "npm ci failed"
        Write-Step "Building the Echo web application"
        Invoke-Checked { & npm.cmd run build } "The frontend build failed"
    }
    finally {
        Pop-Location
    }

    Write-Step "Building the Echo server"
    New-Item -ItemType Directory -Force -Path $BuildRoot | Out-Null
    Push-Location $RepositoryRoot
    try {
        Invoke-Checked { & go.exe build -trimpath -o $EchoBinary . } "The Go build failed"
    }
    finally {
        Pop-Location
    }

    if ($env:ECHO_INSTALL_ONLY -eq "1") {
        Write-Step "Echo is ready at $EchoBinary"
        exit 0
    }

    Write-Step "Starting Echo at http://localhost:3740"
    Write-Host "Leave this window open while you use Echo. Press Ctrl+C to stop the server."
    & $EchoBinary @EchoArguments
    exit $LASTEXITCODE
}
catch {
    Write-Host ""
    Write-Host "ERROR: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
}
