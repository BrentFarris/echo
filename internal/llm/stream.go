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
	Usage   *Usage         `json:"usage"`
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
	parseStreamLogged(ctx, reader, events, nil, nil)
}

func parseStreamLogged(ctx context.Context, reader io.Reader, events chan<- StreamEvent, logger *streamLogger, usageOut **Usage) {
	if logger != nil {
		defer logger.finish()
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataLines []string
	completed := false
	receivedData := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			emitCanceledLogged(events, logger)
			return
		}

		line := scanner.Text()
		if line == "" {
			if flushStreamData(ctx, events, dataLines, &completed, &receivedData, logger, usageOut) {
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

	if ctx.Err() != nil {
		emitCanceledLogged(events, logger)
		return
	}
	if err := scanner.Err(); err != nil {
		// Scanner error (typically unexpected EOF from a dropped connection).
		// Do NOT flush remaining dataLines because they are likely incomplete/truncated.
		// Valid events were already emitted during the scanning loop above,
		// and receivedData tracks whether any meaningful content arrived.

		// Handle EOF and other read errors gracefully based on what was received.
		if completed {
			// [DONE] or finish_reason already received; trailing EOF is harmless.
			return
		}
		if receivedData {
			// Partial content streamed; emit completion so the partial response is usable.
			emitLogged(ctx, events, StreamEvent{Type: EventComplete}, logger)
			return
		}
		// No data received at all; this is a genuine error.
		emitLogged(ctx, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("read stream: %v", err)}, logger)
		return
	}
	// Clean finish: flush any remaining dataLines that lacked a trailing newline.
	if len(dataLines) > 0 {
		flushStreamData(ctx, events, dataLines, &completed, &receivedData, logger, usageOut)
	}
}

func flushStreamData(ctx context.Context, events chan<- StreamEvent, dataLines []string, completed *bool, receivedData *bool, logger *streamLogger, usageOut **Usage) bool {
	if len(dataLines) == 0 {
		return false
	}

	data := strings.Join(dataLines, "\n")
	if logger != nil {
		logger.raw(data)
	}
	if data == "[DONE]" {
		if !*completed {
			emitLogged(ctx, events, StreamEvent{Type: EventComplete}, logger)
			*completed = true
		}
		return true
	}

	var chunk streamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		emitLogged(ctx, events, StreamEvent{Type: EventError, Error: fmt.Sprintf("decode stream chunk: %v", err)}, logger)
		return true
	}

	hasData := false
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			emitLogged(ctx, events, StreamEvent{Type: EventToken, Content: *choice.Delta.Content, Raw: json.RawMessage(data)}, logger)
			hasData = true
		}
		if reasoning := choice.Delta.reasoningText(); reasoning != "" {
			emitLogged(ctx, events, StreamEvent{Type: EventReasoning, Content: reasoning, Raw: json.RawMessage(data)}, logger)
			hasData = true
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			copy := toolCall
			emitLogged(ctx, events, StreamEvent{Type: EventToolCall, ToolCall: &copy, Raw: json.RawMessage(data)}, logger)
			hasData = true
		}
		if choice.FinishReason != nil && !*completed {
			emitLogged(ctx, events, StreamEvent{Type: EventComplete, FinishReason: *choice.FinishReason, Raw: json.RawMessage(data)}, logger)
			*completed = true
		}
	}
	if hasData {
		*receivedData = true
	}
	if chunk.Usage != nil {
		usage := *chunk.Usage
		emitLogged(ctx, events, StreamEvent{Type: EventUsage, Usage: &usage, Raw: json.RawMessage(data)}, logger)
		if usageOut != nil {
			*usageOut = &usage
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

func emitLogged(ctx context.Context, events chan<- StreamEvent, event StreamEvent, logger *streamLogger) bool {
	if logger != nil {
		logger.event(event)
	}
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

func emitCanceledLogged(events chan<- StreamEvent, logger *streamLogger) {
	event := StreamEvent{Type: EventCanceled}
	if logger != nil {
		logger.event(event)
	}
	select {
	case events <- event:
	default:
	}
}
