package services

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInlineToolCallParserExtractsTaggedFunctionParameters(t *testing.T) {
	command := `Get-ChildItem -Path "C:\Users\brent\Documents\git\kaiju\src\editor" -Recurse -Include .css,.go.html | Select-Object -ExpandProperty FullName`
	parser := inlineToolCallStreamParser{}

	first := parser.Consume("Checking workspace <tool")
	if first.Text != "Checking workspace " || len(first.ToolCalls) != 0 {
		t.Fatalf("expected visible prefix only, got %#v", first)
	}

	second := parser.Consume("_call> <function=shell_command> <parameter=command> " + command + " </parameter> <parameter=timeoutSeconds> 10 </parameter> </function> </tool_call>")
	if second.Text != "" || len(second.ToolCalls) != 1 {
		t.Fatalf("expected one extracted tool call, got %#v", second)
	}
	if strings.Contains(second.Text, "tool_call") {
		t.Fatalf("tool markup leaked into visible text: %q", second.Text)
	}

	call := second.ToolCalls[0]
	if call.Function.Name != "shell_command" {
		t.Fatalf("expected shell_command, got %#v", call)
	}
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args.Command != command || args.TimeoutSeconds != 10 {
		t.Fatalf("unexpected arguments: %#v", args)
	}
}

func TestInlineToolCallParserExtractsJSONToolCall(t *testing.T) {
	parser := inlineToolCallStreamParser{}
	parsed := parser.Consume(`<tool_call>{"name":"filesystem_list","arguments":{"path":".","includeHidden":true}}</tool_call>`)
	if parsed.Text != "" || len(parsed.ToolCalls) != 1 {
		t.Fatalf("expected one JSON tool call, got %#v", parsed)
	}

	call := parsed.ToolCalls[0]
	if call.Function.Name != "filesystem_list" {
		t.Fatalf("expected filesystem_list, got %#v", call)
	}
	var args struct {
		Path          string `json:"path"`
		IncludeHidden bool   `json:"includeHidden"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args.Path != "." || !args.IncludeHidden {
		t.Fatalf("unexpected arguments: %#v", args)
	}
}
