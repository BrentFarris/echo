$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    $notices = [System.Text.StringBuilder]::new()
    [void]$notices.AppendLine('Echo third-party notices')
    [void]$notices.AppendLine('Generated from the pinned Go modules and bundled browser dependencies. Build/test dependencies are recorded separately in package-lock.json.')
    $modules = go list -deps -f '{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}' . | Sort-Object -Unique
    if ($LASTEXITCODE) { throw 'Cannot inspect module licenses' }
    foreach ($module in $modules) {
        $fields = $module.Split('|')
        if (-not $fields[1] -or -not $fields[2]) { continue }
        [void]$notices.AppendLine("`n===== $($fields[0]) $($fields[1]) =====")
        foreach ($license in Get-ChildItem -LiteralPath $fields[2] -File | Where-Object { $_.Name -like 'LICENSE*' -or $_.Name -like 'COPYING*' -or $_.Name -eq 'NOTICE' }) {
            [void]$notices.AppendLine((Get-Content -Raw -LiteralPath $license.FullName))
        }
    }
    $runtimeRoot = go env GOROOT
    [void]$notices.AppendLine("`n===== Go runtime =====")
    [void]$notices.AppendLine((Get-Content -Raw -LiteralPath (Join-Path $runtimeRoot 'LICENSE')))
    foreach ($package in @('dompurify', 'marked', 'vite')) {
        [void]$notices.AppendLine("`n===== $package =====")
        foreach ($license in Get-ChildItem -LiteralPath "web/node_modules/$package" -File | Where-Object { $_.Name -like 'LICENSE*' -or $_.Name -like 'COPYING*' }) {
            [void]$notices.AppendLine((Get-Content -Raw -LiteralPath $license.FullName))
        }
    }
    [void]$notices.AppendLine("`n===== Unicode CLDR Windows timezone mapping =====")
    [void]$notices.AppendLine('Source: https://github.com/unicode-org/cldr/blob/main/common/supplemental/windowsZones.xml (territory 001), retrieved 2026-10-26.')
    [void]$notices.AppendLine((Get-Content -Raw -LiteralPath 'internal/calendar/UNICODE-LICENSE.txt'))
    [System.IO.File]::WriteAllText((Join-Path (Get-Location) 'THIRD-PARTY-NOTICES.txt'), $notices.ToString())
} finally { Pop-Location }
