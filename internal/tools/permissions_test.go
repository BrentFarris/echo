package tools

import (
	"encoding/json"
	"testing"
)

func TestRegistryScopesFilterSchemaAndExecution(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"read", "write"} {
		name := name
		MustRegister(registry, ToolFunc{
			Meta: Metadata{Name: name},
			Run:  func(ExecutionContext, json.RawMessage) (any, error) { return name, nil },
		})
	}
	scopes := NewToolScopeChecker([]ToolPermission{{Name: "read"}})
	schema := registry.LLMSchemaForScopes(scopes)
	if len(schema) != 1 || schema[0].Function.Name != "read" {
		t.Fatalf("unexpected filtered schema: %+v", schema)
	}
	result := registry.Execute(ExecutionContext{ToolScopes: scopes}, "write", nil)
	if result.Success || result.Error == nil || result.Error.Code != "tool_not_allowed" {
		t.Fatalf("expected tool_not_allowed, got %+v", result)
	}
}

func TestToolScopesEnforcePathGlobs(t *testing.T) {
	scopes := NewToolScopeChecker([]ToolPermission{{Name: "read", Paths: []string{"src/**", "README.md"}}})
	if !scopes.Allowed("read", "src/nested/file.go") || !scopes.Allowed("read", "README.md") {
		t.Fatal("expected configured paths to be allowed")
	}
	if scopes.Allowed("read", "secrets/key.txt") {
		t.Fatal("unexpected path permission")
	}
}
