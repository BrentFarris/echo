package services

import (
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestRecoverToolResultContextReplacesLargestResultAndImage(t *testing.T) {
	smallCall := llm.ToolCall{ID: "small", Function: llm.FunctionCall{Name: "filesystem_stat"}}
	largeCall := llm.ToolCall{ID: "large", Function: llm.FunctionCall{Name: "filesystem_read_image"}}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{smallCall, largeCall}},
		{Role: llm.RoleTool, ToolCallID: smallCall.ID, Content: `{"output":"small"}`},
		{Role: llm.RoleTool, ToolCallID: largeCall.ID, Content: `{"output":"image"}`},
		{
			Role:    llm.RoleUser,
			Content: "image",
			ContentParts: []llm.MessageContentPart{
				llm.TextContentPart("image"),
				llm.ImageURLContentPart("data:image/png;base64," + strings.Repeat("a", 1000)),
			},
		},
	}

	recovery, ok := recoverToolResultContext(messages, map[string]bool{"small": true, "large": true})
	if !ok {
		t.Fatal("expected tool result recovery")
	}
	if recovery.Call.ID != largeCall.ID {
		t.Fatalf("expected largest result to be replaced, got %q", recovery.Call.ID)
	}
	if len(recovery.Messages) != 4 {
		t.Fatalf("expected image message to be removed, got %#v", recovery.Messages)
	}
	if !strings.Contains(recovery.ResultMessage.Content, toolResultContextErrorCode) {
		t.Fatalf("expected context error result, got %q", recovery.ResultMessage.Content)
	}

	second, ok := recoverToolResultContext(recovery.Messages, map[string]bool{"small": true, "large": true})
	if !ok || second.Call.ID != smallCall.ID {
		t.Fatalf("expected a later retry to replace the remaining result, got %#v, %v", second.Call, ok)
	}
}
