package tools

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestDuplicateToolNamesFailFast(t *testing.T) {
	registry := NewRegistry()
	tool := testTool("duplicate_name", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "ok", nil
	})

	MustRegister(registry, tool)
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected duplicate registration to panic")
		}
	}()
	MustRegister(registry, tool)
}

func TestRegisteredToolsAppearInLLMSchema(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, ToolFunc{
		Meta: Metadata{
			Name:        "inspect_workspace",
			Description: "Inspect workspace files.",
			Parameters: Schema{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
		Run: func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
			return "ok", nil
		},
	})

	schema := registry.LLMSchema()
	if len(schema) != 1 {
		t.Fatalf("expected one schema entry, got %d", len(schema))
	}
	if schema[0].Type != "function" {
		t.Fatalf("expected function tool, got %q", schema[0].Type)
	}
	if schema[0].Function.Name != "inspect_workspace" {
		t.Fatalf("unexpected tool name: %q", schema[0].Function.Name)
	}
	if schema[0].Function.Description != "Inspect workspace files." {
		t.Fatalf("unexpected description: %q", schema[0].Function.Description)
	}
	if schema[0].Function.Parameters["type"] != "object" {
		t.Fatalf("expected parameters to be exposed, got %#v", schema[0].Function.Parameters)
	}
}

func TestToolExecutionReturnsStructuredSuccessAndError(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("success_tool", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return map[string]any{"answer": "done"}, nil
	}))
	MustRegister(registry, testTool("error_tool", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return nil, SafeError{Code: "invalid_arguments", Message: "bad input"}
	}))

	success := registry.Execute(ExecutionContext{Context: context.Background()}, "success_tool", nil)
	if !success.Success {
		t.Fatalf("expected success result, got %#v", success)
	}
	if success.Error != nil {
		t.Fatalf("expected no error, got %#v", success.Error)
	}

	failure := registry.Execute(ExecutionContext{Context: context.Background()}, "error_tool", nil)
	if failure.Success {
		t.Fatalf("expected error result, got %#v", failure)
	}
	if failure.Error == nil || failure.Error.Code != "invalid_arguments" || failure.Error.Message != "bad input" {
		t.Fatalf("unexpected structured error: %#v", failure.Error)
	}

	missing := registry.Execute(ExecutionContext{Context: context.Background()}, "missing_tool", nil)
	if missing.Success || missing.Error == nil || missing.Error.Code != "tool_not_found" {
		t.Fatalf("expected missing tool error, got %#v", missing)
	}
}

func TestCancellationPreventsToolExecutionFromStarting(t *testing.T) {
	registry := NewRegistry()
	var calls atomic.Int32
	MustRegister(registry, testTool("cancel_before_start", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		calls.Add(1)
		return "should not run", nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := registry.Execute(ExecutionContext{Context: ctx}, "cancel_before_start", nil)

	if result.Success || result.Error == nil || result.Error.Code != "canceled" {
		t.Fatalf("expected canceled result, got %#v", result)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected canceled tool not to start, got %d calls", calls.Load())
	}
}

func TestCancellationStopsLongRunningTool(t *testing.T) {
	registry := NewRegistry()
	started := make(chan struct{})
	stopped := make(chan struct{})
	MustRegister(registry, testTool("long_running", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		close(started)
		<-ctx.context().Done()
		close(stopped)
		return nil, ctx.context().Err()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan ExecutionResult, 1)
	go func() {
		results <- registry.Execute(ExecutionContext{Context: ctx}, "long_running", nil)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	cancel()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("tool did not observe cancellation")
	}

	select {
	case result := <-results:
		if result.Success || result.Error == nil || result.Error.Code != "canceled" {
			t.Fatalf("expected canceled result, got %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("tool execution did not return")
	}
}

func TestDefaultRegistryIncludesFilesystemTools(t *testing.T) {
	schema := LLMSchema()
	names := make(map[string]bool, len(schema))
	for _, tool := range schema {
		names[tool.Function.Name] = true
	}
	for _, name := range []string{"filesystem_create_text", "filesystem_delete_file", "filesystem_edit_text", "filesystem_list", "filesystem_read_text", "filesystem_stat", "shell_command"} {
		if !names[name] {
			t.Fatalf("expected default registry to include %s, got %#v", name, names)
		}
	}
}

func testTool(name string, run func(ctx ExecutionContext, arguments json.RawMessage) (any, error)) ToolFunc {
	return ToolFunc{
		Meta: Metadata{
			Name:        name,
			Description: "test tool",
			Parameters:  Schema{"type": "object"},
		},
		Run: run,
	}
}
