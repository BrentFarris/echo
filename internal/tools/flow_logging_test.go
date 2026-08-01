package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/flowlog"
)

func TestToolExecutionFlowLogCapturesRequestAndResult(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(ToolFunc{
		Meta: Metadata{Name: "trace_tool"},
		Run: func(_ ExecutionContext, arguments json.RawMessage) (any, error) {
			return map[string]any{"result": "exact-tool-output", "arguments": string(arguments)}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "echo.log")
	logger := flowlog.NewController()
	if err := logger.Enable(path); err != nil {
		t.Fatal(err)
	}
	result := registry.Execute(ExecutionContext{
		FlowLog:    logger,
		ToolCallID: "call-exact",
	}, "trace_tool", json.RawMessage(`{"input":"exact-tool-input"}`))
	if !result.Success {
		t.Fatalf("tool failed: %#v", result)
	}
	if err := logger.Disable(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"tool_request", "tool_execution_result", "call-exact", "trace_tool", "exact-tool-input", "exact-tool-output"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("tool log missing %q: %s", expected, text)
		}
	}
}
