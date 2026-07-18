package services

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
	"sort"
	"strings"
	"time"
)

const (
	kanbanVerificationTimeout     = 300 * time.Second
	kanbanVerificationOutputBytes = 64 * 1024
)

const (
	kanbanVerificationStatusPassed     = "passed"
	kanbanVerificationStatusFailed     = "failed"
	kanbanVerificationStatusSkipped    = "skipped"
	kanbanVerificationStatusUnverified = "unverified"
)

type kanbanVerificationCommand struct {
	Kind             string   `json:"kind"`
	Command          string   `json:"command"`
	Executable       string   `json:"-"`
	Args             []string `json:"-"`
	WorkingDirectory string   `json:"workingDirectory"`
	absoluteDir      string
}

type kanbanVerificationResult struct {
	Command              string `json:"command"`
	WorkingDirectory     string `json:"workingDirectory"`
	ExitCode             int    `json:"exitCode"`
	Stdout               string `json:"stdout,omitempty"`
	Stderr               string `json:"stderr,omitempty"`
	StdoutTruncated      bool   `json:"stdoutTruncated,omitempty"`
	StderrTruncated      bool   `json:"stderrTruncated,omitempty"`
	TimedOut             bool   `json:"timedOut,omitempty"`
	DurationMilliseconds int64  `json:"durationMilliseconds"`
}

type kanbanVerificationReport struct {
	Status       string                      `json:"status"`
	Message      string                      `json:"message"`
	ChangedPaths []string                    `json:"changedPaths,omitempty"`
	Commands     []kanbanVerificationCommand `json:"commands,omitempty"`
	Results      []kanbanVerificationResult  `json:"results,omitempty"`
}

type packageManifest struct {
	Scripts map[string]string `json:"scripts"`
}

func (s *SystemService) runKanbanVerification(ctx context.Context, workspace Workspace, changedPaths []string) (kanbanVerificationReport, error) {
	changedPaths = normalizedChangedPaths(changedPaths)
	report := kanbanVerificationReport{ChangedPaths: changedPaths}
	if len(changedPaths) == 0 {
		report.Status = kanbanVerificationStatusSkipped
		report.Message = "Verification skipped: no file changes were recorded."
		return report, nil
	}

	commands := detectKanbanVerificationCommands(workspace, changedPaths)
	report.Commands = commands
	if len(commands) == 0 {
		report.Status = kanbanVerificationStatusUnverified
		report.Message = "Unverified: no matching verification command was detected for the changed files."
		return report, nil
	}

	for _, command := range commands {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		result, err := runKanbanVerificationCommand(ctx, command)
		report.Results = append(report.Results, result)
		if err != nil {
			return report, err
		}
		if result.ExitCode != 0 || result.TimedOut {
			report.Status = kanbanVerificationStatusFailed
			report.Message = "Verification failed."
			return report, nil
		}
	}

	report.Status = kanbanVerificationStatusPassed
	report.Message = "Verification passed."
	return report, nil
}

func detectKanbanVerificationCommands(workspace Workspace, changedPaths []string) []kanbanVerificationCommand {
	if command, ok := customKanbanVerificationCommand(workspace); ok {
		return []kanbanVerificationCommand{command}
	}
	commands := make([]kanbanVerificationCommand, 0)
	seen := map[string]bool{}
	for _, changedPath := range normalizedChangedPaths(changedPaths) {
		absolute, err := resolveWorkspaceServicePath(workspace, changedPath)
		if err != nil {
			continue
		}
		folder, err := workspaceFolderForAbsolutePath(workspace, absolute)
		if err != nil {
			continue
		}
		root, err := workspaceFolderAbsolutePath(folder)
		if err != nil {
			continue
		}

		if goVerificationRelevant(changedPath) {
			if projectRoot, ok := nearestMarkerRoot(absolute, root, "go.mod"); ok {
				key := "go\x00" + strings.ToLower(projectRoot)
				if !seen[key] {
					seen[key] = true
					commands = append(commands, kanbanVerificationCommand{
						Kind:             "go",
						Command:          "go test ./...",
						Executable:       "go",
						Args:             []string{"test", "./..."},
						WorkingDirectory: workspaceRelativePath(workspace, projectRoot),
						absoluteDir:      projectRoot,
					})
				}
			}
		}

		if nodeVerificationRelevant(changedPath) {
			if projectRoot, ok := nearestMarkerRoot(absolute, root, "package.json"); ok {
				command, ok := nodeVerificationCommand(workspace, projectRoot)
				if !ok {
					continue
				}
				key := "node\x00" + strings.ToLower(projectRoot)
				if !seen[key] {
					seen[key] = true
					commands = append(commands, command)
				}
			}
		}
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].WorkingDirectory != commands[j].WorkingDirectory {
			return commands[i].WorkingDirectory < commands[j].WorkingDirectory
		}
		return commands[i].Command < commands[j].Command
	})
	return commands
}

func customKanbanVerificationCommand(workspace Workspace) (kanbanVerificationCommand, bool) {
	command := normalizeWorkspaceBuildCommand(workspace.BuildCommand)
	if command == "" {
		return kanbanVerificationCommand{}, false
	}
	root, ok := firstAvailableWorkspaceFolderPath(workspace)
	if !ok {
		return kanbanVerificationCommand{}, false
	}
	return kanbanVerificationCommand{
		Kind:             "custom",
		Command:          command,
		WorkingDirectory: workspaceRelativePath(workspace, root),
		absoluteDir:      root,
	}, true
}

func nodeVerificationCommand(workspace Workspace, projectRoot string) (kanbanVerificationCommand, bool) {
	data, err := os.ReadFile(filepath.Join(projectRoot, "package.json"))
	if err != nil {
		return kanbanVerificationCommand{}, false
	}
	var manifest packageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return kanbanVerificationCommand{}, false
	}
	if strings.TrimSpace(manifest.Scripts["test"]) != "" {
		return kanbanVerificationCommand{
			Kind:             "node",
			Command:          "npm test",
			Executable:       "npm",
			Args:             []string{"test"},
			WorkingDirectory: workspaceRelativePath(workspace, projectRoot),
			absoluteDir:      projectRoot,
		}, true
	}
	if strings.TrimSpace(manifest.Scripts["build"]) != "" {
		return kanbanVerificationCommand{
			Kind:             "node",
			Command:          "npm run build",
			Executable:       "npm",
			Args:             []string{"run", "build"},
			WorkingDirectory: workspaceRelativePath(workspace, projectRoot),
			absoluteDir:      projectRoot,
		}, true
	}
	return kanbanVerificationCommand{}, false
}

func runKanbanVerificationCommand(ctx context.Context, command kanbanVerificationCommand) (kanbanVerificationResult, error) {
	commandContext, cancel := context.WithTimeout(ctx, kanbanVerificationTimeout)
	defer cancel()

	stdout := newKanbanVerificationBuffer(kanbanVerificationOutputBytes)
	stderr := newKanbanVerificationBuffer(kanbanVerificationOutputBytes)
	cmd, err := kanbanVerificationExecCommand(commandContext, command)
	if err != nil {
		return kanbanVerificationResult{
			Command:          command.Command,
			WorkingDirectory: command.WorkingDirectory,
			ExitCode:         -1,
			Stderr:           err.Error(),
		}, nil
	}
	cmd.Dir = command.absoluteDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	configureWorkspaceCommandProcess(cmd)

	started := time.Now()
	runErr := cmd.Run()
	duration := time.Since(started)
	timedOut := commandContext.Err() == context.DeadlineExceeded
	result := kanbanVerificationResult{
		Command:              command.Command,
		WorkingDirectory:     command.WorkingDirectory,
		ExitCode:             0,
		Stdout:               stdout.String(),
		Stderr:               stderr.String(),
		StdoutTruncated:      stdout.Truncated(),
		StderrTruncated:      stderr.Truncated(),
		TimedOut:             timedOut,
		DurationMilliseconds: duration.Milliseconds(),
	}
	if ctx.Err() != nil && !timedOut {
		return result, ctx.Err()
	}
	if runErr == nil {
		return result, nil
	}
	result.ExitCode = -1
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if result.Stderr == "" {
		result.Stderr = runErr.Error()
	}
	return result, nil
}

func kanbanVerificationExecCommand(ctx context.Context, command kanbanVerificationCommand) (*exec.Cmd, error) {
	if command.Executable != "" {
		return exec.CommandContext(ctx, command.Executable, command.Args...), nil
	}
	shellName, shellArgs, err := kanbanVerificationShellInvocation(command.Command)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, shellName, shellArgs...), nil
}

func kanbanVerificationShellInvocation(command string) (string, []string, error) {
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

func nearestMarkerRoot(path string, workspaceRoot string, marker string) (string, bool) {
	path = filepath.Clean(path)
	workspaceRoot = filepath.Clean(workspaceRoot)
	dir := path
	if filepath.Base(path) != marker {
		dir = filepath.Dir(path)
	}
	for {
		relative, err := filepath.Rel(workspaceRoot, dir)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", false
		}
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return dir, true
		}
		if samePath(dir, workspaceRoot) {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func goVerificationRelevant(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == "go.mod" || base == "go.sum" {
		return true
	}
	return strings.EqualFold(filepath.Ext(path), ".go")
}

func nodeVerificationRelevant(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "tsconfig.json",
		"vite.config.js", "vite.config.mjs", "vite.config.cjs", "vite.config.ts", "webpack.config.js", "rollup.config.js":
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".css", ".scss", ".sass", ".less", ".html", ".json", ".vue", ".svelte":
		return true
	default:
		return false
	}
}

func normalizedChangedPaths(paths []string) []string {
	seen := map[string]bool{}
	output := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		output = append(output, path)
	}
	sort.Strings(output)
	return output
}

func samePath(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if strings.EqualFold(left, right) {
		return true
	}
	return left == right
}

func kanbanVerificationReportSucceeded(report kanbanVerificationReport) bool {
	return report.Status != kanbanVerificationStatusFailed
}

func kanbanVerificationProgressTitle(report kanbanVerificationReport, attempt int) string {
	switch report.Status {
	case kanbanVerificationStatusPassed:
		return "Verification passed"
	case kanbanVerificationStatusFailed:
		return fmt.Sprintf("Verification failed (attempt %d)", attempt)
	case kanbanVerificationStatusSkipped:
		return "Verification skipped"
	case kanbanVerificationStatusUnverified:
		return "Verification warning"
	default:
		return "Verification"
	}
}

func kanbanVerificationReportText(report kanbanVerificationReport) string {
	var builder strings.Builder
	builder.WriteString(report.Message)
	if len(report.ChangedPaths) > 0 {
		builder.WriteString("\n\nChanged paths:")
		for _, path := range report.ChangedPaths {
			builder.WriteString("\n- ")
			builder.WriteString(path)
		}
	}
	if len(report.Commands) > 0 {
		builder.WriteString("\n\nVerification commands:")
		for _, command := range report.Commands {
			builder.WriteString("\n- ")
			builder.WriteString(command.Command)
			builder.WriteString(" (")
			builder.WriteString(command.WorkingDirectory)
			builder.WriteString(")")
		}
	}
	if len(report.Results) > 0 {
		builder.WriteString("\n\nResults:")
		for _, result := range report.Results {
			fmt.Fprintf(&builder, "\n\n$ %s\nworkingDirectory: %s\nexitCode: %d\ndurationMilliseconds: %d", result.Command, result.WorkingDirectory, result.ExitCode, result.DurationMilliseconds)
			if result.TimedOut {
				builder.WriteString("\ntimedOut: true")
			}
			appendVerificationStream(&builder, "stdout", result.Stdout, result.StdoutTruncated)
			appendVerificationStream(&builder, "stderr", result.Stderr, result.StderrTruncated)
		}
	}
	return builder.String()
}

func kanbanVerificationRepairPrompt(report kanbanVerificationReport) string {
	return "Automatic verification failed. Fix the card using the available tools, then provide a final handoff summary after the fix.\n\n" + kanbanVerificationReportText(report)
}

func appendVerificationStream(builder *strings.Builder, label string, content string, truncated bool) {
	content = strings.TrimSpace(content)
	if content == "" && !truncated {
		return
	}
	builder.WriteString("\n")
	builder.WriteString(label)
	builder.WriteString(":\n")
	if content != "" {
		builder.WriteString(content)
		builder.WriteString("\n")
	}
	if truncated {
		builder.WriteString("... ")
		builder.WriteString(label)
		builder.WriteString(" truncated by Echo ...\n")
	}
}

type kanbanVerificationBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newKanbanVerificationBuffer(limit int) *kanbanVerificationBuffer {
	return &kanbanVerificationBuffer{limit: limit}
}

func (b *kanbanVerificationBuffer) Write(data []byte) (int, error) {
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

func (b *kanbanVerificationBuffer) String() string {
	return b.buffer.String()
}

func (b *kanbanVerificationBuffer) Truncated() bool {
	return b.truncated
}
