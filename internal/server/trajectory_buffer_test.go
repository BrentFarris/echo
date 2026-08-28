package server

import (
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

func TestAssistantTrajectoryBufferFlushesAtSemanticPhaseBoundaries(t *testing.T) {
	startedAt := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	buffer := assistantTrajectoryBuffer{
		turnID: "turn-1", step: 3, chunk: make([]map[string]any, 0, trajectoryStreamChunkEvents),
	}
	for index := range 35 {
		if buffer.changesPhase(llm.EventReasoning) {
			t.Fatal("a continuing reasoning stream unexpectedly changed phase")
		}
		if buffer.add(llm.StreamEvent{Type: llm.EventReasoning, Content: "r"}, startedAt.Add(time.Duration(index)*time.Millisecond)) {
			t.Fatal("small reasoning events unexpectedly reached the byte limit")
		}
	}
	if !buffer.changesPhase(llm.EventToken) {
		t.Fatal("reasoning to visible content did not create a flush boundary")
	}
	reasoningEntries := buffer.drain()
	if len(reasoningEntries) != 3 {
		t.Fatalf("expected 16/16/3 reasoning records, got %d", len(reasoningEntries))
	}
	wantSizes := []int{16, 16, 3}
	for index, entry := range reasoningEntries {
		data, ok := entry.Data.(map[string]any)
		if !ok {
			t.Fatalf("entry %d has unexpected data: %#v", index, entry.Data)
		}
		events, ok := data["streamEvents"].([]map[string]any)
		if !ok || len(events) != wantSizes[index] {
			t.Fatalf("entry %d contains %d stream events, want %d", index, len(events), wantSizes[index])
		}
	}
	if !reasoningEntries[2].Timestamp.Equal(startedAt.Add(34 * time.Millisecond)) {
		t.Fatalf("partial reasoning record lost its capture timestamp: %s", reasoningEntries[2].Timestamp)
	}

	buffer.add(llm.StreamEvent{Type: llm.EventToken, Content: "answer"}, startedAt.Add(35*time.Millisecond))
	if buffer.changesPhase(llm.EventUsage) || buffer.changesPhase(llm.EventComplete) {
		t.Fatal("usage and completion controls should finish the current phase")
	}
	buffer.add(llm.StreamEvent{Type: llm.EventUsage, Usage: &llm.Usage{TotalTokens: 36}}, startedAt.Add(36*time.Millisecond))
	buffer.add(llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}, startedAt.Add(37*time.Millisecond))
	contentEntries := buffer.drain()
	if len(contentEntries) != 1 {
		t.Fatalf("expected content controls in one record, got %d", len(contentEntries))
	}
	data := contentEntries[0].Data.(map[string]any)
	if events := data["streamEvents"].([]map[string]any); len(events) != 3 {
		t.Fatalf("expected token, usage, and completion in one record, got %d", len(events))
	}
}

func TestAssistantTrajectoryBufferEnforcesByteLimit(t *testing.T) {
	buffer := assistantTrajectoryBuffer{
		turnID: "turn-1", chunk: make([]map[string]any, 0, trajectoryStreamChunkEvents),
	}
	reached := buffer.add(llm.StreamEvent{
		Type: llm.EventReasoning, Content: strings.Repeat("x", trajectoryStreamMaxBufferedBytes),
	}, time.Now().UTC())
	if !reached {
		t.Fatal("expected an oversized phase to request a bounded flush")
	}
	if entries := buffer.drain(); len(entries) != 1 {
		t.Fatalf("expected the oversized event to remain persistable, got %d entries", len(entries))
	}
}

func TestResearchTrajectoryBufferUsesSharedBoundsAndActorMetadata(t *testing.T) {
	receivedAt := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	buffer := streamTrajectoryBuffer{
		turnID: "turn-research", omitStep: true, eventType: "research/chunk",
		baseData: map[string]any{"agentId": "agent-2", "agentName": "Docs", "jobId": "agent-2-job-1", "jobNumber": 1, "round": 0},
		chunk:    make([]map[string]any, 0, trajectoryStreamChunkEvents),
	}
	buffer.add(llm.StreamEvent{Type: llm.EventToken, Content: "evidence"}, receivedAt)
	entries := buffer.drain()
	if len(entries) != 1 || entries[0].Type != "research/chunk" || entries[0].Step != nil {
		t.Fatalf("unexpected research chunk envelope: %#v", entries)
	}
	data := entries[0].Data.(map[string]any)
	if data["agentId"] != "agent-2" || data["jobId"] != "agent-2-job-1" || data["round"] != 0 {
		t.Fatalf("research identity was not retained: %#v", data)
	}
	streamEvents := data["streamEvents"].([]map[string]any)
	if len(streamEvents) != 1 || streamEvents[0]["receivedAt"] != receivedAt {
		t.Fatalf("research stream event was not retained exactly: %#v", streamEvents)
	}
}
