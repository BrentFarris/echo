param([string]$Binary = (Join-Path $PSScriptRoot '..\echo.exe'), [switch]$Coordinated)
$ErrorActionPreference = 'Stop'
$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$testRoot = Join-Path $repoRoot '.test-data'
$sandboxRoot = Join-Path $testRoot ("development-" + [Guid]::NewGuid().ToString('N'))
$source = Join-Path $sandboxRoot "Echo's source"
$runtimeDir = Join-Path $sandboxRoot 'running copy'
$profile = Join-Path $sandboxRoot 'test profile'
$complete = $false

function Wait-Health([string]$Previous = '', [string]$OperationID = '') {
    $deadline = (Get-Date).AddMinutes(2)
    do {
        if ($OperationID) {
            $op = $null
            try { $op = Invoke-RestMethod "$base/api/v1/development/operations/$OperationID" -WebSession $session -TimeoutSec 2 } catch { }
            if ($op -and $op.status -eq 'failed') { throw ($op.error + "`n" + $op.logTail) }
        }
        try {
            $health = Invoke-RestMethod "$base/api/v1/health" -WebSession $session -TimeoutSec 2
            if ($health.instanceId -and $health.instanceId -ne $Previous) { return $health.instanceId }
        } catch { }
        Start-Sleep -Milliseconds 200
    } while ((Get-Date) -lt $deadline)
    throw "Echo did not become healthy. Logs: $sandboxRoot"
}

try {
    New-Item -ItemType Directory -Path $source, $runtimeDir, $profile -Force | Out-Null
    Copy-Item -LiteralPath $Binary -Destination (Join-Path $runtimeDir 'echo.exe')
    foreach ($file in @('main.go', 'go.mod', 'go.sum', 'build.ps1')) { Copy-Item -LiteralPath (Join-Path $repoRoot $file) -Destination $source }
    foreach ($folder in @('internal', 'cmd')) { Copy-Item -LiteralPath (Join-Path $repoRoot $folder) -Destination $source -Recurse }
    $sourceWeb = Join-Path $source 'web'
    New-Item -ItemType Directory -Path $sourceWeb | Out-Null
    Get-ChildItem -LiteralPath (Join-Path $repoRoot 'web') -File | Copy-Item -Destination $sourceWeb
    foreach ($folder in @('src', 'public', 'e2e')) {
        $from = Join-Path $repoRoot "web\$folder"
        if (Test-Path -LiteralPath $from) { Copy-Item -LiteralPath $from -Destination $sourceWeb -Recurse }
    }
    git -C $source init -b development-test
    if ($LASTEXITCODE) { throw 'Could not initialize isolated source checkout' }
    if ($Coordinated) {
        Copy-Item -LiteralPath (Join-Path $repoRoot '.gitignore') -Destination $source
        git -c core.safecrlf=false -C $source add .
        if ($LASTEXITCODE) { throw 'Could not stage isolated source' }
        git -C $source -c user.name=Echo-Test -c user.email=test@example.invalid -c commit.gpgsign=false commit -m 'Initial updater fixture'
        if ($LASTEXITCODE) { throw 'Could not commit isolated source' }
        $remote = Join-Path $sandboxRoot 'upstream.git'
        git clone --bare $source $remote
        if ($LASTEXITCODE) { throw 'Could not create isolated upstream' }
        git -C $source remote add origin $remote
        git -C $source fetch origin
        git -C $source branch --set-upstream-to=origin/development-test
        if ($LASTEXITCODE) { throw 'Could not configure fixture upstream' }
    }
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    $base = "http://127.0.0.1:$port"
    $session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    $process = Start-Process -FilePath (Join-Path $runtimeDir 'echo.exe') -ArgumentList "-no-open -port $port -data `"$profile`"" -WorkingDirectory $runtimeDir -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $sandboxRoot 'initial.stdout') -RedirectStandardError (Join-Path $sandboxRoot 'initial.stderr')
    $deadline = (Get-Date).AddSeconds(30)
    do {
        try { $null = Invoke-RestMethod "$base/api/v1/session" -Method Post -Body '{}' -ContentType 'application/json' -WebSession $session -TimeoutSec 2; break } catch { Start-Sleep -Milliseconds 200 }
    } while ((Get-Date) -lt $deadline)
    $initial = Wait-Health
    $null = Invoke-RestMethod "$base/api/v1/development" -Method Put -Body (@{repositoryPath=$source} | ConvertTo-Json) -ContentType 'application/json' -WebSession $session
    $accepted = Invoke-RestMethod "$base/api/v1/development/relaunch" -Method Post -Body '{}' -ContentType 'application/json' -WebSession $session
    $restarted = Wait-Health $initial $accepted.operation.id
    if (-not $process.WaitForExit(10000)) { throw 'Original process did not exit gracefully' }
    Write-Output 'Relaunch preserved the profile, session, port, and arguments.'

    $accepted = Invoke-RestMethod "$base/api/v1/development/build-relaunch" -Method Post -Body '{}' -ContentType 'application/json' -WebSession $session
    $deadline = (Get-Date).AddMinutes(5)
    do {
        try { $operation = Invoke-RestMethod "$base/api/v1/development/operations/$($accepted.operation.id)" -WebSession $session -TimeoutSec 3 }
        catch { break }
        if ($operation.status -eq 'failed') { throw ($operation.error + "`n" + $operation.logTail) }
        if ($operation.status -eq 'restarting') { break }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    $built = Wait-Health $restarted
    $settings = Invoke-RestMethod "$base/api/v1/development" -WebSession $session
    if ($settings.repositoryPath -ne $source) { throw 'Repository setting did not survive replacement' }
    if (-not (Test-Path -LiteralPath (Join-Path $source 'echo.exe'))) { throw 'Built executable was not installed' }
    $replacement = @(Get-Process -Name echo -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq (Join-Path $source 'echo.exe') })
    if ($replacement.Count -ne 1) { throw 'Replacement is not running from the selected repository' }
    Write-Output 'Build & Relaunch rebuilt all assets and launched the selected checkout.'

    if ($Coordinated) {
        $publisher = Join-Path $sandboxRoot 'publisher'
        git clone $remote $publisher
        if ($LASTEXITCODE) { throw 'Could not create fixture publisher' }
        Set-Content -LiteralPath (Join-Path $publisher 'update-test.txt') -Value 'Pinned update acceptance'
        git -C $publisher add update-test.txt
        git -C $publisher -c user.name=Echo-Test -c user.email=test@example.invalid -c commit.gpgsign=false commit -m 'Pinned updater target'
        if ($LASTEXITCODE) { throw 'Could not commit updater target' }
        git -C $publisher push origin development-test
        if ($LASTEXITCODE) { throw 'Could not publish fixture target' }
        $targetRevision = git -C $publisher rev-parse HEAD
        $node = (Invoke-RestMethod "$base/api/v1/swarm/peers" -WebSession $session).node.nodeId
        $preview = Invoke-RestMethod "$base/api/v1/development/rollouts/check" -Method Post -Body (@{ sourceNodeId=$node; scope='machine' } | ConvertTo-Json) -ContentType 'application/json' -WebSession $session
        if ($preview.target -ne $targetRevision -or $preview.members[0].status -ne 'queued') { throw "Unexpected update preview: $($preview | ConvertTo-Json -Depth 8)" }
        $rollout = Invoke-RestMethod "$base/api/v1/development/rollouts" -Method Post -Body (@{ checkId=$preview.id } | ConvertTo-Json) -ContentType 'application/json' -WebSession $session
        $deadline = (Get-Date).AddMinutes(5)
        do {
            $result = $null
            try { $result = Invoke-RestMethod "$base/api/v1/development/rollouts/$($rollout.id)" -WebSession $session -TimeoutSec 3 } catch { }
            if ($result -and $result.state -eq 'succeeded') { break }
            if ($result -and $result.state -in @('partial', 'cancelled')) { throw ($result | ConvertTo-Json -Depth 8) }
            Start-Sleep -Milliseconds 300
        } while ((Get-Date) -lt $deadline)
        if (-not $result -or $result.state -ne 'succeeded') { throw 'Coordinated update did not complete' }
        $health = Invoke-RestMethod "$base/api/v1/health" -WebSession $session
        if ($health.revision -ne $targetRevision -or $health.instanceId -eq $built) { throw 'Replacement revision or process identity did not match' }
        $replacement = @(Get-Process -Name echo -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq (Join-Path $source 'echo.exe') })
        if ($replacement.Count -ne 1) { throw 'Expected exactly one updated process' }
        Write-Output 'Coordinated update rebuilt the pinned commit, replaced the process, verified its compiled revision, and recovered the completed rollout.'
    }

    $null = Invoke-RestMethod "$base/api/v1/development/terminate" -Method Post -Body '{}' -ContentType 'application/json' -WebSession $session
    if (-not $replacement[0].WaitForExit(15000)) { throw 'Termination did not stop the replacement' }
    $lock = [IO.File]::Open((Join-Path $profile 'server.lock'), 'Open', 'ReadWrite', 'None')
    $lock.Dispose()
    Write-Output 'Terminate released the profile lock without relaunching.'
    $complete = $true
} finally {
    # Only stop processes whose executable is inside this unique test directory.
    $resolvedTest = [IO.Path]::GetFullPath($sandboxRoot)
    $allowedRoot = [IO.Path]::GetFullPath($testRoot).TrimEnd('\') + '\'
    if (-not $resolvedTest.StartsWith($allowedRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'Test cleanup escaped .test-data' }
    Get-Process -Name echo -ErrorAction SilentlyContinue | Where-Object { $_.Path -and $_.Path.StartsWith($resolvedTest + '\', [StringComparison]::OrdinalIgnoreCase) } | Stop-Process -Force
    if ($complete) { Remove-Item -LiteralPath $resolvedTest -Recurse -Force }
    else { Write-Output "Preserved failure logs and isolated sources at $resolvedTest" }
}
