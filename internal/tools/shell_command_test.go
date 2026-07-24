package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestShellCommandExecutesInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"shell_command",
		mustJSON(t, map[string]any{"command": testEchoCommand("hello shell")}),
	)

	if !result.Success {
		t.Fatalf("shell command failed: %#v", result)
	}
	output, ok := result.Output.(shellCommandOutput)
	if !ok {
		t.Fatalf("unexpected shell output type: %#v", result.Output)
	}
	if output.ExitCode != 0 {
		t.Fatalf("expected zero exit code, got %#v", output)
	}
	if !strings.Contains(output.Stdout, "hello shell") {
		t.Fatalf("expected stdout to contain command output, got %#v", output)
	}
	if output.WorkingDirectory != "." {
		t.Fatalf("expected workspace root working directory, got %#v", output)
	}
}

func TestShellCommandReturnsNonZeroExitCodeAsOutput(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"shell_command",
		mustJSON(t, map[string]any{"command": testExitCommand(7)}),
	)

	if !result.Success {
		t.Fatalf("non-zero command should still produce shell output: %#v", result)
	}
	output := result.Output.(shellCommandOutput)
	if output.ExitCode != 7 {
		t.Fatalf("expected exit code 7, got %#v", output)
	}
}

func TestShellCommandRunsWhenChangeTrackingExceedsBudget(t *testing.T) {
	workspace := t.TempDir()
	originalTimeout := shellChangeTrackingTimeout
	shellChangeTrackingTimeout = time.Nanosecond
	t.Cleanup(func() {
		shellChangeTrackingTimeout = originalTimeout
	})

	result := Execute(
		ExecutionContext{
			Context:       context.Background(),
			WorkspacePath: workspace,
			FileChanges:   func([]FileChange) {},
		},
		"shell_command",
		mustJSON(t, map[string]any{"command": testEchoCommand("tracking skipped")}),
	)

	if !result.Success {
		t.Fatalf("shell command should run when best-effort change tracking times out: %#v", result)
	}
	output := result.Output.(shellCommandOutput)
	if !strings.Contains(output.Stdout, "tracking skipped") {
		t.Fatalf("expected command output after tracking timeout, got %#v", output)
	}
}

func TestShellCommandRejectsWorkingDirectoryOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"shell_command",
		mustJSON(t, map[string]any{"command": testEchoCommand("nope"), "workingDirectory": ".."}),
	)

	if result.Success || result.Error == nil || result.Error.Code != "path_outside_workspace" {
		t.Fatalf("expected path safety error, got %#v", result)
	}
}

func TestShellCommandUsesRequestedWorkingDirectory(t *testing.T) {
	workspace := t.TempDir()
	subdir := filepath.Join(workspace, "src")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	expectedDirectory, err := filepath.EvalSymlinks(subdir)
	if err != nil {
		t.Fatal(err)
	}

	result := Execute(
		ExecutionContext{Context: context.Background(), WorkspacePath: workspace},
		"shell_command",
		mustJSON(t, map[string]any{"command": testPrintWorkingDirectoryCommand(), "workingDirectory": "src"}),
	)

	if !result.Success {
		t.Fatalf("shell command failed: %#v", result)
	}
	output := result.Output.(shellCommandOutput)
	if output.WorkingDirectory != "src" {
		t.Fatalf("expected relative working directory src, got %#v", output)
	}
	if !strings.Contains(strings.TrimSpace(output.Stdout), expectedDirectory) {
		t.Fatalf("expected command to run in %q, got %#v", expectedDirectory, output)
	}
}

func TestShellCommandScopesChangeTrackingToWorkingDirectory(t *testing.T) {
	workspace := t.TempDir()
	subdir := filepath.Join(workspace, "src")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	var changes []FileChange
	command := "printf 'generated\\n' > generated.txt"
	if runtime.GOOS == "windows" {
		command = "Set-Content -LiteralPath generated.txt -Value generated"
	}
	result := Execute(
		ExecutionContext{
			Context:       context.Background(),
			WorkspacePath: workspace,
			FileChanges: func(captured []FileChange) {
				changes = append(changes, captured...)
			},
		},
		"shell_command",
		mustJSON(t, map[string]any{
			"command":          command,
			"workingDirectory": "src",
		}),
	)

	if !result.Success {
		t.Fatalf("shell command failed: %#v", result)
	}
	if len(changes) != 1 || changes[0].Operation != FileChangeCreated || changes[0].Path != "src/generated.txt" {
		t.Fatalf("expected one scoped generated-file change, got %#v", changes)
	}
}

func TestShellCommandUsesWorkspaceRootLabels(t *testing.T) {
	base := t.TempDir()
	appRoot := filepath.Join(base, "app")
	docsRoot := filepath.Join(base, "docs")
	for _, path := range []string{appRoot, docsRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	expectedApp, err := filepath.EvalSymlinks(appRoot)
	if err != nil {
		t.Fatal(err)
	}
	expectedDocs, err := filepath.EvalSymlinks(docsRoot)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ExecutionContext{
		Context: context.Background(),
		WorkspaceRoots: []WorkspaceRoot{
			{Label: "app", Path: appRoot},
			{Label: "docs", Path: docsRoot},
		},
	}

	defaultResult := Execute(ctx, "shell_command", mustJSON(t, map[string]any{"command": testPrintWorkingDirectoryCommand()}))
	if !defaultResult.Success {
		t.Fatalf("default shell command failed: %#v", defaultResult)
	}
	defaultOutput := defaultResult.Output.(shellCommandOutput)
	if defaultOutput.WorkingDirectory != "app" || !strings.Contains(strings.TrimSpace(defaultOutput.Stdout), expectedApp) {
		t.Fatalf("expected default working directory app, got %#v", defaultOutput)
	}

	docsResult := Execute(ctx, "shell_command", mustJSON(t, map[string]any{
		"command":          testPrintWorkingDirectoryCommand(),
		"workingDirectory": "docs",
	}))
	if !docsResult.Success {
		t.Fatalf("labeled shell command failed: %#v", docsResult)
	}
	docsOutput := docsResult.Output.(shellCommandOutput)
	if docsOutput.WorkingDirectory != "docs" || !strings.Contains(strings.TrimSpace(docsOutput.Stdout), expectedDocs) {
		t.Fatalf("expected labeled working directory docs, got %#v", docsOutput)
	}
}

func testEchoCommand(text string) string {
	if runtime.GOOS == "windows" {
		return "Write-Output '" + text + "'"
	}
	return "echo '" + text + "'"
}

func testExitCommand(code int) string {
	return "exit " + strconv.Itoa(code)
}

func testPrintWorkingDirectoryCommand() string {
	if runtime.GOOS == "windows" {
		return "Get-Location | Select-Object -ExpandProperty Path"
	}
	return "pwd"
}
