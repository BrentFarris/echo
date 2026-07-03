package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// PrepareRebuildAndRelaunch writes a platform-specific relaunch script that
// waits for graceful shutdown before force-killing and rebuilding, launches
// the script as a detached background process, then calls app.Quit() to
// trigger graceful shutdown.
func (s *SystemService) PrepareRebuildAndRelaunch(workspaceID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return fmt.Errorf("workspace id is required")
	}

	ws, err := s.workspaceByID(workspaceID)
	if err != nil {
		return err
	}

	// Find the first available folder containing wails.json
	for _, folder := range ws.Folders {
		if folder.Missing || folder.Path == "" {
			continue
		}
		wailsConfig := filepath.Join(folder.Path, "wails.json")
		info, err := os.Stat(wailsConfig)
		if err != nil || info.IsDir() {
			continue
		}
		// Found a valid Echo source root — proceed with this folder.
		scriptPath, err := prepareRebuildScript(folder.Path)
		if err != nil {
			return fmt.Errorf("prepare rebuild script: %w", err)
		}
		if err := launchDetachedRebuild(scriptPath); err != nil {
			return fmt.Errorf("launch rebuild: %w", err)
		}
		runtimeQuit(s.ctx)
		return nil
	}

	return fmt.Errorf("workspace does not contain a folder with wails.json; rebuild requires the Echo source directory")
}

func runtimeQuit(ctx context.Context) {
	if ctx != nil {
		runtime.Quit(ctx)
	}
}

func prepareRebuildScript(workspaceDir string) (string, error) {
	scriptDir := os.TempDir()
	if goruntime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			scriptDir = filepath.Join(localAppData, "Echo")
		}
	} else {
		if home, err := os.UserHomeDir(); err == nil {
			scriptDir = filepath.Join(home, ".echo")
		}
	}
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return "", fmt.Errorf("create script dir: %w", err)
	}

	var scriptPath string
	var scriptContent string
	if goruntime.GOOS == "windows" {
		scriptPath = filepath.Join(scriptDir, "rebuild-relaunch.ps1")
		scriptContent = buildRebuildPowerShellScript(workspaceDir)
	} else {
		scriptPath = filepath.Join(scriptDir, "rebuild-relaunch.sh")
		scriptContent = buildRebuildShellScript(workspaceDir)
	}

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o644); err != nil {
		return "", fmt.Errorf("write script: %w", err)
	}

	return scriptPath, nil
}

func buildRebuildShellScript(workspaceDir string) string {
	return "#!/bin/sh\n" +
		"# Echo rebuild-and-relaunch script\n" +
		fmt.Sprintf("workspace_dir=\"%s\"\n", workspaceDir) +
		"binary_name=\"echo\"\n" +
		"binary_path=\"$workspace_dir/build/bin/$binary_name\"\n" +
		"log_file=\"$HOME/.echo/rebuild-relaunch.log\"\n" +
		"\n" +
		"log() {\n" +
		"    echo \"$(date '+%Y-%m-%d %H:%M:%S') $1\" >> \"$log_file\"\n" +
		"}\n" +
		"\n" +
		"log \"=== Echo rebuild started ===\"\n" +
		"log \"Waiting for graceful shutdown...\"\n" +
		"\n" +
		"# Wait up to 10 seconds for Echo to shut down gracefully.\n" +
		"for i in $(seq 1 10); do\n" +
		"    if ! pgrep -f \"$binary_path\" >/dev/null 2>&1; then\n" +
		"        log \"Echo shut down gracefully.\"\n" +
		"        break\n" +
		"    fi\n" +
		"    sleep 1\n" +
		"done\n" +
		"\n" +
		"# Force-kill any remaining processes.\n" +
		"echo_pids=$(pgrep -f \"$binary_path\" 2>/dev/null || true)\n" +
		"if [ -n \"$echo_pids\" ]; then\n" +
		"    log \"Force-killing echo process(es): $echo_pids\"\n" +
		"    kill -9 $echo_pids 2>/dev/null || true\n" +
		"    sleep 2\n" +
		"fi\n" +
		"\n" +
		"# Rebuild.\n" +
		"cd \"$workspace_dir\" || exit 1\n" +
		"log \"Running wails build...\"\n" +
		"wails_build_output=$(wails build 2>&1)\n" +
		"build_exit_code=$?\n" +
		"echo \"$wails_build_output\" >> \"$log_file\"\n" +
		"\n" +
		"if [ $build_exit_code -ne 0 ]; then\n" +
		"    log \"BUILD FAILED with exit code $build_exit_code. Check $log_file for details.\"\n" +
		"    exit $build_exit_code\n" +
		"fi\n" +
		"\n" +
		"log \"Build succeeded. Launching Echo...\"\n" +
		"nohup \"$binary_path\" > /dev/null 2>&1 &\n" +
		"disown\n" +
		"log \"Echo launched successfully.\"\n"
}

func buildRebuildPowerShellScript(workspaceDir string) string {
	return "# Echo rebuild-and-relaunch script\r\n" +
		"$ErrorActionPreference = \"Stop\"\r\n" +
		fmt.Sprintf("$workspaceDir = \"%s\"\r\n", workspaceDir) +
		"$binaryName = \"echo.exe\"\r\n" +
		"$binaryPath = Join-Path (Join-Path $workspaceDir \"build\\bin\") $binaryName\r\n" +
		"$logFile = Join-Path (Join-Path $env:LOCALAPPDATA \"Echo\") \"rebuild-relaunch.log\"\r\n" +
		"\r\n" +
		"function Write-Log {\r\n" +
		"    param([string]$Message)\r\n" +
		"    \"$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $Message\" | Out-File -FilePath $logFile -Append -Encoding utf8\r\n" +
		"}\r\n" +
		"\r\n" +
		"Write-Log \"=== Echo rebuild started ===\"\r\n" +
		"Write-Log \"Waiting for graceful shutdown...\"\r\n" +
		"\r\n" +
		"# Wait up to 10 seconds for Echo to shut down gracefully.\r\n" +
		"$shutdownTimeout = 10\r\n" +
		"while ($shutdownTimeout -gt 0) {\r\n" +
		"    $echoProcesses = Get-Process -Name \"echo\" -ErrorAction SilentlyContinue | Where-Object { $_.Path -like \"$workspaceDir\\*\" -or $_.Path -like \"$workspaceDir/*\" }\r\n" +
		"    if (-not $echoProcesses) {\r\n" +
		"        Write-Log \"Echo shut down gracefully.\"\r\n" +
		"        break\r\n" +
		"    }\r\n" +
		"    Start-Sleep -Seconds 1\r\n" +
		"    $shutdownTimeout--\r\n" +
		"}\r\n" +
		"\r\n" +
		"# Force-kill any remaining processes.\r\n" +
		"$remaining = Get-Process -Name \"echo\" -ErrorAction SilentlyContinue | Where-Object { $_.Path -like \"$workspaceDir\\*\" -or $_.Path -like \"$workspaceDir/*\" }\r\n" +
		"if ($remaining) {\r\n" +
		"    Write-Log \"Force-killing $($remaining.Count) echo.exe process(es)\"\r\n" +
		"    $remaining | Stop-Process -Force\r\n" +
		"    Start-Sleep -Seconds 2\r\n" +
		"}\r\n" +
		"\r\n" +
		"# Rebuild.\r\n" +
		"Set-Location $workspaceDir\r\n" +
		"Write-Log \"Running wails build...\"\r\n" +
		"$buildResult = & wails build 2>&1\r\n" +
		"$buildExitCode = $LASTEXITCODE\r\n" +
		"foreach ($line in $buildResult) {\r\n" +
		"    Write-Log $line\r\n" +
		"}\r\n" +
		"\r\n" +
		"if ($buildExitCode -ne 0) {\r\n" +
		"    Write-Log \"BUILD FAILED with exit code $buildExitCode. Check $logFile for details.\"\r\n" +
		"    exit $buildExitCode\r\n" +
		"}\r\n" +
		"\r\n" +
		"Write-Log \"Build succeeded. Launching Echo...\"\r\n" +
		"Start-Process $binaryPath\r\n" +
		"Write-Log \"Echo launched successfully.\"\r\n"
}
