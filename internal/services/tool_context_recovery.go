package services

import (
	"encoding/json"
	"strings"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

const (
	toolResultContextErrorCode = "tool_result_too_large"
	toolResultContextErrorText = "The tool result was too large for the model's available context. Narrow the request by using a smaller path, fewer results or lines, or a lower byte limit, then try again."
)

type toolContextRecovery struct {
	Messages      []llm.Message
	Call          llm.ToolCall
	ResultMessage llm.Message
}

// recoverToolResultContext replaces the largest result from the latest tool
// turn with a protocol-valid tool error. A later retry can replace another
// result from the same turn if several results together exceeded the context.
func recoverToolResultContext(messages []llm.Message, allowedCallIDs map[string]bool) (toolContextRecovery, bool) {
	assistantIndex := -1
	var calls []llm.ToolCall
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleAssistant && len(messages[i].ToolCalls) > 0 {
			assistantIndex = i
			calls = messages[i].ToolCalls
			break
		}
	}
	if assistantIndex < 0 {
		return toolContextRecovery{}, false
	}

	callsByID := make(map[string]llm.ToolCall, len(calls))
	for _, call := range calls {
		if allowedCallIDs == nil || allowedCallIDs[call.ID] {
			callsByID[call.ID] = call
		}
	}

	type candidate struct {
		index  int
		end    int
		weight int
		call   llm.ToolCall
	}
	var selected candidate
	found := false
	for i := assistantIndex + 1; i < len(messages); i++ {
		message := messages[i]
		call, ok := callsByID[message.ToolCallID]
		if message.Role != llm.RoleTool || !ok || strings.Contains(message.Content, `"`+toolResultContextErrorCode+`"`) {
			continue
		}

		end := i + 1
		weight := messageContextWeight(message)
		for end < len(messages) && isToolResultImageMessage(messages[end]) {
			weight += messageContextWeight(messages[end])
			end++
		}
		if !found || weight > selected.weight {
			selected = candidate{index: i, end: end, weight: weight, call: call}
			found = true
		}
	}
	if !found {
		return toolContextRecovery{}, false
	}

	result := tools.ExecutionResult{
		Tool:    selected.call.Function.Name,
		Success: false,
		Error: &tools.ExecutionError{
			Code:    toolResultContextErrorCode,
			Message: toolResultContextErrorText,
		},
	}
	data, _ := json.Marshal(result)
	resultMessage := llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: selected.call.ID,
		Content:    string(data),
	}

	recovered := make([]llm.Message, 0, len(messages)-(selected.end-selected.index)+1)
	recovered = append(recovered, messages[:selected.index]...)
	recovered = append(recovered, resultMessage)
	recovered = append(recovered, messages[selected.end:]...)
	return toolContextRecovery{
		Messages:      recovered,
		Call:          selected.call,
		ResultMessage: resultMessage,
	}, true
}

func isToolResultImageMessage(message llm.Message) bool {
	if message.Role != llm.RoleUser || len(message.ContentParts) == 0 {
		return false
	}
	for _, part := range message.ContentParts {
		if part.Type == "image_url" && part.ImageURL != nil {
			return true
		}
	}
	return false
}

func messageContextWeight(message llm.Message) int {
	weight := len(message.Content)
	for _, part := range message.ContentParts {
		weight += len(part.Text)
		if part.ImageURL != nil {
			weight += len(part.ImageURL.URL)
		}
	}
	return weight
}
