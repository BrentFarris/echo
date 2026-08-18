package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "restart",
			Description: "Restart the Echo application by stopping the current process, rebuilding with go build, and relaunching. Spawns a background process that survives after Echo exits.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
		},
		Run: executeRestart,
	})
}

func executeRestart(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}

	workspaceDir := ctx.WorkspacePath
	if workspaceDir == "" {
		roots := ctx.workspaceRoots()
		if len(roots) > 0 {
			workspaceDir = roots[0].Path
		}
	}
	if workspaceDir == "" {
		return nil, SafeError{Code: "missing_workspace", Message: "no workspace is configured"}
	}

	// Verify this is the Echo source root so we never rebuild an arbitrary project.
	if err := verifyEchoSource(workspaceDir); err != nil {
		return nil, err
	}

	scriptPath, _, err := prepareRestartScript(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("prepare restart script: %w", err)
	}

	if err := launchDetachedRestart(scriptPath); err != nil {
		return nil, fmt.Errorf("launch restart: %w", err)
	}

	binaryName := "echo"
	if runtime.GOOS == "windows" {
		binaryName = "echo.exe"
	}
	binaryPath := filepath.Join(workspaceDir, binaryName)

	return map[string]any{
		"status":       "restarting",
		"message":      fmt.Sprintf("Background restart process launched. Echo will stop shortly. Wait for the rebuild to finish (~60 s), then %s will relaunch automatically.", binaryName),
		"binaryPath":   binaryPath,
		"scriptPath":   scriptPath,
		"workspaceDir": workspaceDir,
	}, nil
}

// verifyEchoSource confirms the workspace is the Echo source root by checking
// that its go.mod declares the Echo module.
func verifyEchoSource(workspaceDir string) error {
	gomod := filepath.Join(workspaceDir, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return SafeError{Code: "not_echo_workspace", Message: "workspace does not contain an Echo go.mod; restart only works from the Echo source directory"}
	}
	if !strings.Contains(string(data), "module github.com/brent/echo") {
		return SafeError{Code: "not_echo_workspace", Message: "go.mod does not declare the Echo module; restart only works from the Echo source directory"}
	}
	return nil
}

func prepareRestartScript(workspaceDir string) (string, func() error, error) {
	scriptDir := os.TempDir()
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			scriptDir = filepath.Join(localAppData, "Echo")
		}
	} else {
		if home, err := os.UserHomeDir(); err == nil {
			scriptDir = filepath.Join(home, ".echo")
		}
	}
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		return "", func() error { return nil }, fmt.Errorf("create script dir: %w", err)
	}

	var scriptPath string
	var scriptContent string
	if runtime.GOOS == "windows" {
		scriptPath = filepath.Join(scriptDir, "restart.ps1")
		scriptContent = buildPowerShellScript(workspaceDir)
	} else {
		scriptPath = filepath.Join(scriptDir, "restart.sh")
		scriptContent = buildShellScript(workspaceDir)
	}

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0644); err != nil {
		return "", func() error { return nil }, fmt.Errorf("write script: %w", err)
	}

	cleanup := func() error { return os.Remove(scriptPath) }
	return scriptPath, cleanup, nil
}

func buildShellScript(workspaceDir string) string {
	return "#!/bin/sh\n" +
		"# Echo self-restart script\n" +
		fmt.Sprintf("workspace_dir=\"%s\"\n", workspaceDir) +
		"binary_name=\"echo\"\n" +
		"binary_path=\"$workspace_dir/$binary_name\"\n" +
		"dev_binary_pattern=\"$workspace_dir/__debug_bin\"\n" +
		"log_file=\"$HOME/.echo/restart.log\"\n" +
		"\n" +
		"log() {\n" +
		"    echo \"$(date '+%Y-%m-%d %H:%M:%S') $1\" >> \"$log_file\"\n" +
		"}\n" +
		"\n" +
		"log \"=== Echo restart started ===\"\n" +
		"log \"Workspace: $workspace_dir\"\n" +
		"\n" +
		"# Kill existing Echo processes from this workspace (built binary and dev binary).\n" +
		"echo_pids=$(pgrep -f \"$binary_path\" 2>/dev/null || true)\n" +
		"dev_pids=$(pgrep -f \"$dev_binary_pattern\" 2>/dev/null || true)\n" +
		"if [ -n \"$echo_pids$dev_pids\" ]; then\n" +
		"    log \"Stopping echo process(es): $echo_pids $dev_pids\"\n" +
		"    kill -9 $echo_pids $dev_pids 2>/dev/null || true\n" +
		"    sleep 2\n" +
		"else\n" +
		"    log \"No running echo found in workspace\"\n" +
		"fi\n" +
		"\n" +
		"# Rebuild.\n" +
		"cd \"$workspace_dir\" || exit 1\n" +
		"log \"Running go build...\"\n" +
		"go_build_output=$(go build -o \"$binary_path\" . 2>&1)\n" +
		"build_exit_code=$?\n" +
		"echo \"$go_build_output\" >> \"$log_file\"\n" +
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

func buildPowerShellScript(workspaceDir string) string {
	return "# Echo self-restart script\r\n" +
		"$ErrorActionPreference = \"Stop\"\r\n" +
		fmt.Sprintf("$workspaceDir = \"%s\"\r\n", workspaceDir) +
		"$binaryName = \"echo.exe\"\r\n" +
		"$binaryPath = Join-Path $workspaceDir $binaryName\r\n" +
		"$devBinaryPattern = Join-Path $workspaceDir \"__debug_bin*.exe\"\r\n" +
		"$logFile = Join-Path (Join-Path $env:LOCALAPPDATA \"Echo\") \"restart.log\"\r\n" +
		"\r\n" +
		"function Write-Log {\r\n" +
		"    param([string]$Message)\r\n" +
		"    \"$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $Message\" | Out-File -FilePath $logFile -Append -Encoding utf8\r\n" +
		"}\r\n" +
		"\r\n" +
		"Write-Log \"=== Echo restart started ===\"\r\n" +
		"Write-Log \"Workspace: $workspaceDir\"\r\n" +
		"\r\n" +
		"# Kill existing Echo processes from this workspace (built binary and dev binary).\r\n" +
		"$echoProcesses = Get-Process -ErrorAction SilentlyContinue | Where-Object {\r\n" +
		"    $_.Path -and ($_.Path -eq $binaryPath -or $_.Path -like $devBinaryPattern)\r\n" +
		"}\r\n" +
		"if ($echoProcesses) {\r\n" +
		"    Write-Log \"Stopping $($echoProcesses.Count) echo process(es)\"\r\n" +
		"    $echoProcesses | Stop-Process -Force\r\n" +
		"    Start-Sleep -Seconds 2\r\n" +
		"} else {\r\n" +
		"    Write-Log \"No running echo.exe found in workspace\"\r\n" +
		"}\r\n" +
		"\r\n" +
		"# Rebuild.\r\n" +
		"Set-Location $workspaceDir\r\n" +
		"Write-Log \"Running go build...\"\r\n" +
		"$buildResult = & go build -o $binaryPath . 2>&1\r\n" +
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
