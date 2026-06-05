package services

import (
	"fmt"
	"strings"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func toolResultMessages(call llm.ToolCall, result tools.ExecutionResult, data []byte) []llm.Message {
	messages := []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: call.ID,
		Content:    string(data),
	}}

	imageMessage, ok := toolResultImageMessage(call.Function.Name, result)
	if ok {
		messages = append(messages, imageMessage)
	}
	return messages
}

func toolResultImageMessage(toolName string, result tools.ExecutionResult) (llm.Message, bool) {
	if !result.Success || result.Output == nil {
		return llm.Message{}, false
	}
	provider, ok := result.Output.(tools.LLMImageContentProvider)
	if !ok {
		return llm.Message{}, false
	}
	image, ok := provider.LLMImageContent()
	if !ok || strings.TrimSpace(image.DataURL) == "" {
		return llm.Message{}, false
	}

	label := strings.TrimSpace(image.Path)
	if label == "" {
		label = strings.TrimSpace(image.Name)
	}
	if label == "" {
		label = "image"
	}
	text := fmt.Sprintf("Image returned by tool %s: %s (%s, %s).", toolName, label, image.MediaType, formatChatImageBytes(image.Bytes))
	imagePart := llm.ImageURLContentPart(image.DataURL)
	if image.Detail != "" && imagePart.ImageURL != nil {
		imagePart.ImageURL.Detail = image.Detail
	}
	return llm.Message{
		Role:         llm.RoleUser,
		Content:      text,
		ContentParts: []llm.MessageContentPart{llm.TextContentPart(text), imagePart},
	}, true
}
