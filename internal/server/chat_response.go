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
	maxContextLengthRetries   = 3
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

// lengthContinuationMessage asks the model to resume after an output token
// cutoff instead of repeating the whole response. Resuming leaves only the
// unfinished remainder to produce, which is what lets the next request fit.
func lengthContinuationMessage(interruptedTool string) llm.Message {
	content := "Your previous response was cut off because it hit the output token limit."
	if interruptedTool != "" {
		content += fmt.Sprintf(" Your last tool call (%s) was interrupted before it completed.", interruptedTool)
	}
	content += " Resume from exactly where you stopped. Keep this response shorter than the last one: split large edits or long outputs across several smaller steps."
	return llm.Message{Role: llm.RoleUser, Content: content}
}

func transientStreamRetryMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "The previous model stream ended before returning usable content. " +
			"Retry once from the current context and produce a valid tool call or final answer.",
	}
}
