package llm

import (
	"log/slog"
	"sort"
	"strings"

	"github.com/brent/echo/internal/flowlog"
)

type runtimeStreamLogger struct {
	trace     *flowlog.RequestTrace
	content   strings.Builder
	reasoning strings.Builder
	toolCalls map[int]*ToolCall
	finish    string
	usage     *Usage
	finished  bool
}

func newRuntimeStreamLogger(trace *flowlog.RequestTrace) *runtimeStreamLogger {
	if trace == nil {
		return nil
	}
	return &runtimeStreamLogger{trace: trace, toolCalls: make(map[int]*ToolCall)}
}

func (l *runtimeStreamLogger) raw(data string) {
	if l == nil {
		return
	}
	l.trace.Log(slog.LevelDebug, "llm_stream_chunk", slog.String("payload", data))
}

func (l *runtimeStreamLogger) event(event StreamEvent) {
	if l == nil {
		return
	}
	l.trace.Log(slog.LevelDebug, "llm_stream_event", slog.Any("stream_event", event))
	switch event.Type {
	case EventToken:
		l.content.WriteString(event.Content)
	case EventReasoning:
		l.reasoning.WriteString(event.Content)
	case EventToolCall:
		if event.ToolCall != nil {
			call := l.toolCalls[event.ToolCall.Index]
			if call == nil {
				call = &ToolCall{}
				l.toolCalls[event.ToolCall.Index] = call
			}
			if event.ToolCall.ID != "" {
				call.ID = event.ToolCall.ID
			}
			if event.ToolCall.Type != "" {
				call.Type = event.ToolCall.Type
			}
			if event.ToolCall.Function.Name != "" {
				call.Function.Name = event.ToolCall.Function.Name
			}
			call.Function.Arguments += event.ToolCall.Function.Arguments
		}
	case EventComplete:
		l.finish = event.FinishReason
	case EventUsage:
		if event.Usage != nil {
			usage := *event.Usage
			l.usage = &usage
		}
	}
}

func (l *runtimeStreamLogger) finishResponse() {
	if l == nil || l.finished {
		return
	}
	l.finished = true
	indices := make([]int, 0, len(l.toolCalls))
	for index := range l.toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]ToolCall, 0, len(indices))
	for _, index := range indices {
		calls = append(calls, *l.toolCalls[index])
	}
	l.trace.Log(slog.LevelInfo, "llm_response_summary",
		slog.String("content", l.content.String()),
		slog.String("reasoning", l.reasoning.String()),
		slog.Any("tool_calls", calls),
		slog.String("finish_reason", l.finish),
		slog.Any("usage", l.usage),
	)
}
