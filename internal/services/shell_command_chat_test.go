package services

import (
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func TestExtractShellConsoleOutputSupportsTypedToolOutput(t *testing.T) {
	call := llm.ToolCall{
		Function: llm.FunctionCall{Name: "shell_command"},
	}
	result := tools.ExecutionResult{
		Output: struct {
			Command              string `json:"command"`
			Stdout               string `json:"stdout"`
			Stderr               string `json:"stderr"`
			ExitCode             int    `json:"exitCode"`
			DurationMilliseconds int64  `json:"durationMilliseconds"`
		}{
			Command:              "go test ./...",
			Stdout:               "ok package",
			Stderr:               "warning",
			ExitCode:             0,
			DurationMilliseconds: 1250,
		},
	}

	output := extractShellConsoleOutput(call, result)
	for _, expected := range []string{"> go test ./...", "ok package", "warning", "exit code: 0", "duration: 1250ms"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected console output to contain %q, got %q", expected, output)
		}
	}
}

func TestExtractShellConsoleOutputIgnoresMissingOutput(t *testing.T) {
	call := llm.ToolCall{
		Function: llm.FunctionCall{Name: "shell_command"},
	}
	if output := extractShellConsoleOutput(call, tools.ExecutionResult{}); output != "" {
		t.Fatalf("expected no console output for a failed shell call without output, got %q", output)
	}
}
