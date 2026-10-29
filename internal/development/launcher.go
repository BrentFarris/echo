package development

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type launchSpec struct {
	ProcessID   int
	StagedPath  string
	BinaryPath  string
	Arguments   []string
	WorkingDir  string
	LogPath     string
	ReadyPath   string
	WaitSeconds int
	// ReplaceBinary moves StagedPath over BinaryPath before launching. A
	// relaunch of the already-running binary skips the move because the
	// staged and target paths are identical.
	ReplaceBinary bool
}

func prepareAndLaunch(spec launchSpec, dataDir string) error {
	spec.ReadyPath = filepath.Join(dataDir, "rebuild-relaunch.ready")
	if err := os.Remove(spec.ReadyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale relaunch readiness marker: %w", err)
	}

	var name, content string
	if runtime.GOOS == "windows" {
		name = "rebuild-relaunch.ps1"
		content = buildPowerShellLauncher(spec)
	} else {
		name = "rebuild-relaunch.sh"
		content = buildShellLauncher(spec)
	}
	scriptPath := filepath.Join(dataDir, name)
	if err := os.WriteFile(scriptPath, []byte(content), 0o700); err != nil {
		return fmt.Errorf("write relaunch script: %w", err)
	}
	return launchDetached(scriptPath)
}

func buildPowerShellLauncher(spec launchSpec) string {
	argumentLine := make([]string, 0, len(spec.Arguments))
	for _, argument := range spec.Arguments {
		argumentLine = append(argumentLine, quoteWindowsArgument(argument))
	}
	return "# Echo rebuild-and-relaunch script\r\n" +
		"$ErrorActionPreference = 'Stop'\r\n" +
		fmt.Sprintf("$echoProcessId = %d\r\n", spec.ProcessID) +
		fmt.Sprintf("$stagedBinary = '%s'\r\n", quotePowerShell(spec.StagedPath)) +
		fmt.Sprintf("$binaryPath = '%s'\r\n", quotePowerShell(spec.BinaryPath)) +
		fmt.Sprintf("$workingDirectory = '%s'\r\n", quotePowerShell(spec.WorkingDir)) +
		fmt.Sprintf("$logFile = '%s'\r\n", quotePowerShell(spec.LogPath)) +
		fmt.Sprintf("$readyFile = '%s'\r\n", quotePowerShell(spec.ReadyPath)) +
		fmt.Sprintf("$launchArguments = '%s'\r\n", quotePowerShell(strings.Join(argumentLine, " "))) +
		fmt.Sprintf("$waitSeconds = %d\r\n", spec.WaitSeconds) +
		"function Write-Log { param([string]$Message) \"$((Get-Date -Format 'yyyy-MM-dd HH:mm:ss')) $Message\" | Out-File -LiteralPath $logFile -Append -Encoding utf8 }\r\n" +
		"try {\r\n" +
		"  Write-Log 'Waiting for the current Echo server to stop...'\r\n" +
		"  $previousProcess = Get-Process -Id $echoProcessId -ErrorAction Stop\r\n" +
		"  $null = $previousProcess.Handle\r\n" +
		"  Set-Content -LiteralPath $readyFile -Value $PID -Encoding ascii\r\n" +
		"  if (-not $previousProcess.WaitForExit($waitSeconds * 1000)) { throw 'Graceful shutdown timed out; the existing process was left running.' }\r\n" +
		"  if ($stagedBinary -ne $binaryPath) { Move-Item -LiteralPath $stagedBinary -Destination $binaryPath -Force }\r\n" +
		"  Write-Log \"Launching Echo from $binaryPath\"\r\n" +
		// Every development restart keeps the existing UI, including remote updates.
		"  $env:ECHO_NO_OPEN = '1'\r\n" +
		"  $launch = @{ FilePath = $binaryPath; WorkingDirectory = $workingDirectory; WindowStyle = 'Hidden'; RedirectStandardOutput = ($logFile + '.stdout'); RedirectStandardError = ($logFile + '.stderr') }\r\n" +
		"  if ($launchArguments) { $launch.ArgumentList = $launchArguments }\r\n" +
		"  Start-Process @launch\r\n" +
		"  Write-Log 'Echo relaunch command completed.'\r\n" +
		"} catch { Write-Log \"RELAUNCH FAILED: $($_.Exception.Message)\"; exit 1 }\r\n"
}

func buildShellLauncher(spec launchSpec) string {
	arguments := make([]string, 0, len(spec.Arguments))
	for _, argument := range spec.Arguments {
		arguments = append(arguments, quoteShell(argument))
	}
	launchArguments := ""
	if len(arguments) > 0 {
		launchArguments = " " + strings.Join(arguments, " ")
	}
	return "#!/bin/sh\n" +
		"# Echo rebuild-and-relaunch script\n" +
		fmt.Sprintf("echo_pid=%d\n", spec.ProcessID) +
		fmt.Sprintf("staged_binary=%s\n", quoteShell(spec.StagedPath)) +
		fmt.Sprintf("binary_path=%s\n", quoteShell(spec.BinaryPath)) +
		fmt.Sprintf("working_directory=%s\n", quoteShell(spec.WorkingDir)) +
		fmt.Sprintf("log_file=%s\n", quoteShell(spec.LogPath)) +
		fmt.Sprintf("ready_file=%s\n", quoteShell(spec.ReadyPath)) +
		fmt.Sprintf("wait_seconds=%d\n", spec.WaitSeconds) +
		"log() { printf '%s %s\\n' \"$(date '+%Y-%m-%d %H:%M:%S')\" \"$1\" >> \"$log_file\"; }\n" +
		"log 'Waiting for the current Echo server to stop...'\n" +
		"printf '%s\\n' \"$$\" > \"$ready_file\" || { log 'RELAUNCH FAILED: could not create readiness marker.'; exit 1; }\n" +
		"elapsed=0\n" +
		"while kill -0 \"$echo_pid\" 2>/dev/null; do\n" +
		"  if [ \"$elapsed\" -ge \"$wait_seconds\" ]; then log 'Graceful shutdown timed out; the existing process was left running.'; exit 1; fi\n" +
		"  sleep 1\n" +
		"  elapsed=$((elapsed + 1))\n" +
		"done\n" +
		"while kill -0 \"$echo_pid\" 2>/dev/null; do sleep 1; done\n" +
		"if [ \"$staged_binary\" != \"$binary_path\" ]; then mv -f \"$staged_binary\" \"$binary_path\" || { log 'RELAUNCH FAILED: could not replace binary.'; exit 1; }; fi\n" +
		"cd \"$working_directory\" || { log 'RELAUNCH FAILED: working directory is unavailable.'; exit 1; }\n" +
		"log \"Launching Echo from $binary_path\"\n" +
		"ECHO_NO_OPEN=1 nohup \"$binary_path\"" + launchArguments + " >> \"$log_file\" 2>&1 &\n" +
		"log 'Echo relaunch command completed.'\n"
}

func quotePowerShell(value string) string { return strings.ReplaceAll(value, "'", "''") }

func quoteShell(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// quoteWindowsArgument follows CommandLineToArgvW quoting rules so
// Start-Process receives one complete, correctly escaped argument line.
func quoteWindowsArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\v\"") {
		return value
	}
	var builder strings.Builder
	builder.WriteByte('"')
	backslashes := 0
	for _, character := range value {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			builder.WriteString(strings.Repeat("\\", backslashes*2+1))
			builder.WriteRune(character)
			backslashes = 0
			continue
		}
		builder.WriteString(strings.Repeat("\\", backslashes))
		backslashes = 0
		builder.WriteRune(character)
	}
	builder.WriteString(strings.Repeat("\\", backslashes*2))
	builder.WriteByte('"')
	return builder.String()
}
