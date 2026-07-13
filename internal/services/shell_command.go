package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type ShellCommandEventType string

const (
	ShellCommandTypeStarted   ShellCommandEventType = "started"
	ShellCommandTypeStdout    ShellCommandEventType = "stdout"
	ShellCommandTypeStderr    ShellCommandEventType = "stderr"
	ShellCommandTypeCompleted ShellCommandEventType = "completed"
)

type ShellCommandEvent struct {
	WorkspaceID string                `json:"workspaceId"`
	ID          string                `json:"id"`
	Type        ShellCommandEventType `json:"type"`
	Data        any                   `json:"data,omitempty"`
}

type ShellCommandCompletedData struct {
	ExitCode             int   `json:"exitCode"`
	TimedOut             bool  `json:"timedOut"`
	DurationMilliseconds int64 `json:"durationMilliseconds"`
}

func (s *SystemService) RunShellCommand(workspaceID, command, workingDirectory string, timeoutSeconds, maxOutputBytes int) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}

	// Validate workspace exists
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return "", err
	}

	// Resolve working directory
	resolvedDir, err := resolveShellWorkingDir(workspace, workingDirectory)
	if err != nil {
		return "", err
	}

	// Default and clamp timeout
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	if timeoutSeconds > 300 {
		timeoutSeconds = 300
	}

	// Default and clamp output bytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = 64 * 1024
	}
	if maxOutputBytes > 256*1024 {
		maxOutputBytes = 256 * 1024
	}

	// Generate unique run ID
	s.chatMu.Lock()
	s.shellCommandSeq++
	seq := s.shellCommandSeq
	runID := fmt.Sprintf("%s:%d", workspaceID, seq)
	s.chatMu.Unlock()

	shellName, shellArgs, err := resolveShellInvocation(command)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)

	// Store cancel func before starting goroutine
	s.chatMu.Lock()
	s.shellCommandRuns[runID] = cancel
	s.chatMu.Unlock()

	// Emit started event
	s.emitRuntimeEvent(ShellRuntimeEventName, ShellCommandEvent{
		WorkspaceID: workspaceID,
		ID:          runID,
		Type:        ShellCommandTypeStarted,
	})

	go func() {
		defer s.cleanupShellRun(runID)

		cmd := exec.CommandContext(ctx, shellName, shellArgs...)
		cmd.Dir = resolvedDir
		configureProcess(cmd)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			s.emitRuntimeEvent(ShellRuntimeEventName, ShellCommandEvent{
				WorkspaceID: workspaceID,
				ID:          runID,
				Type:        ShellCommandTypeCompleted,
				Data: ShellCommandCompletedData{
					ExitCode: -1,
				},
			})
			return
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			s.emitRuntimeEvent(ShellRuntimeEventName, ShellCommandEvent{
				WorkspaceID: workspaceID,
				ID:          runID,
				Type:        ShellCommandTypeCompleted,
				Data: ShellCommandCompletedData{
					ExitCode: -1,
				},
			})
			return
		}

		if err := cmd.Start(); err != nil {
			s.emitRuntimeEvent(ShellRuntimeEventName, ShellCommandEvent{
				WorkspaceID: workspaceID,
				ID:          runID,
				Type:        ShellCommandTypeCompleted,
				Data: ShellCommandCompletedData{
					ExitCode: -1,
				},
			})
			return
		}

		started := time.Now()

		// Read stdout/stderr concurrently
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			scanLines(stdout, ShellCommandTypeStdout, workspaceID, runID, s.emitRuntimeEvent)
		}()
		go func() {
			defer wg.Done()
			scanLines(stderr, ShellCommandTypeStderr, workspaceID, runID, s.emitRuntimeEvent)
		}()

		wg.Wait()

		err = cmd.Wait()
		duration := time.Since(started)
		timedOut := ctx.Err() == context.DeadlineExceeded
		exitCode := 0
		if err != nil {
			exitCode = -1
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			}
		}

		s.emitRuntimeEvent(ShellRuntimeEventName, ShellCommandEvent{
			WorkspaceID: workspaceID,
			ID:          runID,
			Type:        ShellCommandTypeCompleted,
			Data: ShellCommandCompletedData{
				ExitCode:             exitCode,
				TimedOut:             timedOut,
				DurationMilliseconds: duration.Milliseconds(),
			},
		})
	}()

	return runID, nil
}

func (s *SystemService) StopShellCommand(workspaceID, runID string) error {
	s.chatMu.Lock()
	cancel, ok := s.shellCommandRuns[runID]
	if !ok {
		s.chatMu.Unlock()
		return fmt.Errorf("shell command run %q not found", runID)
	}
	cancel()
	delete(s.shellCommandRuns, runID)
	s.chatMu.Unlock()
	return nil
}

func (s *SystemService) cleanupShellRun(runID string) {
	s.chatMu.Lock()
	delete(s.shellCommandRuns, runID)
	s.chatMu.Unlock()
}

func resolveShellWorkingDir(workspace Workspace, requestedPath string) (string, error) {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		// Default to first workspace folder
		if len(workspace.Folders) == 0 {
			return "", fmt.Errorf("workspace has no folders")
		}
		return workspace.Folders[0].Path, nil
	}

	// Resolve labeled path
	label, relativePath := splitLabeledPath(requestedPath)
	if label != "" {
		for _, folder := range workspace.Folders {
			if strings.EqualFold(folder.Label, label) {
				resolved := filepath.Clean(filepath.Join(folder.Path, relativePath))
				info, err := os.Stat(resolved)
				if err != nil {
					return "", fmt.Errorf("working directory not found: %w", err)
				}
				if !info.IsDir() {
					return "", fmt.Errorf("working directory is not a directory")
				}
				return resolved, nil
			}
		}
		return "", fmt.Errorf("workspace folder %q not found", label)
	}

	// No label: resolve relative to first folder
	if len(workspace.Folders) == 0 {
		return "", fmt.Errorf("workspace has no folders")
	}
	resolved := filepath.Clean(filepath.Join(workspace.Folders[0].Path, requestedPath))
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("working directory not found: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory is not a directory")
	}
	return resolved, nil
}

func splitLabeledPath(path string) (string, string) {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimPrefix(path, "./")
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return "", "."
	}
	parts := strings.SplitN(path, "/", 2)
	relativePath := "."
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		relativePath = filepath.FromSlash(parts[1])
	}
	return parts[0], relativePath
}

func resolveShellInvocation(command string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		if shell, err := exec.LookPath("pwsh.exe"); err == nil {
			return shell, []string{"-NoProfile", "-NonInteractive", "-Command", command}, nil
		}
		if shell, err := exec.LookPath("powershell.exe"); err == nil {
			return shell, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command}, nil
		}
		return "", nil, fmt.Errorf("PowerShell was not found")
	}
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" && filepath.IsAbs(shell) {
		return shell, []string{"-c", command}, nil
	}
	return "/bin/sh", []string{"-c", command}, nil
}

func scanLines(reader io.Reader, eventType ShellCommandEventType, workspaceID, runID string, emit func(string, any)) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		emit(ShellRuntimeEventName, ShellCommandEvent{
			WorkspaceID: workspaceID,
			ID:          runID,
			Type:        eventType,
			Data:        line,
		})
	}
}

func configureProcess(cmd *exec.Cmd) {
	// No-op on Unix; overridden by build tags on Windows
}
