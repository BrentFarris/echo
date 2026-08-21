package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/llm"
)

const (
	maxEmptyAssistantRetries  = 2
	maxTransientStreamRetries = 1
)

func finishReasonError(finishReason string, hasToolCalls bool) error {
	switch strings.TrimSpace(finishReason) {
	case "", "stop":
		return nil
	case "tool_calls":
		if hasToolCalls {
			return nil
		}
		return errors.New("The LLM stopped to call a tool, but no tool call was received. Try again.")
	case "length":
		return errors.New("The LLM stopped before completing its response because it hit the token limit. Increase Max Tokens or reduce the thinking-token budget and try again.")
	case "content_filter":
		return errors.New("The LLM stopped because the provider filtered the response.")
	default:
		return fmt.Errorf("The LLM stopped before completing normally (finish_reason: %s).", finishReason)
	}
}

func isEmptyAssistantResponse(content string, toolCalls []llm.ToolCall) bool {
	return strings.TrimSpace(content) == "" && len(toolCalls) == 0
}

func emptyAssistantRetryMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "Your previous response completed without visible content or a tool call. " +
			"Continue from the current context and now return either a visible final answer or a valid tool call. " +
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

func transientStreamRetryMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "The previous model stream ended before returning usable content. " +
			"Retry once from the current context and produce a valid tool call or final answer.",
	}
}
