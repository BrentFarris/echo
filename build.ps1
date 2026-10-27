param([switch]$Test, [switch]$NativeTests, [switch]$BrowserTests, [switch]$Release)
$ErrorActionPreference = 'Stop'
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
        npm ci --ignore-scripts --prefer-offline
        if ($LASTEXITCODE) { throw 'Frontend dependency installation failed' }
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
    go build -trimpath -o echo.exe .
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
        Compress-Archive -Path echo.exe, README.md, ARCHITECTURE.md, ACCEPTANCE.md, LICENSE, THIRD-PARTY-NOTICES.txt -DestinationPath "echo-windows-$architecture.zip" -Force
        Get-FileHash -Algorithm SHA256 "echo-windows-$architecture.zip"
    }
} finally {
    $env:ECHO2_NATIVE_TESTS = $previousNative
    $env:ECHO2_REQUIRE_TOOLCHAINS = $previousRequired
    Pop-Location
}
