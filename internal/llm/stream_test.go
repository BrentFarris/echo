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
