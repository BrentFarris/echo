@echo off
setlocal EnableExtensions

cd /d "%~dp0"
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\install-and-run-windows.ps1" %*
set "echo_exit_code=%ERRORLEVEL%"

if not "%echo_exit_code%"=="0" (
    echo.
    echo Echo could not be installed or started. Review the error above.
    pause
)

exit /b %echo_exit_code%
