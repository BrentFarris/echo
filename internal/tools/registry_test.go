package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFilesystemListSelfRegisters(t *testing.T) {
	found := false
	for _, tool := range Registered() {
		if tool.Metadata().Name == "filesystem_list" {
			found = true
		}
	}
	if !found {
		t.Fatal("filesystem_list was not registered via init")
	}
}

func TestLLMSchemaIncludesFilesystemList(t *testing.T) {
	schema := LLMSchema()
	var list *struct {
		Name        string
		Description string
		Parameters  map[string]any
	}
	for _, tool := range schema {
		if tool.Function.Name == "filesystem_list" {
			list = &struct {
				Name        string
				Description string
				Parameters  map[string]any
			}{tool.Function.Name, tool.Function.Description, tool.Function.Parameters}
		}
	}
	if list == nil {
		t.Fatal("filesystem_list missing from LLMSchema")
	}
	if list.Name != "filesystem_list" {
		t.Fatalf("unexpected name: %q", list.Name)
	}
	if list.Description != "List direct children of a directory inside the active workspace." {
		t.Fatalf("unexpected description: %q", list.Description)
	}
	props, ok := list.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties object, got %#v", list.Parameters)
	}
	for _, field := range []string{"path", "includeHidden"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("expected parameter %q, got %#v", field, props)
		}
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	result := Execute(ExecutionContext{Context: context.Background()}, "does_not_exist", json.RawMessage(`{}`))
	if result.Success {
		t.Fatalf("expected failure, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "tool_not_found" {
		t.Fatalf("expected tool_not_found, got %#v", result.Error)
	}
}

func TestExecutePanicRecovered(t *testing.T) {
	reg := NewRegistry()
	MustRegister(reg, ToolFunc{
		Meta: Metadata{Name: "panic_tool"},
		Run: func(ExecutionContext, json.RawMessage) (any, error) {
			panic("boom")
		},
	})
	result := reg.Execute(ExecutionContext{Context: context.Background()}, "panic_tool", json.RawMessage(`{}`))
	if result.Success {
		t.Fatalf("expected failure, got %#v", result)
	}
	if result.Error == nil || result.Error.Code != "tool_panic" {
		t.Fatalf("expected tool_panic, got %#v", result.Error)
	}
}

func TestRegisterDuplicateName(t *testing.T) {
	reg := NewRegistry()
	MustRegister(reg, ToolFunc{Meta: Metadata{Name: "dup"}})
	err := reg.Register(ToolFunc{Meta: Metadata{Name: "dup"}})
	if err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}
