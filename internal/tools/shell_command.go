package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultShellTimeoutSeconds = 30
	maxShellTimeoutSeconds     = 300
	defaultShellOutputBytes    = 64 * 1024
	maxShellOutputBytes        = maxTextFileBytes
	shellCommandWaitDelay      = 2 * time.Second
)

var shellChangeTrackingTimeout = time.Second

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "shell_command",
			Description: "Execute a command in the local system shell from inside the active workspace. Commands run with the app user's OS permissions.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"command"},
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to execute. Set workingDirectory instead of putting cd or Set-Location in the command. On Windows this runs in PowerShell, not cmd.exe; write PowerShell-native syntax and avoid CMD syntax such as dir /s, copy, del, type, set VAR=VALUE, and %VAR%. Prefer commands such as Get-ChildItem, Copy-Item, Remove-Item, Get-Content, Select-String, and $env:VAR.",
					},
					"workingDirectory": map[string]any{
						"type":        "string",
						"description": "Labeled workspace directory to run in. Defaults to the first workspace folder. Prefer this over changing directories inside command so execution and file-change tracking stay scoped to the intended project. " + labeledPathSchemaHint,
					},
					"timeoutSeconds": map[string]any{
						"type":        "integer",
						"description": "Maximum runtime in seconds. Defaults to 30 and is capped at 300.",
						"minimum":     1,
						"maximum":     maxShellTimeoutSeconds,
					},
					"maxOutputBytes": map[string]any{
						"type":        "integer",
						"description": "Maximum bytes to capture for each of stdout and stderr. Defaults to 65536 and is capped at 262144.",
						"minimum":     1,
						"maximum":     maxShellOutputBytes,
					},
				},
			},
		},
		Run: executeShellCommand,
	})
}

type shellCommandArgs struct {
	Command          string `json:"command"`
	WorkingDirectory string `json:"workingDirectory"`
	TimeoutSeconds   int    `json:"timeoutSeconds"`
	MaxOutputBytes   int    `json:"maxOutputBytes"`
}

type shellCommandOutput struct {
	Command              string `json:"command"`
	WorkingDirectory     string `json:"workingDirectory"`
	Shell                string `json:"shell"`
	ExitCode             int    `json:"exitCode"`
	Stdout               string `json:"stdout"`
	Stderr               string `json:"stderr"`
	StdoutTruncated      bool   `json:"stdoutTruncated"`
	StderrTruncated      bool   `json:"stderrTruncated"`
	TimedOut             bool   `json:"timedOut"`
	DurationMilliseconds int64  `json:"durationMilliseconds"`
}

func executeShellCommand(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args shellCommandArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	args.Command = strings.TrimSpace(args.Command)
	if args.Command == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "command is required"}
	}

	workingDirectory, err := resolveShellWorkingDirectory(ctx, args.WorkingDirectory)
	if err != nil {
		return nil, err
	}
	timeoutSeconds := args.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultShellTimeoutSeconds
	}
	if timeoutSeconds > maxShellTimeoutSeconds {
		timeoutSeconds = maxShellTimeoutSeconds
	}
	outputLimit := args.MaxOutputBytes
	if outputLimit <= 0 {
		outputLimit = defaultShellOutputBytes
	}
	if outputLimit > maxShellOutputBytes {
		outputLimit = maxShellOutputBytes
	}

	commandContext, cancel := context.WithTimeout(ctx.context(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	shellName, shellArgs, err := shellCommandInvocation(args.Command)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(commandContext, shellName, shellArgs...)
	command.Dir = workingDirectory
	configureShellCommandProcess(command)
	command.WaitDelay = shellCommandWaitDelay

	stdout := newLimitedBuffer(outputLimit)
	stderr := newLimitedBuffer(outputLimit)
	command.Stdout = stdout
	command.Stderr = stderr

	var before workspaceSnapshot
	trackChanges := ctx.FileChanges != nil
	if trackChanges {
		before = snapshotShellWorkspaceChanges(ctx, workingDirectory)
	}

	started := time.Now()
	runErr := command.Run()
	duration := time.Since(started)
	if trackChanges && before != nil {
		if after := snapshotShellWorkspaceChanges(ctx, workingDirectory); after != nil {
			ctx.recordFileChanges(diffWorkspaceSnapshots(before, after)...)
		}
	}
	timedOut := commandContext.Err() == context.DeadlineExceeded
	exitCode := 0
	if runErr != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if !timedOut {
			return nil, fmt.Errorf("run shell command: %w", runErr)
		}
	}

	return shellCommandOutput{
		Command:              args.Command,
		WorkingDirectory:     relativeWorkspacePath(ctx, workingDirectory),
		Shell:                shellName,
		ExitCode:             exitCode,
		Stdout:               stdout.String(),
		Stderr:               stderr.String(),
		StdoutTruncated:      stdout.Truncated(),
		StderrTruncated:      stderr.Truncated(),
		TimedOut:             timedOut,
		DurationMilliseconds: duration.Milliseconds(),
	}, nil
}

func snapshotShellWorkspaceChanges(ctx ExecutionContext, workingDirectory string) workspaceSnapshot {
	snapshotContext, cancel := context.WithTimeout(ctx.context(), shellChangeTrackingTimeout)
	defer cancel()

	snapshot, err := snapshotWorkspaceDirectoryChanges(snapshotContext, ctx, workingDirectory)
	if err != nil {
		return nil
	}
	return snapshot
}

func resolveShellWorkingDirectory(ctx ExecutionContext, requestedPath string) (string, error) {
	if strings.TrimSpace(requestedPath) == "" {
		roots := ctx.workspaceRoots()
		if len(roots) == 0 {
			return "", SafeError{Code: "missing_workspace", Message: "workspace path is required"}
		}
		requestedPath = roots[0].Label
	}
	path, err := resolveWorkspacePath(ctx, requestedPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", SafeError{Code: "path_not_found", Message: "working directory was not found"}
	}
	if !info.IsDir() {
		return "", SafeError{Code: "not_directory", Message: "working directory is not a directory"}
	}
	return path, nil
}

func shellCommandInvocation(command string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		if shell, err := exec.LookPath("pwsh.exe"); err == nil {
			return shell, []string{"-NoProfile", "-NonInteractive", "-Command", command}, nil
		}
		if shell, err := exec.LookPath("powershell.exe"); err == nil {
			return shell, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command}, nil
		}
		return "", nil, SafeError{Code: "shell_not_found", Message: "PowerShell was not found"}
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" && filepath.IsAbs(shell) {
		return shell, []string{"-c", command}, nil
	}
	return "/bin/sh", []string{"-c", command}, nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = b.truncated || len(data) > 0
		return len(data), nil
	}
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || len(data) > 0
		return len(data), nil
	}
	if len(data) > remaining {
		_, _ = b.buffer.Write(data[:remaining])
		b.truncated = true
		return len(data), nil
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

func (b *limitedBuffer) Truncated() bool {
	return b.truncated
}
