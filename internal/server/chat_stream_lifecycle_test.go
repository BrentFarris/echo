package server

import (
	"context"
	"fmt"
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

func TestChatAutoContinuesLengthFinishReason(t *testing.T) {
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{{
		{Type: llm.EventToken, Content: "Partial answer "},
		{Type: llm.EventComplete, FinishReason: "length"},
	}, {
		{Type: llm.EventToken, Content: "rest of the answer"},
		{Type: llm.EventComplete, FinishReason: "stop"},
	}}}

	finished, transcript := runLifecycleChat(t, streamer, "answer this")
	if finished["status"] != "done" {
		t.Fatalf("expected auto-continue to complete the turn, got %v", finished)
	}
	requests := streamer.snapshot()
	if len(requests) != 2 {
		t.Fatalf("expected one auto-continue request, got %d requests", len(requests))
	}
	lastRequestMessage := requests[1].Messages[len(requests[1].Messages)-1]
	if lastRequestMessage.Role != llm.RoleUser || !strings.Contains(lastRequestMessage.Content, "cut off") {
		t.Fatalf("expected truncation continue guidance, got %#v", requests[1].Messages)
	}
	firstKwargs := requests[0].ChatTemplateKwargs
	if firstKwargs == nil || firstKwargs.EnableThinking != nil {
		t.Fatalf("first request should use the configured thinking mode: %#v", firstKwargs)
	}
	continueKwargs := requests[1].ChatTemplateKwargs
	if continueKwargs == nil || continueKwargs.EnableThinking == nil || *continueKwargs.EnableThinking {
		t.Fatalf("continuation request must disable thinking so reasoning cannot consume the output budget again: %#v", continueKwargs)
	}
	var assistantContents []string
	for _, message := range transcript.Messages {
		if message.Role == llm.RoleAssistant {
			assistantContents = append(assistantContents, message.Content)
		}
	}
	if len(assistantContents) != 2 || !strings.Contains(assistantContents[0], "Partial answer") || !strings.Contains(assistantContents[1], "rest of the answer") {
		t.Fatalf("expected the partial and continued assistant turns in history: %#v", transcript.Messages)
	}
}

func TestChatBoundsLengthFinishReasonContinues(t *testing.T) {
	lengthSequence := []llm.StreamEvent{
		{Type: llm.EventToken, Content: "Partial answer"},
		{Type: llm.EventComplete, FinishReason: "length"},
	}
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{lengthSequence, lengthSequence, lengthSequence}}

	finished, _ := runLifecycleChat(t, streamer, "answer this")
	if finished["status"] != "error" || !strings.Contains(finished["error"].(string), "token limit") {
		t.Fatalf("expected bounded token-limit error, got %v", finished)
	}
	if len(streamer.snapshot()) != maxTruncationContinues+1 {
		t.Fatalf("expected %d attempts before failing, got %d", maxTruncationContinues+1, len(streamer.snapshot()))
	}
}

func TestParseObservedContextLimit(t *testing.T) {
	for message, want := range map[string]int{
		`llm endpoint returned 400 Bad Request: {"error":{"code":400,"message":"request (131887 tokens) exceeds the available context size (131072 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":131887,"n_ctx":131072}}`: 131072,
		`llm endpoint returned 400 Bad Request: request exceeds the available context size (65536)`:       65536,
		`This model's maximum context length is 128,000 tokens. Your messages resulted in 130000 tokens.`: 128000,
		`request exceeds context window size: 32768 tokens`:                                               32768,
		`prompt exceeds the configured limit of 16,384 tokens`:                                            16384,
		"llm endpoint returned 500 Internal Server Error":                                                 0,
	} {
		if got := parseObservedContextLimit(fmt.Errorf("%s", message)); got != want {
			t.Fatalf("parseObservedContextLimit(%q) = %d, want %d", message, got, want)
		}
	}
}

func TestContextLengthAfterRejectionUsesConservativeFallback(t *testing.T) {
	settings := llm.DefaultSettings()
	settings.ContextLength = 200000
	settings.MaxTokens = 1024

	parsed := contextLengthAfterRejection(settings, fmt.Errorf("maximum context length is 65,536 tokens"), 40000)
	if parsed != 65536 {
		t.Fatalf("expected the parsed provider limit, got %d", parsed)
	}
	fallback := contextLengthAfterRejection(settings, fmt.Errorf("too many tokens"), 40000)
	if fallback >= settings.ContextLength || fallback >= 40000+settings.MaxTokens || fallback < settings.MaxTokens+1024 {
		t.Fatalf("expected a smaller but usable fallback context window, got %d", fallback)
	}
}

func TestChatRecoversFromProviderContextSizeRejection(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "context-recovery")
	settings := llm.DefaultSettings()
	// The configured window is far larger than the model's real one, so the
	// preflight never triggers and only the provider rejection reveals the
	// true limit.
	settings.ContextLength = 200000
	settings.MaxTokens = 1024
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	server.llmSettings = settings.NormalizedEndpointProfiles()

	// Seed a completed turn with a large assistant response so the next
	// request has compressible history.
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "Opening request"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("a", 150000)},
	}
	transcript := sessions.TabTranscript{
		ChatID: "chat-context-recovery", Preview: "Opening request", Revision: 1, Messages: canonical,
		Turns: []sessions.Turn{{
			ID: "turn-seed", RequestID: "request-seed", UserContent: "Opening request",
			UserMessageIndex: 0, Status: "done", AssistantTurns: []sessions.AssistantTurn{},
		}},
	}
	store := sessions.NewWorkspaceStore(workspace.MainPath)
	if err := store.Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: workspace.ID, ActiveChatID: transcript.ChatID,
		Tabs: []sessions.TabTranscript{transcript},
	}); err != nil {
		t.Fatalf("seed chat workspace: %v", err)
	}

	rejection := `llm endpoint returned 400 Bad Request: {"error":{"code":400,"message":"request (131887 tokens) exceeds the available context size (65536 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":131887,"n_ctx":65536}}`
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{{
		{Type: llm.EventError, Error: rejection},
	}, {
		{Type: llm.EventToken, Content: "Recovered answer."},
		{Type: llm.EventComplete, FinishReason: "stop"},
	}}}
	server.llm = streamer
	server.llmCompleter = &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived work.\n### In Progress\nCurrent work.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nNone.\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request."}

	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	chatID := snapshot["activeChatId"].(string)
	if chatID != transcript.ChatID {
		t.Fatalf("expected the seeded chat to be active, got %q", chatID)
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID,
		"requestId": "context-recovery-request", "message": "continue working",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")

	if finished["status"] != "done" {
		t.Fatalf("expected the turn to recover from the context-size rejection, got %v", finished)
	}
	if len(streamer.snapshot()) != 2 {
		t.Fatalf("expected one rejected request plus one recovered retry, got %d requests", len(streamer.snapshot()))
	}
	stored := loadActiveTabTranscript(t, workspace)
	if stored.ContextCheckpoint == nil || stored.ContextCheckpoint.CompactedThrough < 2 {
		t.Fatalf("recovery should have committed a compression checkpoint: %#v", stored.ContextCheckpoint)
	}
}

func TestChatBacksOffAfterFailedCompression(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "compression-backoff")
	settings := llm.DefaultSettings()
	settings.ContextLength = 65536
	settings.MaxTokens = 1024
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	server.llmSettings = settings.NormalizedEndpointProfiles()

	// The context starts above the compression threshold, but the only
	// completed exchange is tiny next to the huge recent tail, so every
	// automatic attempt fails with nothing-to-compress. With a one-round
	// cooldown the loop retried every other round; it must now back off and
	// record exactly one failed attempt across three model rounds.
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
			Index: 0, ID: "call-list-1", Type: "function",
			Function: llm.FunctionCallDelta{Name: "filesystem_list", Arguments: `{"path":"."}`},
		}},
		{Type: llm.EventComplete, FinishReason: "tool_calls"},
	}, {
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
			Index: 0, ID: "call-list-2", Type: "function",
			Function: llm.FunctionCallDelta{Name: "filesystem_list", Arguments: `{"path":"."}`},
		}},
		{Type: llm.EventComplete, FinishReason: "tool_calls"},
	}, {
		{Type: llm.EventToken, Content: "Done."},
		{Type: llm.EventComplete, FinishReason: "stop"},
	}}}
	server.llm = streamer

	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	chatID := snapshot["activeChatId"].(string)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID,
		"requestId": "compression-backoff-request",
		"message":   "inspect " + strings.Repeat("x", 140000),
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")

	if finished["status"] != "done" {
		t.Fatalf("expected the turn to complete around failed compression, got %v", finished)
	}
	requests := streamer.snapshot()
	if len(requests) != 3 {
		t.Fatalf("expected three model rounds, got %d", len(requests))
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Turns) != 1 || len(transcript.Turns[0].Compressions) != 1 {
		t.Fatalf("expected exactly one failed compression attempt across three rounds, got %d", len(transcript.Turns[0].Compressions))
	}
	activity := transcript.Turns[0].Compressions[0]
	if activity.Status != "skipped" || !strings.Contains(activity.Error, "not enough completed history") {
		t.Fatalf("expected one skipped nothing-to-compress attempt: %#v", activity)
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
