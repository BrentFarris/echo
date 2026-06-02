package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Content          *string         `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	Reasoning        *string         `json:"reasoning"`
	ThinkingContent  *string         `json:"thinking_content"`
	Thinking         *string         `json:"thinking"`
	ToolCalls        []ToolCallDelta `json:"tool_calls"`
}

func parseStream(ctx context.Context, reader io.Reader, events chan<- StreamEvent) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	completed := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			emitCanceled(events)
			return
		}

		line := scanner.Text()
		if line == "" {
			if flushStreamData(ctx, events, dataLines, &completed) {
				return
			}
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if len(dataLines) > 0 && flushStreamData(ctx, events, dataLines, &completed) {
		return
	}
	if ctx.Err() != nil {
		emitCanceled(events)
		return
	}
	if err := scanner.Err(); err != nil {
		emit(ctx, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("read stream: %v", err)})
	}
}

func flushStreamData(ctx context.Context, events chan<- StreamEvent, dataLines []string, completed *bool) bool {
	if len(dataLines) == 0 {
		return false
	}

	data := strings.Join(dataLines, "\n")
	if data == "[DONE]" {
		if !*completed {
			emit(ctx, events, StreamEvent{Type: EventComplete})
			*completed = true
		}
		return true
	}

	var chunk streamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		emit(ctx, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("decode stream chunk: %v", err)})
		return true
	}

	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			emit(ctx, events, StreamEvent{Type: EventToken, Content: *choice.Delta.Content, Raw: json.RawMessage(data)})
		}
		if reasoning := choice.Delta.reasoningText(); reasoning != "" {
			emit(ctx, events, StreamEvent{Type: EventReasoning, Content: reasoning, Raw: json.RawMessage(data)})
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			copy := toolCall
			emit(ctx, events, StreamEvent{Type: EventToolCall, ToolCall: &copy, Raw: json.RawMessage(data)})
		}
		if choice.FinishReason != nil && !*completed {
			emit(ctx, events, StreamEvent{Type: EventComplete, FinishReason: *choice.FinishReason, Raw: json.RawMessage(data)})
			*completed = true
		}
	}
	return false
}

func (d streamDelta) reasoningText() string {
	for _, value := range []*string{d.ReasoningContent, d.Reasoning, d.ThinkingContent, d.Thinking} {
		if value != nil && *value != "" {
			return *value
		}
	}
	return ""
}

func emit(ctx context.Context, events chan<- StreamEvent, event StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

func emitCanceled(events chan<- StreamEvent) {
	select {
	case events <- StreamEvent{Type: EventCanceled}:
	default:
	}
}
