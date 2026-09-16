package tools

import (
	"context"
	"encoding/json"
	"github.com/brent/echo/internal/sandbox"
	"testing"
)

func TestCanonicalUIAdvertisingPreservesExplicitCustomPermissions(t *testing.T) {
	registry := NewRegistry()
	for _, tool := range Registered() {
		MustRegister(registry, tool)
	}
	for _, goal := range []bool{false, true} {
		names := map[string]bool{}
		for _, tool := range registry.ChatLLMSchemaForScopes(nil, ChatSchemaOptions{SandboxGUI: true, GoalMode: goal}) {
			names[tool.Function.Name] = true
		}
		for name := range canonicalUITools {
			if !names[name] {
				t.Errorf("default missing %s", name)
			}
		}
		for name := range legacyUITools {
			if names[name] {
				t.Errorf("default advertises legacy %s", name)
			}
		}
		for _, name := range []string{"browser_open", "browser_tabs", "browser_upload"} {
			if !names[name] {
				t.Errorf("missing browser convenience %s", name)
			}
		}
	}
	custom := NewToolScopeChecker([]ToolPermission{{Name: "browser_snapshot"}, {Name: "browser_click"}})
	schema := registry.ChatLLMSchemaForScopes(custom, ChatSchemaOptions{SandboxGUI: true})
	if len(schema) != 2 {
		t.Fatalf("custom allowlist changed: %+v", schema)
	}
	result := registry.Execute(ExecutionContext{Context: context.Background(), ToolScopes: custom}, "ui_act", json.RawMessage(`{}`))
	if result.Error == nil || result.Error.Code != "tool_not_allowed" {
		t.Fatalf("canonical tool bypassed custom permissions: %+v", result)
	}
	for _, options := range []ChatSchemaOptions{{}, {SandboxGUI: true, PlanMode: true}} {
		for _, tool := range registry.ChatLLMSchemaForScopes(nil, options) {
			if canonicalUITools[tool.Function.Name] {
				t.Fatalf("UI tool available outside enabled execution mode: %s", tool.Function.Name)
			}
		}
	}
}

func TestUICanceledExecutionPreservesOutcomeEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := NewRegistry()
	MustRegister(registry, ToolFunc{Meta: Metadata{Name: "ui_act"}, Run: func(ExecutionContext, json.RawMessage) (any, error) {
		cancel()
		return uiOutput{&sandbox.UIResult{Execution: "unknown"}}, nil
	}})
	result := registry.Execute(ExecutionContext{Context: ctx}, "ui_act", json.RawMessage(`{}`))
	output, ok := result.Output.(uiOutput)
	if !result.Success || !ok || output.Execution != "unknown" {
		t.Fatalf("cancellation dropped outcome evidence: %+v", result)
	}
}
