package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
)

type lifecycleSequenceStreamer struct {
	mu        sync.Mutex
	requests  []llm.ChatRequest
	sequences [][]llm.StreamEvent
}

func (s *lifecycleSequenceStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	index := len(s.requests) - 1
	var sequence []llm.StreamEvent
	if index < len(s.sequences) {
		sequence = append([]llm.StreamEvent(nil), s.sequences[index]...)
	} else {
		sequence = []llm.StreamEvent{{Type: llm.EventError, Error: "unexpected extra model request"}}
	}
	s.mu.Unlock()

	events := make(chan llm.StreamEvent, len(sequence))
	for _, event := range sequence {
		events <- event
	}
	close(events)
	return &llm.Stream{ID: "lifecycle", Events: events}
}

func (s *lifecycleSequenceStreamer) snapshot() []llm.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.ChatRequest(nil), s.requests...)
}

func runLifecycleChat(t *testing.T, streamer *lifecycleSequenceStreamer, prompt string) (map[string]any, sessions.TabTranscript) {
	t.Helper()
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "stream-lifecycle")
	server.llm = streamer
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID,
		"requestId": "stream-lifecycle-request", "message": prompt,
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	transcript := loadActiveTabTranscript(t, workspace)
	return finished, transcript
}

func TestChatRetriesIncompleteReasoningOnlyStream(t *testing.T) {
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{{Type: llm.EventReasoning, Content: "still thinking"}},
		{{Type: llm.EventToken, Content: "Recovered answer."}, {Type: llm.EventComplete, FinishReason: "stop"}},
	}}

	finished, transcript := runLifecycleChat(t, streamer, "answer this")
	if finished["status"] != "done" {
		t.Fatalf("expected retry to complete, got %v", finished)
	}
	requests := streamer.snapshot()
	if len(requests) != 2 {
		t.Fatalf("expected one retry, got %d requests", len(requests))
	}
	lastRequestMessage := requests[1].Messages[len(requests[1].Messages)-1]
	if lastRequestMessage.Role != llm.RoleUser || !strings.Contains(lastRequestMessage.Content, "ended before returning usable content") {
		t.Fatalf("expected incomplete-stream recovery guidance, got %#v", requests[1].Messages)
	}
	if transcript.Messages[len(transcript.Messages)-1].Role != llm.RoleAssistant || transcript.Messages[len(transcript.Messages)-1].Content != "Recovered answer." {
		t.Fatalf("expected recovered response in history, got %#v", transcript.Messages)
	}
}

func TestChatRetriesReasoningOnlyCompletion(t *testing.T) {
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{{Type: llm.EventReasoning, Content: "I should answer."}, {Type: llm.EventComplete, FinishReason: "stop"}},
		{{Type: llm.EventToken, Content: "Visible answer."}, {Type: llm.EventComplete, FinishReason: "stop"}},
	}}

	finished, _ := runLifecycleChat(t, streamer, "answer this")
	if finished["status"] != "done" {
		t.Fatalf("expected reasoning-only recovery to complete, got %v", finished)
	}
	requests := streamer.snapshot()
	if len(requests) != 2 {
		t.Fatalf("expected one reasoning-only retry, got %d requests", len(requests))
	}
	lastRequestMessage := requests[1].Messages[len(requests[1].Messages)-1]
	if !strings.Contains(lastRequestMessage.Content, "without visible content") {
		t.Fatalf("expected reasoning-only recovery guidance, got %#v", requests[1].Messages)
	}
}

func TestChatBoundsReasoningOnlyRetries(t *testing.T) {
	reasoningOnly := []llm.StreamEvent{
		{Type: llm.EventReasoning, Content: "thinking"},
		{Type: llm.EventComplete, FinishReason: "stop"},
	}
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{reasoningOnly, reasoningOnly, reasoningOnly}}

	finished, transcript := runLifecycleChat(t, streamer, "answer this")
	if finished["status"] != "error" || !strings.Contains(finished["error"].(string), "without producing visible content") {
		t.Fatalf("expected bounded empty-response error, got %v", finished)
	}
	if len(streamer.snapshot()) != maxEmptyAssistantRetries+1 {
		t.Fatalf("expected %d attempts, got %d", maxEmptyAssistantRetries+1, len(streamer.snapshot()))
	}
	for _, message := range transcript.Messages {
		if message.Role == llm.RoleAssistant && strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
			t.Fatalf("reasoning-only response poisoned history: %#v", transcript.Messages)
		}
	}
}

func TestChatReportsLengthFinishReasonAndExcludesPartialHistory(t *testing.T) {
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{{
		{Type: llm.EventToken, Content: "Partial answer"},
		{Type: llm.EventComplete, FinishReason: "length"},
	}}}

	finished, transcript := runLifecycleChat(t, streamer, "answer this")
	if finished["status"] != "error" || !strings.Contains(finished["error"].(string), "token limit") {
		t.Fatalf("expected token-limit error, got %v", finished)
	}
	if len(streamer.snapshot()) != 1 {
		t.Fatalf("partial visible output should not be retried automatically")
	}
	for _, message := range transcript.Messages {
		if message.Role == llm.RoleAssistant {
			t.Fatalf("partial response was added to model history: %#v", transcript.Messages)
		}
	}
	if len(transcript.Turns) != 1 || len(transcript.Turns[0].AssistantTurns) != 1 || transcript.Turns[0].AssistantTurns[0].Content != "Partial answer" {
		t.Fatalf("partial response was not preserved for the user: %#v", transcript.Turns)
	}
}

func TestChatReportsIncompleteVisibleStreamWithoutRetry(t *testing.T) {
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{{
		{Type: llm.EventToken, Content: "Partial answer"},
	}}}

	finished, transcript := runLifecycleChat(t, streamer, "answer this")
	if finished["status"] != "error" || !strings.Contains(finished["error"].(string), "ended before completion") {
		t.Fatalf("expected incomplete-stream error, got %v", finished)
	}
	if len(streamer.snapshot()) != 1 {
		t.Fatalf("partial visible output should not be retried automatically")
	}
	for _, message := range transcript.Messages {
		if message.Role == llm.RoleAssistant {
			t.Fatalf("partial response was added to model history: %#v", transcript.Messages)
		}
	}
	if len(transcript.Turns) != 1 || len(transcript.Turns[0].AssistantTurns) != 1 || transcript.Turns[0].AssistantTurns[0].Content != "Partial answer" {
		t.Fatalf("partial response was not preserved for the user: %#v", transcript.Turns)
	}
}
