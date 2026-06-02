package services

import (
	"fmt"
	"strings"
)

func userFacingLLMError(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	switch {
	case strings.Contains(raw, "send chat request"),
		strings.Contains(raw, "connection refused"),
		strings.Contains(raw, "No connection could be made"):
		return "Could not reach the LLM endpoint. Check Settings and try again. " + raw
	case strings.Contains(raw, "context deadline exceeded"),
		strings.Contains(raw, "Client.Timeout"):
		return "The LLM endpoint timed out. Increase Timeout Seconds or check the endpoint. " + raw
	default:
		return raw
	}
}

func finishReasonError(finishReason string, hasToolCalls bool) error {
	switch strings.TrimSpace(finishReason) {
	case "", "stop":
		return nil
	case "tool_calls":
		if hasToolCalls {
			return nil
		}
		return fmt.Errorf("The LLM stopped to call a tool, but no tool call was received. Try again.")
	case "length":
		return fmt.Errorf("The LLM stopped before completing its response because it hit the token limit. Increase Max Tokens and try again.")
	case "content_filter":
		return fmt.Errorf("The LLM stopped because the provider filtered the response.")
	default:
		return fmt.Errorf("The LLM stopped before completing normally (finish_reason: %s).", finishReason)
	}
}
