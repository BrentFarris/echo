package llm

import (
	"context"
	"strings"
	"testing"
)

func TestParseStreamEmitsReasoningBeforeContentFromMixedDelta(t *testing.T) {
	input := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"The\",\"reasoning\":\" find it.\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" web search tool\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n")
	events := make(chan StreamEvent, 8)

	parseStream(context.Background(), input, events, nil)
	close(events)

	var got []StreamEvent
	for event := range events {
		got = append(got, event)
	}

	if len(got) != 4 {
		t.Fatalf("expected four events, got %#v", got)
	}
	if got[0].Type != EventReasoning || got[0].Content != " find it." {
		t.Fatalf("expected reasoning first, got %#v", got[0])
	}
	if got[1].Type != EventToken || got[1].Content != "The" {
		t.Fatalf("expected first answer content second, got %#v", got[1])
	}
	if got[2].Type != EventToken || got[2].Content != " web search tool" {
		t.Fatalf("expected remaining answer content third, got %#v", got[2])
	}
	if got[3].Type != EventComplete || got[3].FinishReason != "stop" {
		t.Fatalf("expected completion last, got %#v", got[3])
	}
}

func TestParseStreamRejectsEOFBeforeTerminalEvent(t *testing.T) {
	input := strings.NewReader("data: {\"choices\":[{\"delta\":{\"reasoning\":\"still thinking\"}}]}\n\n")
	events := make(chan StreamEvent, 4)

	parseStream(context.Background(), input, events, nil)
	close(events)

	got := make([]StreamEvent, 0, 2)
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 2 || got[0].Type != EventReasoning {
		t.Fatalf("expected reasoning followed by an error, got %#v", got)
	}
	if got[1].Type != EventError || got[1].Error != ErrStreamEndedBeforeCompletion.Error() {
		t.Fatalf("expected incomplete-stream error, got %#v", got[1])
	}
}

func TestParseStreamAcceptsDoneSentinelWithoutFinishReason(t *testing.T) {
	input := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
	events := make(chan StreamEvent, 4)

	parseStream(context.Background(), input, events, nil)
	close(events)

	got := make([]StreamEvent, 0, 2)
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 2 || got[0].Type != EventToken || got[1].Type != EventComplete {
		t.Fatalf("expected token and compatible completion, got %#v", got)
	}
}

func TestParseStreamTreatsSSECommentsAsActivity(t *testing.T) {
	input := strings.NewReader(": keepalive\n\ndata: [DONE]\n\n")
	events := make(chan StreamEvent, 2)
	activity := 0

	parseStreamWithActivity(context.Background(), input, events, nil, func() { activity++ })
	close(events)

	if activity != 4 {
		t.Fatalf("expected every SSE line to reset activity, got %d", activity)
	}
}
