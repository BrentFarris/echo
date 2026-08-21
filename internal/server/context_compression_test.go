package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
)

type contextCompressionCompleter struct {
	summary  string
	requests []llm.ChatRequest
	err      error
	failOnce bool
}

func (c *contextCompressionCompleter) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	c.requests = append(c.requests, request)
	if c.failOnce && len(c.requests) == 1 {
		return llm.ChatResponse{}, fmt.Errorf("context_length_exceeded: summary input was too large")
	}
	if c.err != nil {
		return llm.ChatResponse{}, c.err
	}
	return llm.ChatResponse{
		Choices: []llm.ChatChoice{{Message: llm.Message{Role: llm.RoleAssistant, Content: c.summary}}},
		Usage:   &llm.Usage{PromptTokens: 100, CompletionTokens: 25, TotalTokens: 125},
	}, nil
}

func compressionTestSettings() llm.Settings {
	settings := llm.DefaultSettings()
	settings.ContextLength = 8192
	settings.MaxTokens = 512
	settings.ContextCompressionThresholdPercent = 70
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	return settings.NormalizedEndpointProfiles()
}

func compressionTestHistory(exchanges int) []llm.Message {
	messages := []llm.Message{{Role: llm.RoleUser, Content: "Opening request: preserve this exact instruction."}}
	for index := 0; index < exchanges; index++ {
		messages = append(messages,
			llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat(string(rune('a'+index%20)), 2400)},
			llm.Message{Role: llm.RoleUser, Content: "Follow-up " + strings.Repeat(string(rune('A'+index%20)), 1400)},
		)
	}
	return messages
}

func TestBuildCompressedModelHistoryPreservesProtectedHeadAndRawTail(t *testing.T) {
	media := llm.Message{Role: llm.RoleUser, Content: "latest exact request", ContentParts: []llm.MessageContentPart{
		llm.TextContentPart("latest exact request"), llm.ImageURLContentPart("data:image/png;base64,AAAA"),
	}}
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "opening prompt"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-old", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"old.go"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "call-old", Content: "old result"},
		media,
		{Role: llm.RoleAssistant, Content: "latest answer"},
	}
	checkpoint := &sessions.ContextCheckpoint{
		Summary: "## Goal\nContinue the task.", ProtectedHeadIndex: 0, CompactedThrough: 3,
	}

	modelHistory := buildCompressedModelHistory(canonical, checkpoint)
	if len(modelHistory) != 4 || modelHistory[0].Content != "opening prompt" || modelHistory[1].Name != contextSummaryName {
		t.Fatalf("unexpected compressed history: %#v", modelHistory)
	}
	if !strings.Contains(modelHistory[1].Content, "## Goal") || modelHistory[2].Content != "latest exact request" {
		t.Fatalf("summary or raw tail was not preserved: %#v", modelHistory)
	}
	if len(modelHistory[2].ContentParts) != 2 || modelHistory[2].ContentParts[1].ImageURL == nil {
		t.Fatalf("tail media was not preserved: %#v", modelHistory[2])
	}
	modelHistory[2].ContentParts[0].Text = "changed"
	if canonical[3].ContentParts[0].Text == "changed" {
		t.Fatal("compressed model history aliased canonical media content")
	}
}

func TestContextMeterIncludesSchemasMediaStreamDeltasAndProviderUsage(t *testing.T) {
	settings := compressionTestSettings()
	messages := []llm.Message{{Role: llm.RoleUser, Content: "inspect", ContentParts: []llm.MessageContentPart{
		llm.TextContentPart("inspect"), llm.ImageURLContentPart("data:image/png;base64,AAAA"),
	}}}
	withoutTools := contextRequestTokens(settings, messages, nil)
	withTools := contextRequestTokens(settings, messages, []llm.Tool{{Type: "function", Function: llm.ToolFunction{
		Name: "read_file", Description: strings.Repeat("schema", 100), Parameters: map[string]any{"type": "object"},
	}}})
	if withoutTools < 1000 || withTools <= withoutTools {
		t.Fatalf("meter omitted media or tool schemas: without=%d with=%d", withoutTools, withTools)
	}
	delta := estimateStreamEventTokens(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
		ID: "call-1", Type: "function", Function: llm.FunctionCallDelta{Name: "read_file", Arguments: `{"path":"main.go"}`},
	}})
	if delta <= 0 || !contextCompressionThresholdReached(90, delta, 100, nil) {
		t.Fatalf("streamed tool-call delta did not cross the estimated threshold: delta=%d", delta)
	}
	if !contextCompressionThresholdReached(1, 0, 100, &llm.Usage{PromptTokens: 75, CompletionTokens: 25}) {
		t.Fatal("provider-reported usage did not take precedence at the response boundary")
	}
}

func TestSelectCompressionBoundaryUsesUserExchangeBoundaries(t *testing.T) {
	settings := compressionTestSettings()
	canonical := compressionTestHistory(8)
	head, start, cutoff, err := selectCompressionBoundary(settings, canonical, nil, nil, nil)
	if err != nil {
		t.Fatalf("select boundary: %v", err)
	}
	if head != 0 || start != 1 || cutoff <= start || cutoff >= len(canonical) {
		t.Fatalf("unexpected boundary head=%d start=%d cutoff=%d", head, start, cutoff)
	}
	if canonical[cutoff].Role != llm.RoleUser {
		t.Fatalf("cutoff split a completed exchange at role %q", canonical[cutoff].Role)
	}
}

func TestValidateContextMessageOrderingRejectsSplitToolGroups(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "inspect"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
		{Role: llm.RoleUser, Content: "continue"},
	}
	if err := validateContextMessageOrdering(messages); err == nil {
		t.Fatal("expected a missing tool result to fail validation")
	}
	messages[2] = llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Content: "ok"}
	if err := validateContextMessageOrdering(messages); err != nil {
		t.Fatalf("valid tool group was rejected: %v", err)
	}
}

func TestCompressionChunkingKeepsToolGroupsAndRetriesOneOversizedSummary(t *testing.T) {
	toolGroup := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-large", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
		{Role: llm.RoleTool, ToolCallID: "call-large", Content: strings.Repeat("result", 1000)},
		{Role: llm.RoleUser, Content: "next exchange"},
		{Role: llm.RoleAssistant, Content: "done"},
	}
	chunks := splitCompressionChunks(toolGroup, 100)
	if len(chunks) < 2 || len(chunks[0]) != 2 || chunks[0][0].Role != llm.RoleAssistant || chunks[0][1].Role != llm.RoleTool {
		t.Fatalf("tool group was split across compression chunks: %#v", chunks)
	}

	settings := compressionTestSettings()
	completer := &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived.\n### In Progress\nContinue.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nNone.\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request.", failOnce: true}
	server := &Server{llmCompleter: completer, llmSettings: settings}
	result, err := server.compressContext(context.Background(), settings, compressionTestHistory(10), nil, nil, nil, 0, "estimated")
	if err != nil {
		t.Fatalf("compression did not retry a splittable context-limit failure: %v", err)
	}
	if len(completer.requests) <= result.ChunkCount || result.ChunkCount < 2 {
		t.Fatalf("expected one failed request followed by smaller aligned chunks: calls=%d chunks=%d", len(completer.requests), result.ChunkCount)
	}
}

func TestCompressContextCommitsOnlyMeaningfulNonemptySummary(t *testing.T) {
	settings := compressionTestSettings()
	canonical := compressionTestHistory(8)
	completer := &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nKeep identifiers exact.\n## Progress\n### Done\nArchived work.\n### In Progress\nCurrent work.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nold.go\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request."}
	server := &Server{llmCompleter: completer, llmSettings: settings}

	result, err := server.compressContext(context.Background(), settings, canonical, nil, nil, nil, 0, "estimated")
	if err != nil {
		t.Fatalf("compress context: %v", err)
	}
	if result.Checkpoint == nil || result.Checkpoint.Summary == "" || result.AfterTokens >= result.BeforeTokens {
		t.Fatalf("compression did not produce a reduced checkpoint: %#v", result)
	}
	if result.Checkpoint.ProtectedHeadIndex != 0 || result.Checkpoint.CompactedThrough <= 1 || result.RetiredMessages <= 0 {
		t.Fatalf("unexpected checkpoint boundary: %#v", result.Checkpoint)
	}
	if len(completer.requests) == 0 || len(completer.requests[0].Tools) != 0 || completer.requests[0].Stream {
		t.Fatalf("summary request must be non-streaming and tool-free: %#v", completer.requests)
	}

	empty := &contextCompressionCompleter{summary: "   "}
	server.llmCompleter = empty
	previous := *result.Checkpoint
	_, err = server.compressContext(context.Background(), settings, append(canonical, compressionTestHistory(3)[1:]...), result.Checkpoint, nil, nil, 0, "estimated")
	if err == nil || *result.Checkpoint != previous {
		t.Fatalf("empty summary should fail atomically without changing the checkpoint: err=%v checkpoint=%#v", err, result.Checkpoint)
	}
}

func TestConversationHistorySearchIsBoundedAndFilterable(t *testing.T) {
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "opening request"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"src/hidden.go"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: "exact archived error ECHO-417 in src/hidden.go"},
		{Role: llm.RoleUser, Content: "older follow-up"},
		{Role: llm.RoleAssistant, Content: "resolved ECHO-417"},
		{Role: llm.RoleUser, Content: "current request"},
	}
	checkpoint := &sessions.ContextCheckpoint{Summary: "summary", ProtectedHeadIndex: 0, CompactedThrough: 5}
	arguments, _ := json.Marshal(contextHistorySearchArgs{Query: "ECHO-417 hidden.go", Roles: []string{llm.RoleTool}, Tools: []string{"read_file"}, Limit: 20})

	result := (&chatSession{}).executeContextHistorySearch(canonical, checkpoint, arguments)
	if !result.Success {
		t.Fatalf("history search failed: %#v", result.Error)
	}
	data, err := json.Marshal(result.Output)
	if err != nil {
		t.Fatalf("marshal search output: %v", err)
	}
	var output struct {
		Count   int `json:"count"`
		Matches []struct {
			MessageIndex int    `json:"messageIndex"`
			Tool         string `json:"tool"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("decode search output: %v", err)
	}
	if output.Count != 1 || len(output.Matches) != 1 || output.Matches[0].MessageIndex != 2 || output.Matches[0].Tool != "read_file" {
		t.Fatalf("unexpected filtered match: %#v", output)
	}

	arguments, _ = json.Marshal(contextHistorySearchArgs{Query: "src/hidden.go", Roles: []string{llm.RoleAssistant}, Tools: []string{"read_file"}, Limit: 1})
	result = (&chatSession{}).executeContextHistorySearch(canonical, checkpoint, arguments)
	data, _ = json.Marshal(result.Output)
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("decode tool-argument search output: %v", err)
	}
	if output.Count != 1 || len(output.Matches) != 1 || output.Matches[0].MessageIndex != 1 {
		t.Fatalf("exact tool arguments were not searchable: %#v", output)
	}
}

func TestManualCompressionWorksWhenAutomaticCompressionIsDisabled(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "manual-compression")
	settings := compressionTestSettings()
	settings.ContextLength = 65536
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	settings = settings.NormalizedEndpointProfiles()
	disabled := false
	settings.ContextCompressionEnabled = &disabled
	settings.Endpoints[0].ContextCompressionEnabled = &disabled
	server.llmSettings = settings
	server.llmCompleter = &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived.\n### In Progress\nContinue.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nNone.\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request."}
	canonical := compressionTestHistory(8)
	transcript := sessions.TabTranscript{
		ChatID: "chat-compression", Preview: "Continue", Revision: 1, Messages: canonical,
		Turns: []sessions.Turn{{
			ID: "turn-compression", RequestID: "request-compression", UserContent: canonical[len(canonical)-1].Content,
			UserMessageIndex: len(canonical) - 1, Status: "done", AssistantTurns: []sessions.AssistantTurn{},
		}},
	}
	store := sessions.NewWorkspaceStore(workspace.MainPath)
	if err := store.Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: workspace.ID, ActiveChatID: transcript.ChatID,
		Tabs: []sessions.TabTranscript{transcript},
	}); err != nil {
		t.Fatalf("seed chat workspace: %v", err)
	}

	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	subscribeChat(t, connection, workspace.ID)
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_compress", "workspaceId": workspace.ID, "chatId": transcript.ChatID,
	}); err != nil {
		t.Fatalf("request manual compression: %v", err)
	}
	readSessionEventForChat(t, connection, transcript.ChatID, "context_compression_started")
	completed := readSessionEventForChat(t, connection, transcript.ChatID, "context_compression_completed")
	activity, _ := completed["compression"].(map[string]any)
	if activity["status"] != "completed" || activity["recoveryAvailable"] != true {
		t.Fatalf("unexpected compression lifecycle: %#v", completed)
	}

	stored, err := store.Load(workspace.ID)
	if err != nil {
		t.Fatalf("reload compressed chat: %v", err)
	}
	if len(stored.Tabs) != 1 || stored.Tabs[0].ContextCheckpoint == nil || len(stored.Tabs[0].Messages) != len(canonical) {
		t.Fatalf("checkpoint or canonical history was not persisted atomically: %#v", stored)
	}
}

func TestManualCompressionQueuesDuringFirstStreamingTurn(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "queued-first-turn-compression")
	settings := compressionTestSettings()
	settings.ContextLength = 65536
	disabled := false
	settings.ContextCompressionEnabled = &disabled
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	settings.Endpoints[0].ContextCompressionEnabled = &disabled
	server.llmSettings = settings.NormalizedEndpointProfiles()
	streamer := &gatedStreamer{started: make(chan struct{}), release: make(chan struct{})}
	server.llm = streamer

	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	if err := connection.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, connection)
	chatID := snapshot["activeChatId"].(string)
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID,
		"requestId": "queued-compression", "message": "keep streaming",
	}); err != nil {
		t.Fatal(err)
	}
	readSessionEventForChat(t, connection, chatID, "turn_started")
	select {
	case <-streamer.started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_compress", "workspaceId": workspace.ID, "chatId": chatID,
	}); err != nil {
		t.Fatal(err)
	}
	queued := readSessionEventForChat(t, connection, chatID, "context_compression_queued")
	activity, _ := queued["compression"].(map[string]any)
	if activity["status"] != "queued" || activity["phase"] != "mid_turn" {
		t.Fatalf("unexpected queued lifecycle: %#v", queued)
	}
	close(streamer.release)
	readSessionEventForChat(t, connection, chatID, "turn_finished")
}

func TestDeferredManualCompressionUpdatesItsOriginalActivityAnchor(t *testing.T) {
	anchor := 0
	session := &chatSession{
		transcript: sessions.TabTranscript{Turns: []sessions.Turn{{
			ID: "completed-turn", Compressions: []sessions.CompressionActivity{{
				ID: "compression-deferred", Trigger: "manual", Phase: "mid_turn", Status: "queued", AfterAssistantNumber: &anchor,
			}},
		}}},
		active: &sessions.Turn{ID: "next-turn"},
	}
	existing, turnID, ok := session.compressionActivityLocked("compression-deferred")
	if !ok || turnID != "completed-turn" || existing.AfterAssistantNumber == nil || *existing.AfterAssistantNumber != 0 {
		t.Fatalf("deferred activity anchor was not found: activity=%#v turn=%q", existing, turnID)
	}
	existing.Status = "completed"
	session.upsertCompressionActivityLocked(existing)
	if session.transcript.Turns[0].Compressions[0].Status != "completed" || len(session.active.Compressions) != 0 {
		t.Fatalf("deferred activity was duplicated onto the next turn: transcript=%#v active=%#v", session.transcript, session.active)
	}
}
