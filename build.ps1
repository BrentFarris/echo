param([switch]$Test, [switch]$NativeTests, [switch]$BrowserTests, [switch]$Release, [string]$OutputPath = 'echo.exe')
$ErrorActionPreference = 'Stop'
if ($Release -and $OutputPath -ne 'echo.exe') { throw '-OutputPath cannot be combined with -Release' }
if ($Release) { $Test = $true; $NativeTests = $true; $BrowserTests = $true }
$previousNative = $env:ECHO2_NATIVE_TESTS
$previousRequired = $env:ECHO2_REQUIRE_TOOLCHAINS
Push-Location $PSScriptRoot
try {
    $env:GOCACHE = Join-Path $PSScriptRoot '.cache\go-build'
    $oldOS = $env:GOOS
    $oldArch = $env:GOARCH
    try {
        $env:GOOS = 'wasip1'
        $env:GOARCH = 'wasm'
        go build -trimpath -o internal/plugins/guest/builtin.wasm ./internal/plugins/guest
        if ($LASTEXITCODE) { throw 'Plugin runtime build failed' }
    } finally {
        $env:GOOS = $oldOS
        $env:GOARCH = $oldArch
    }
    Push-Location web
    try {
        # npm ci removes node_modules first. Windows cannot remove native modules
        # loaded by Vite/Vitest, and a failed cleanup leaves a partial install.
        # Stop only Node processes with modules loaded from this checkout. Echo
        # stays running until the staged build and detached launcher are ready.
        $modulesRoot = [IO.Path]::GetFullPath((Join-Path $PWD.Path 'node_modules')).TrimEnd('\') + '\'
        $lockingProcesses = @(Get-Process -Name node -ErrorAction SilentlyContinue | ForEach-Object {
            $nodeProcess = $_
            try {
                # Pin this process's identity even if it exits and Windows reuses its PID.
                $null = $nodeProcess.Handle
                $loadedModules = @($nodeProcess.Modules)
            } catch { $loadedModules = @() }
            if ($loadedModules | Where-Object {
                $_.FileName -and $_.FileName.StartsWith($modulesRoot, [StringComparison]::OrdinalIgnoreCase)
            }) { $nodeProcess }
        })
        foreach ($nodeProcess in $lockingProcesses) {
            Write-Output "Stopping Node process $($nodeProcess.Id) holding frontend dependencies in $modulesRoot"
            try {
                # Use the captured process object rather than looking up its PID
                # again, and wait for Windows to release its native module locks.
                if (-not $nodeProcess.HasExited) { Stop-Process -InputObject $nodeProcess -Force -ErrorAction Stop }
                if (-not $nodeProcess.WaitForExit(10000)) { throw 'Process exit timed out' }
            } catch {
                # A watcher can exit on its own between discovery and stopping.
                if (-not $nodeProcess.HasExited) {
                    throw "Could not stop frontend Node process $($nodeProcess.Id): $($_.Exception.Message). npm ci has not been started."
                }
            }
        }
        npm ci --ignore-scripts --prefer-offline
        if ($LASTEXITCODE) { throw 'Frontend dependency installation failed. For EPERM/unlink errors, stop processes using this checkout (such as Vite or Vitest), then rerun build.ps1.' }
        npm run build
        if ($LASTEXITCODE) { throw 'Frontend build failed' }
        if ($Test) { npm test; if ($LASTEXITCODE) { throw 'Frontend tests failed' } }
    } finally { Pop-Location }
    if ($NativeTests) { $env:ECHO2_NATIVE_TESTS = '1'; $env:ECHO2_REQUIRE_TOOLCHAINS = '1' }
    if ($Test -or $NativeTests) {
        go test ./... -count=1 -timeout 5m
        if ($LASTEXITCODE) { throw 'Go acceptance tests failed' }
        go vet ./...
        if ($LASTEXITCODE) { throw 'Go vet failed' }
    }
    $revision = git rev-parse --verify --quiet HEAD
    if ($LASTEXITCODE) { $revision = 'unknown' }
    $sourceChanges = git status --porcelain --untracked-files=normal
    if ($LASTEXITCODE) { $sourceChanges = 'unknown' }
    if ($sourceChanges) { $revision = "$revision-modified" }
    go build -trimpath -ldflags "-X github.com/brent/echo/internal/buildinfo.Revision=$revision" -o $OutputPath .
    if ($LASTEXITCODE) { throw 'Echo build failed' }
    if ($BrowserTests) {
        go build -o mock-model.exe ./cmd/mock-model
        if ($LASTEXITCODE) { throw 'Mock model build failed' }
        go build -o browser-fixture.exe ./cmd/browser-fixture
        if ($LASTEXITCODE) { throw 'Browser fixture build failed' }
        Push-Location web
        try {
            npx playwright install chromium
            if ($LASTEXITCODE) { throw 'Playwright browser installation failed' }
            npm run test:e2e
            if ($LASTEXITCODE) { throw 'Browser acceptance tests failed' }
        } finally { Pop-Location }
    }
    if ($Release) {
        & (Join-Path $PSScriptRoot 'scripts\notices.ps1')
        $architecture = go env GOARCH
        $releaseStage = Join-Path $PSScriptRoot '.cache\release-windows'
        $null = New-Item -ItemType Directory -Path (Join-Path $releaseStage 'scripts') -Force
        Copy-Item -LiteralPath echo.exe, README.md, ARCHITECTURE.md, ACCEPTANCE.md, LICENSE, THIRD-PARTY-NOTICES.txt -Destination $releaseStage -Force
        Copy-Item -LiteralPath scripts/startup.ps1 -Destination (Join-Path $releaseStage 'scripts') -Force
        Compress-Archive -Path (Join-Path $releaseStage '*') -DestinationPath "echo-windows-$architecture.zip" -Force
        Get-FileHash -Algorithm SHA256 "echo-windows-$architecture.zip"
    }
} finally {
    $env:ECHO2_NATIVE_TESTS = $previousNative
    $env:ECHO2_REQUIRE_TOOLCHAINS = $previousRequired
    Pop-Location
}
