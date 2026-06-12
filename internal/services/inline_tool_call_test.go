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

func TestInlineToolCallParserExtractsSentinelCallSyntax(t *testing.T) {
	parser := inlineToolCallStreamParser{}

	first := parser.Consume("Checking <|tool")
	if first.Text != "Checking " || len(first.ToolCalls) != 0 {
		t.Fatalf("expected visible prefix only, got %#v", first)
	}

	second := parser.Consume(`_call>call:filesystem_list{path:<|"|>Public/PlayerController<|"|>}<tool_call|>`)
	if second.Text != "" || len(second.ToolCalls) != 1 {
		t.Fatalf("expected sentinel call to be extracted, got %#v", second)
	}
	if strings.Contains(second.Text, "tool_call") {
		t.Fatalf("tool markup leaked into visible text: %q", second.Text)
	}

	call := second.ToolCalls[0]
	if call.Function.Name != "filesystem_list" {
		t.Fatalf("expected filesystem_list, got %#v", call)
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args.Path != "Public/PlayerController" {
		t.Fatalf("unexpected arguments: %#v", args)
	}
}

func TestInlineToolCallParserExtractsBareFunctionTag(t *testing.T) {
	parser := inlineToolCallStreamParser{}

	first := parser.Consume("Reading <func")
	if first.Text != "Reading " || len(first.ToolCalls) != 0 {
		t.Fatalf("expected visible prefix only, got %#v", first)
	}

	second := parser.Consume("tion=filesystem_read>\n<parameter=path>\necho/frontend/src/styles.css\n</parameter>\n</function>")
	if second.Text != "" || len(second.ToolCalls) != 1 {
		t.Fatalf("expected bare function tag to be extracted, got %#v", second)
	}
	if strings.Contains(second.Text, "function") {
		t.Fatalf("tool markup leaked into visible text: %q", second.Text)
	}

	call := second.ToolCalls[0]
	if call.Function.Name != "filesystem_read_text" {
		t.Fatalf("expected filesystem_read_text, got %#v", call)
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args.Path != "echo/frontend/src/styles.css" {
		t.Fatalf("unexpected arguments: %#v", args)
	}
}

func TestInlineToolCallParserExtractsToolCodeBlock(t *testing.T) {
	parser := inlineToolCallStreamParser{}

	first := parser.Consume("Searching\n<tool_code>filesystem_search</tool_code>\n<path>frontend/src/main.ts</path>\n")
	if first.Text != "Searching\n" || len(first.ToolCalls) != 0 {
		t.Fatalf("expected visible prefix only while tool_code block is open, got %#v", first)
	}

	second := parser.Consume("<pattern>send.*button|is-busy|session\\.busy</pattern>\n<range>[1800, 1840]</range>\nDone")
	if second.Text != "\nDone" || len(second.ToolCalls) != 1 {
		t.Fatalf("expected tool_code block to be extracted with trailing text preserved, got %#v", second)
	}
	if strings.Contains(second.Text, "tool_code") || strings.Contains(second.Text, "pattern") {
		t.Fatalf("tool markup leaked into visible text: %q", second.Text)
	}

	call := second.ToolCalls[0]
	if call.Function.Name != "filesystem_search_text" {
		t.Fatalf("expected filesystem_search_text, got %#v", call)
	}
	var args struct {
		Path  string `json:"path"`
		Query string `json:"query"`
		Range []int  `json:"range"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args.Path != "frontend/src/main.ts" || args.Query != `send.*button|is-busy|session\.busy` || len(args.Range) != 2 || args.Range[0] != 1800 || args.Range[1] != 1840 {
		t.Fatalf("unexpected arguments: %#v", args)
	}
}

func TestInlineToolCallParserFlushesToolCodeBlockWithoutTrailingText(t *testing.T) {
	parser := inlineToolCallStreamParser{}

	parsed := parser.Consume("<tool_code>filesystem_read</tool_code>\n<path>frontend/src/styles.css</path>")
	if parsed.Text != "" || len(parsed.ToolCalls) != 0 {
		t.Fatalf("expected tool_code block to wait for a boundary, got %#v", parsed)
	}

	flushed := parser.Flush()
	if flushed.Text != "" || len(flushed.ToolCalls) != 1 {
		t.Fatalf("expected tool_code block to be extracted on flush, got %#v", flushed)
	}
	call := flushed.ToolCalls[0]
	if call.Function.Name != "filesystem_read_text" {
		t.Fatalf("expected filesystem_read_text, got %#v", call)
	}
}
