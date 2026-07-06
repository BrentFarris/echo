package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
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
	for _, name := range []string{"filesystem_create_text", "filesystem_delete_file", "filesystem_edit_text", "filesystem_list", "filesystem_read_image", "filesystem_read_text", "filesystem_read_video", "filesystem_search_text", "filesystem_search_workspace", "filesystem_stat", "lsp_query", "shell_command", "web_fetch", "web_search", "workspace_context", "workspace_skill_read", "workspace_skill_record", "workspace_skill_search"} {
		if !names[name] {
			t.Fatalf("expected default registry to include %s, got %#v", name, names)
		}
	}
}

func TestLLMSchemaTeachesLabeledWorkspacePaths(t *testing.T) {
	schema := LLMSchema()
	expectations := map[string][]string{
		"filesystem_create_text":      {"path"},
		"filesystem_delete_file":      {"path"},
		"filesystem_edit_text":        {"path"},
		"filesystem_list":             {"path"},
		"filesystem_read_image":       {"path"},
		"filesystem_read_video":       {"path"},
		"filesystem_read_text":        {"path"},
		"filesystem_search_text":      {"path"},
		"filesystem_search_workspace": {"path"},
		"filesystem_stat":             {"path"},
		"lsp_query":                   {"path"},
		"shell_command":               {"workingDirectory"},
		"workspace_context":           {"path", "changedPaths"},
	}

	for toolName, fields := range expectations {
		properties := toolSchemaProperties(t, schema, toolName)
		for _, field := range fields {
			property, ok := properties[field].(map[string]any)
			if !ok {
				t.Fatalf("expected %s.%s schema property, got %#v", toolName, field, properties[field])
			}
			description, _ := property["description"].(string)
			for _, expected := range []string{
				"workspace folder label",
				"echo/frontend/src/main.ts",
				"frontend/src/main.ts",
			} {
				if !strings.Contains(description, expected) {
					t.Fatalf("expected %s.%s description to contain %q, got %q", toolName, field, expected, description)
				}
			}
		}
	}
}

func TestFilesystemReadTextSchemaTeachesLineRanges(t *testing.T) {
	properties := toolSchemaProperties(t, LLMSchema(), "filesystem_read_text")
	for _, field := range []string{"aroundLine", "contextLines", "startLine", "lineCount"} {
		property, ok := properties[field].(map[string]any)
		if !ok {
			t.Fatalf("expected filesystem_read_text.%s schema property, got %#v", field, properties[field])
		}
		description, _ := property["description"].(string)
		if !strings.Contains(description, "line") {
			t.Fatalf("expected filesystem_read_text.%s description to mention lines, got %q", field, description)
		}
	}
}

func TestShellCommandSchemaTeachesPowerShellOnWindows(t *testing.T) {
	properties := toolSchemaProperties(t, LLMSchema(), "shell_command")
	command, ok := properties["command"].(map[string]any)
	if !ok {
		t.Fatalf("expected shell_command.command schema property, got %#v", properties["command"])
	}
	description, _ := command["description"].(string)
	for _, expected := range []string{
		"PowerShell",
		"not cmd.exe",
		"PowerShell-native syntax",
		"avoid CMD syntax",
		"Get-ChildItem",
		"$env:VAR",
	} {
		if !strings.Contains(description, expected) {
			t.Fatalf("expected shell_command.command description to contain %q, got %q", expected, description)
		}
	}
}

func TestReadOnlyLLMSchemaIncludesOnlyInspectionTools(t *testing.T) {
	schema := ReadOnlyLLMSchema()
	names := make(map[string]bool, len(schema))
	for _, tool := range schema {
		names[tool.Function.Name] = true
	}

	for _, name := range []string{"filesystem_list", "filesystem_read_image", "filesystem_read_video", "filesystem_read_text", "filesystem_search_text", "filesystem_search_workspace", "filesystem_stat", "git_inspect", "lsp_query", "web_search", "workspace_context", "workspace_skill_read", "workspace_skill_search"} {
		if !names[name] {
			t.Fatalf("expected read-only schema to include %s, got %#v", name, names)
		}
	}
	for _, name := range []string{"filesystem_create_text", "filesystem_delete_file", "filesystem_edit_text", "shell_command", "web_fetch", "workspace_skill_record"} {
		if names[name] {
			t.Fatalf("expected read-only schema to exclude %s, got %#v", name, names)
		}
	}
	if len(names) != 13 {
		t.Fatalf("expected exactly thirteen read-only tools, got %#v", names)
	}
}

func toolSchemaProperties(t *testing.T, schema []llm.Tool, name string) map[string]any {
	t.Helper()
	for _, tool := range schema {
		if tool.Function.Name != name {
			continue
		}
		properties, ok := tool.Function.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("expected %s schema properties, got %#v", name, tool.Function.Parameters)
		}
		return properties
	}
	t.Fatalf("tool %s was not found", name)
	return nil
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

func TestToolPermissionCheckerRejectsDisallowedTools(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("read_file", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "content", nil
	}))
	MustRegister(registry, testTool("write_file", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "ok", nil
	}))

	// Restrict to read-only tools.
	checker := NewToolPermissionChecker([]string{"read_file"})
	ctx := ExecutionContext{
		Context:         context.Background(),
		ToolPermissions: checker,
	}

	// Allowed tool should succeed.
	result := registry.Execute(ctx, "read_file", nil)
	if !result.Success {
		t.Fatalf("expected read_file to succeed, got %#v", result)
	}

	// Disallowed tool should be rejected before execution.
	result = registry.Execute(ctx, "write_file", nil)
	if result.Success {
		t.Fatalf("expected write_file to be rejected, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "tool_not_allowed" {
		t.Fatalf("expected tool_not_allowed error, got %#v", result.Error)
	}
}

func TestToolPermissionCheckerEmptyAllowlistAllowsAll(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("any_tool", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "ok", nil
	}))

	// Empty checker allows all.
	checker := NewToolPermissionChecker(nil)
	ctx := ExecutionContext{
		Context:         context.Background(),
		ToolPermissions: checker,
	}

	result := registry.Execute(ctx, "any_tool", nil)
	if !result.Success {
		t.Fatalf("expected any_tool to succeed with empty allowlist, got %#v", result)
	}
}

func TestNilPermissionCheckersAllowAll(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("any_tool", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "ok", nil
	}))

	// No permission checkers at all — should allow everything.
	ctx := ExecutionContext{Context: context.Background()}
	result := registry.Execute(ctx, "any_tool", nil)
	if !result.Success {
		t.Fatalf("expected any_tool to succeed with no permissions, got %#v", result)
	}
}

func TestPathPermissionCheckerRejectsOutOfScopePaths(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("read_file", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "content", nil
	}))

	// Only allow paths under src/.
	matcher := NewPathMatcher([]string{"src/**"})
	ctx := ExecutionContext{
		Context:         context.Background(),
		WorkspaceRoots:  []WorkspaceRoot{{Label: "app", Path: t.TempDir()}},
		PathPermissions: matcher,
	}

	// Allowed path.
	args := json.RawMessage(`{"path":"app/src/main.ts"}`)
	result := registry.Execute(ctx, "read_file", args)
	if !result.Success {
		t.Fatalf("expected allowed path to succeed, got %#v", result)
	}

	// Disallowed path.
	args = json.RawMessage(`{"path":"app/tests/test.ts"}`)
	result = registry.Execute(ctx, "read_file", args)
	if result.Success {
		t.Fatalf("expected disallowed path to be rejected, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "path_not_allowed" {
		t.Fatalf("expected path_not_allowed error, got %#v", result.Error)
	}
}

func TestPathPermissionCheckerWithLabeledPaths(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("read_file", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "content", nil
	}))

	matcher := NewPathMatcher([]string{"frontend/src/**"})
	ctx := ExecutionContext{
		Context:         context.Background(),
		WorkspaceRoots:  []WorkspaceRoot{{Label: "echo", Path: t.TempDir()}},
		PathPermissions: matcher,
	}

	// Labeled path that matches.
	args := json.RawMessage(`{"path":"echo/frontend/src/app.ts"}`)
	result := registry.Execute(ctx, "read_file", args)
	if !result.Success {
		t.Fatalf("expected labeled allowed path to succeed, got %#v", result)
	}

	// Labeled path that doesn't match.
	args = json.RawMessage(`{"path":"echo/internal/service.go"}`)
	result = registry.Execute(ctx, "read_file", args)
	if result.Success {
		t.Fatalf("expected labeled disallowed path to be rejected, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "path_not_allowed" {
		t.Fatalf("expected path_not_allowed error, got %#v", result.Error)
	}
}

func TestPathPermissionCheckerAllowsPlainRelativePaths(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("read_file", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "content", nil
	}))

	matcher := NewPathMatcher([]string{"src/**"})
	ctx := ExecutionContext{
		Context:         context.Background(),
		WorkspaceRoots:  []WorkspaceRoot{{Label: ".", Path: t.TempDir()}},
		PathPermissions: matcher,
	}

	// Plain relative path matching.
	args := json.RawMessage(`{"path":"src/main.go"}`)
	result := registry.Execute(ctx, "read_file", args)
	if !result.Success {
		t.Fatalf("expected plain allowed path to succeed, got %#v", result)
	}

	// Plain relative path not matching.
	args = json.RawMessage(`{"path":"docs/readme.md"}`)
	result = registry.Execute(ctx, "read_file", args)
	if result.Success {
		t.Fatalf("expected plain disallowed path to be rejected, got %#v", result)
	}
}

func TestToolAndPathPermissionsEnforcedTogether(t *testing.T) {
	registry := NewRegistry()
	MustRegister(registry, testTool("read_file", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "content", nil
	}))
	MustRegister(registry, testTool("write_file", func(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
		return "ok", nil
	}))

	// Allow read_file only, and only paths under src/.
	checker := NewToolPermissionChecker([]string{"read_file"})
	matcher := NewPathMatcher([]string{"src/**"})
	ctx := ExecutionContext{
		Context:         context.Background(),
		WorkspaceRoots:  []WorkspaceRoot{{Label: ".", Path: t.TempDir()}},
		ToolPermissions: checker,
		PathPermissions: matcher,
	}

	// Tool allowed + path allowed → success.
	args := json.RawMessage(`{"path":"src/main.go"}`)
	result := registry.Execute(ctx, "read_file", args)
	if !result.Success {
		t.Fatalf("expected both-allowed to succeed, got %#v", result)
	}

	// Tool disallowed → tool_not_allowed (checked first).
	args = json.RawMessage(`{"path":"src/main.go"}`)
	result = registry.Execute(ctx, "write_file", args)
	if result.Error == nil || result.Error.Code != "tool_not_allowed" {
		t.Fatalf("expected tool_not_allowed, got %#v", result.Error)
	}

	// Tool allowed + path disallowed → path_not_allowed.
	args = json.RawMessage(`{"path":"docs/readme.md"}`)
	result = registry.Execute(ctx, "read_file", args)
	if result.Error == nil || result.Error.Code != "path_not_allowed" {
		t.Fatalf("expected path_not_allowed, got %#v", result.Error)
	}
}

func TestExtractWorkspacePathsWithMultiplePathArgs(t *testing.T) {
	ctx := ExecutionContext{
		WorkspaceRoots: []WorkspaceRoot{{Label: ".", Path: t.TempDir()}},
	}
	args := json.RawMessage(`{"path":"src/main.go","workingDirectory":"."}`)
	paths := extractWorkspacePaths(ctx, args)

	var foundMain, foundDot bool
	for _, p := range paths {
		if p == "src/main.go" {
			foundMain = true
		}
		if p == "." {
			foundDot = true
		}
	}
	if !foundMain {
		t.Fatalf("expected src/main.go in extracted paths, got %#v", paths)
	}
	if foundDot {
		t.Fatalf("expected . to be excluded from extracted paths, got %#v", paths)
	}
}

func TestExtractWorkspacePathsWithInvalidJSON(t *testing.T) {
	ctx := ExecutionContext{WorkspaceRoots: []WorkspaceRoot{{Label: ".", Path: t.TempDir()}}}
	paths := extractWorkspacePaths(ctx, json.RawMessage(`not json`))
	if len(paths) != 0 {
		t.Fatalf("expected no paths from invalid JSON, got %#v", paths)
	}
}

func TestIsPathArgKey(t *testing.T) {
	for _, key := range []string{"path", "workingDirectory", "repository", "base", "revision", "target"} {
		if !isPathArgKey(key) {
			t.Fatalf("expected %s to be a path arg key", key)
		}
	}
	for _, key := range []string{"query", "operation", "content", "limit", "foo"} {
		if isPathArgKey(key) {
			t.Fatalf("expected %s to not be a path arg key", key)
		}
	}
}
