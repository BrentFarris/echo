package llm

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseStreamEmitsTokenReasoningToolCallAndDone(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"Thinking"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_files","arguments":"{\"path\":\".\"}"}}]}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	events := make(chan StreamEvent, 8)
	parseStream(context.Background(), strings.NewReader(input), events)
	close(events)

	got := drainEvents(events)
	if len(got) != 4 {
		t.Fatalf("expected 4 events, got %d: %#v", len(got), got)
	}
	if got[0].Type != EventToken || got[0].Content != "Hello" {
		t.Fatalf("expected token event, got %#v", got[0])
	}
	if got[1].Type != EventReasoning || got[1].Content != "Thinking" {
		t.Fatalf("expected reasoning event, got %#v", got[1])
	}
	if got[2].Type != EventToolCall || got[2].ToolCall == nil {
		t.Fatalf("expected tool-call event, got %#v", got[2])
	}
	if got[2].ToolCall.ID != "call_1" || got[2].ToolCall.Function.Name != "list_files" {
		t.Fatalf("unexpected tool-call delta: %#v", got[2].ToolCall)
	}
	if got[3].Type != EventComplete {
		t.Fatalf("expected complete event, got %#v", got[3])
	}
}

func TestParseStreamEmitsCompleteForFinishReason(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
	}, "\n")

	events := make(chan StreamEvent, 2)
	parseStream(context.Background(), strings.NewReader(input), events)
	close(events)

	got := drainEvents(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d: %#v", len(got), got)
	}
	if got[0].Type != EventComplete || got[0].FinishReason != "stop" {
		t.Fatalf("expected stop completion, got %#v", got[0])
	}
}

func TestParseStreamEmitsErrorForMalformedJSON(t *testing.T) {
	input := "data: {\"choices\":\n\n"

	events := make(chan StreamEvent, 2)
	parseStream(context.Background(), strings.NewReader(input), events)
	close(events)

	got := drainEvents(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d: %#v", len(got), got)
	}
	if got[0].Type != EventError {
		t.Fatalf("expected error event, got %#v", got[0])
	}
}

func TestParseStreamEmitsCanceledWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := make(chan StreamEvent, 2)
	parseStream(ctx, strings.NewReader("data: {\"choices\":[]}\n\n"), events)
	close(events)

	got := drainEvents(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d: %#v", len(got), got)
	}
	if got[0].Type != EventCanceled {
		t.Fatalf("expected canceled event, got %#v", got[0])
	}
}

func TestParseStreamEmitsUsage(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
	}, "\n")

	events := make(chan StreamEvent, 4)
	parseStream(context.Background(), strings.NewReader(input), events)
	close(events)

	got := drainEvents(events)
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d: %#v", len(got), got)
	}
	if got[0].Type != EventToken || got[0].Content != "Hello" {
		t.Fatalf("expected token event, got %#v", got[0])
	}
	if got[1].Type != EventUsage {
		t.Fatalf("expected usage event, got %#v", got[1])
	}
	if got[1].Usage == nil {
		t.Fatalf("expected usage data, got nil")
	}
	if got[1].Usage.PromptTokens != 10 {
		t.Fatalf("expected 10 prompt tokens, got %d", got[1].Usage.PromptTokens)
	}
	if got[1].Usage.CompletionTokens != 5 {
		t.Fatalf("expected 5 completion tokens, got %d", got[1].Usage.CompletionTokens)
	}
	if got[1].Usage.TotalTokens != 15 {
		t.Fatalf("expected 15 total tokens, got %d", got[1].Usage.TotalTokens)
	}
}

func TestParseStreamIgnoresEmptyUsage(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		``,
	}, "\n")

	events := make(chan StreamEvent, 4)
	parseStream(context.Background(), strings.NewReader(input), events)
	close(events)

	got := drainEvents(events)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d: %#v", len(got), got)
	}
	for i, e := range got {
		if e.Type == EventUsage {
			t.Fatalf("unexpected usage event at index %d", i)
		}
	}
}

func drainEvents(events <-chan StreamEvent) []StreamEvent {
	var drained []StreamEvent
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return drained
			}
			drained = append(drained, event)
		case <-time.After(time.Second):
			return drained
		}
	}
}
