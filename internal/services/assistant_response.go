package services

import (
	"fmt"
	"strings"

	"github.com/brent/echo/internal/llm"
)

const maxEmptyAssistantRetries = 2

func isEmptyAssistantResponse(content string, toolCalls []llm.ToolCall) bool {
	return strings.TrimSpace(content) == "" && len(toolCalls) == 0
}

func emptyAssistantRetryMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "Your previous response completed without visible content or a tool call. " +
			"Continue from your existing reasoning and now return either a visible final answer or a valid tool call. " +
			"Do not return another reasoning-only response.",
	}
}

func emptyAssistantResponseError() error {
	return fmt.Errorf(
		"The LLM completed %d times without producing visible content or a tool call. "+
			"It may be exhausting its output budget on reasoning.",
		maxEmptyAssistantRetries+1,
	)
}
