package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
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
