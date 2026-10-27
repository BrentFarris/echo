param(
    [switch]$Install,
    [switch]$Uninstall,
    [string]$Executable,
    [string]$DataDirectory = (Join-Path $env:LOCALAPPDATA 'Echo2'),
    [ValidateRange(1, 65535)][int]$Port = 3740
)
$ErrorActionPreference = 'Stop'
$taskName = 'Echo-' + [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
if ($Uninstall) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
    return
}
if (-not $Install) {
    Write-Output 'Usage: .\scripts\startup.ps1 -Install -Executable C:\path\to\echo.exe [-DataDirectory C:\path\to\profile] [-Port 3740]'
    Write-Output 'Creates a per-user login task. Uses the existing Windows credential vault; no owner password is stored in the task.'
    return
}
if (-not [System.IO.Path]::IsPathRooted($Executable) -or -not (Test-Path -LiteralPath $Executable -PathType Leaf)) { throw 'Choose an existing absolute Echo executable path.' }
if (-not [System.IO.Path]::IsPathRooted($DataDirectory)) { throw 'Use an absolute profile path.' }
if ($Executable -match '["\r\n]' -or $DataDirectory -match '["\r\n]') { throw 'Paths cannot contain quotes or line breaks.' }
$DataDirectory = [System.IO.Path]::GetFullPath($DataDirectory)
$null = New-Item -ItemType Directory -Path $DataDirectory -Force
$runner = Join-Path $DataDirectory 'startup-runner.ps1'
$quotedExecutable = $Executable.Replace("'", "''")
$quotedDirectory = $DataDirectory.Replace("'", "''")
$source = @'
$ErrorActionPreference = 'Stop'
$process = Start-Process -FilePath '__EXECUTABLE__' -ArgumentList '-data "__PROFILE__" -port __PORT__' -WindowStyle Hidden -Wait -PassThru -RedirectStandardOutput '__PROFILE__\service.stdout.log' -RedirectStandardError '__PROFILE__\service.stderr.log'
exit $process.ExitCode
'@
$source = $source.Replace('__EXECUTABLE__', $quotedExecutable).Replace('__PROFILE__', $quotedDirectory).Replace('__PORT__', [string]$Port)
Set-Content -LiteralPath $runner -Value $source -Encoding UTF8
$user = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
$powerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$action = New-ScheduledTaskAction -Execute $powerShell -Argument ('-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "' + $runner + '"')
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Description 'Echo background startup for this user' -Force | Out-Null
Write-Output "Installed $taskName. It starts at your next login; start Echo manually for this session."
