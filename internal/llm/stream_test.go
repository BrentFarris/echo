package llm

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// eofReader returns io.ErrUnexpectedEOF after reading up to a given byte count.
type eofReader struct {
	source []byte
	limit  int
	offset int
}

func (r *eofReader) Read(p []byte) (int, error) {
	if r.offset >= r.limit {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.source[r.offset:])
	if n > r.limit-r.offset {
		n = r.limit - r.offset
	}
	r.offset += n
	return n, nil
}

// eofAfterBytes wraps a string reader to return io.ErrUnexpectedEOF after `limit` bytes.
func eofAfterBytes(s string, limit int) *eofReader {
	return &eofReader{source: []byte(s), limit: limit}
}

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

func TestParseStreamEOFAfterDoneIsHarmless(t *testing.T) {
	// Stream with content + [DONE], then EOF error after that.
	// The full input ends normally (strings.NewReader returns io.EOF, not UnexpectedEOF).
	// We simulate this by having the scanner finish reading all data including [DONE].
	input := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	// Wrap so that after the last newline (which follows [DONE]) we get UnexpectedEOF.
	// The input string is complete and valid, so io.EOF from ReadString('\n') would
	// normally be fine. But if the scanner encounters unexpected EOF while looking
	// for more lines after all data has been consumed, it should not emit an error.
	// strings.NewReader returns io.EOF (not ErrUnexpectedEOF), so we use eofReader
	// to simulate the server closing mid-read after [DONE].
	// The key: [DONE] is fully received before EOF hits.
	reader := eofAfterBytes(input, len(input))

	events := make(chan StreamEvent, 4)
	parseStream(context.Background(), reader, events)
	close(events)

	got := drainEvents(events)
	if len(got) != 2 {
		t.Fatalf("expected 2 events (token + complete), got %d: %#v", len(got), got)
	}
	if got[0].Type != EventToken || got[0].Content != "Hello" {
		t.Fatalf("expected token event, got %#v", got[0])
	}
	if got[1].Type != EventComplete {
		t.Fatalf("expected complete event, got %#v", got[1])
	}
	// Verify no error event was emitted
	for i, e := range got {
		if e.Type == EventError {
			t.Fatalf("unexpected error event at index %d: %#v", i, e)
		}
	}
}

func TestParseStreamEOFWithPartialContentEmitsComplete(t *testing.T) {
	// Stream with partial content but no [DONE] or finish_reason, then UnexpectedEOF.
	input := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":" World"}}]}`,
		``,
	}, "\n")

	// Cut off before the last empty line so the scanner gets EOF while there's no
	// trailing newline — simulating connection closed mid-stream. The second chunk
	// is in remaining dataLines that are NOT flushed after scanner error (truncated).
	reader := eofAfterBytes(input, len(input)-2) // remove final "\n\n"

	events := make(chan StreamEvent, 4)
	parseStream(context.Background(), reader, events)
	close(events)

	got := drainEvents(events)

	// Should have: token "Hello", EventComplete (graceful EOF because receivedData is true).
	// The second "World" chunk won't be flushed since it's in remaining dataLines after error.
	if len(got) < 2 {
		t.Fatalf("expected at least 2 events, got %d: %#v", len(got), got)
	}

	// Last event should be EventComplete, not EventError
	last := got[len(got)-1]
	if last.Type != EventComplete {
		t.Fatalf("last event should be EventComplete on partial EOF, got %#v", last)
	}
	for i, e := range got {
		if e.Type == EventError {
			t.Fatalf("unexpected error event at index %d: %#v", i, e)
		}
	}
}

func TestParseStreamEOFWithDataEmitsComplete(t *testing.T) {
	// Partial content: token streamed, then connection drops mid-second chunk.
	input := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":`, // truncated mid-JSON
	}, "\n")

	reader := eofAfterBytes(input, len(input))

	events := make(chan StreamEvent, 4)
	parseStream(context.Background(), reader, events)
	close(events)

	got := drainEvents(events)

	// token "Hello" was emitted during the scanning loop. Truncated JSON is NOT flushed.
	// Since receivedData=true, EOF emits EventComplete instead of EventError.
	hasToken := false
	hasComplete := false
	for _, e := range got {
		if e.Type == EventToken && e.Content == "Hello" {
			hasToken = true
		}
		if e.Type == EventComplete {
			hasComplete = true
		}
		if e.Type == EventError {
			t.Fatalf("expected no EventError when partial data exists, got: %v", e.Error)
		}
	}
	if !hasToken {
		t.Fatal("expected token event for 'Hello'")
	}
	if !hasComplete {
		t.Fatal("expected EventComplete for partial EOF with existing data")
	}
}

func TestParseStreamEOFWithNoDataEmitsError(t *testing.T) {
	// Connection drops before any useful data arrives.
	input := "garbage\n"
	reader := eofAfterBytes(input, 5) // cut off partway through first line

	events := make(chan StreamEvent, 2)
	parseStream(context.Background(), reader, events)
	close(events)

	got := drainEvents(events)
	if len(got) == 0 {
		t.Fatal("expected at least one event")
	}
	last := got[len(got)-1]
	if last.Type != EventError {
		t.Fatalf("expected EventError when no data received, got %#v", last)
	}
	for i, e := range got {
		if e.Type == EventComplete || e.Type == EventToken {
			t.Fatalf("unexpected non-error event at index %d: %#v", i, e)
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
