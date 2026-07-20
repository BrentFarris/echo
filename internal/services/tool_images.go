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
	videoMessage, ok := toolResultVideoMessage(call.Function.Name, result)
	if ok {
		messages = append(messages, videoMessage)
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

func toolResultVideoMessage(toolName string, result tools.ExecutionResult) (llm.Message, bool) {
	if !result.Success || result.Output == nil {
		return llm.Message{}, false
	}
	provider, ok := result.Output.(tools.LLMVideoContentProvider)
	if !ok {
		return llm.Message{}, false
	}
	video, ok := provider.LLMVideoContent()
	if !ok || strings.TrimSpace(video.DataURL) == "" {
		return llm.Message{}, false
	}

	label := strings.TrimSpace(video.Path)
	if label == "" {
		label = strings.TrimSpace(video.Name)
	}
	if label == "" {
		label = "video"
	}
	text := fmt.Sprintf("Video returned by tool %s: %s (%s, %s).", toolName, label, video.MediaType, formatChatImageBytes(video.Bytes))
	videoPart := llm.VideoURLContentPart(video.DataURL)
	if video.Detail != "" && videoPart.VideoURL != nil {
		videoPart.VideoURL.Detail = video.Detail
	}
	return llm.Message{
		Role:         llm.RoleUser,
		Content:      text,
		ContentParts: []llm.MessageContentPart{llm.TextContentPart(text), videoPart},
	}, true
}

// stripMediaContentParts removes image and video ContentParts from a message,
// keeping only the plain text Content. Use this before storing tool-result
// messages in persistent chat history so that base64 data URLs do not
// accumulate across turns.
func stripMediaContentParts(message llm.Message) llm.Message {
	if len(message.ContentParts) == 0 {
		return message
	}
	// Keep the text content, discard image/video parts
	return llm.Message{
		Role:       message.Role,
		Content:    message.Content,
		ToolCallID: message.ToolCallID,
		Name:       message.Name,
	}
}
