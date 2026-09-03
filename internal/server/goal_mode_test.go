package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

type goalGateSequenceStreamer struct {
	mu          sync.Mutex
	requests    []llm.ChatRequest
	sequences   [][]llm.StreamEvent
	gateIndex   int
	gateStarted chan struct{}
	release     chan struct{}
	gateOnce    sync.Once
}

func (s *goalGateSequenceStreamer) StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream {
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
	events := make(chan llm.StreamEvent, len(sequence)+1)
	if index != s.gateIndex {
		for _, event := range sequence {
			events <- event
		}
		close(events)
		return &llm.Stream{ID: "goal-sequence", Events: events}
	}
	s.gateOnce.Do(func() { close(s.gateStarted) })
	go func() {
		defer close(events)
		select {
		case <-s.release:
			for _, event := range sequence {
				events <- event
			}
		case <-ctx.Done():
			events <- llm.StreamEvent{Type: llm.EventCanceled}
		}
	}()
	return &llm.Stream{ID: "goal-sequence", Events: events}
}

func (s *goalGateSequenceStreamer) snapshot() []llm.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.ChatRequest(nil), s.requests...)
}

func goalToolCall(id, status, reason string) llm.StreamEvent {
	return llm.StreamEvent{
		Type: llm.EventToolCall,
		ToolCall: &llm.ToolCallDelta{
			Index: 0, ID: id, Type: "function",
			Function: llm.FunctionCallDelta{
				Name:      tools.UpdateGoalToolName,
				Arguments: `{"status":"` + status + `","reason":"` + reason + `"}`,
			},
		},
	}
}

func requestHasTool(request llm.ChatRequest, name string) bool {
	for _, tool := range request.Tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

func TestGoalModeContinuesAcrossCheckpointUntilExplicitCompletion(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-completion")
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{
			{Type: llm.EventToken, Content: "Implemented the first part; I will verify it next."},
			{Type: llm.EventComplete, FinishReason: "stop", Usage: &llm.Usage{TotalTokens: 11}},
		},
		{
			goalToolCall("goal-complete", "complete", "Implementation and checks passed."),
			{Type: llm.EventComplete, FinishReason: "tool_calls", Usage: &llm.Usage{TotalTokens: 13}},
		},
		{
			{Type: llm.EventToken, Content: "The goal is complete and verified."},
			{Type: llm.EventComplete, FinishReason: "stop", Usage: &llm.Usage{TotalTokens: 7}},
		},
	}}
	server.llm = streamer

	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "goal-request", "message": "Deliver a verified result",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("expected completed goal turn, got %v", finished)
	}
	completion := readUntilMessageType(t, conn, "chat_completed")
	if completion["workspaceId"] != workspace.ID {
		t.Fatalf("unexpected completion notification: %v", completion)
	}

	requests := streamer.snapshot()
	if len(requests) != 3 {
		t.Fatalf("ordinary prose must be a checkpoint, not completion; got %d requests", len(requests))
	}
	if !requestHasTool(requests[0], tools.UpdateGoalToolName) || !requestHasTool(requests[1], tools.UpdateGoalToolName) {
		t.Fatalf("goal requests did not expose update_goal: %#v", requests)
	}
	if len(requests[2].Tools) != 0 {
		t.Fatalf("the terminal summary request must disable tools: %#v", requests[2].Tools)
	}
	continuationFound := false
	for _, message := range requests[1].Messages {
		if message.Name == "echo-goal-continuation" {
			continuationFound = true
			break
		}
	}
	if !continuationFound {
		t.Fatalf("checkpoint continuation was not injected: %#v", requests[1].Messages)
	}

	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Goals) != 1 || transcript.CurrentGoalID != transcript.Goals[0].ID {
		t.Fatalf("goal identity/history was not persisted: %#v", transcript)
	}
	goal := transcript.Goals[0]
	if goal.Status != sessions.GoalStatusCompleted || goal.Outcome != "Implementation and checks passed." || goal.StepCount != 3 || goal.TokensUsed != 31 {
		t.Fatalf("unexpected completed goal state: %#v", goal)
	}
	if len(transcript.Turns) != 1 || transcript.Turns[0].GoalID != goal.ID || transcript.Turns[0].GoalOrigin != "start" {
		t.Fatalf("goal turn metadata was not persisted: %#v", transcript.Turns)
	}
	steps := transcript.Turns[0].AssistantTurns
	if len(steps) != 3 || !steps[0].GoalCheckpoint || steps[1].GoalCheckpoint || steps[2].GoalCheckpoint {
		t.Fatalf("unexpected checkpoint markers: %#v", steps)
	}
	if steps[0].GoalOrigin != "start" || steps[1].GoalOrigin != "continuation" || steps[2].GoalOrigin != "continuation" {
		t.Fatalf("goal assistant batches were not tagged by origin: %#v", steps)
	}
	persistedContinuation := false
	for _, message := range transcript.Messages {
		if message.Name == "echo-goal" {
			t.Fatalf("goal system prefix leaked into the durable transcript: %#v", transcript.Messages)
		}
		if message.Name == "echo-goal-continuation" {
			persistedContinuation = true
		}
	}
	if !persistedContinuation {
		t.Fatal("durable transcript did not retain the hidden continuation boundary")
	}
}

func TestGoalTerminalFinalizationStaysActiveAndAcceptsBoundarySteering(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-terminal-steering")
	streamer := &goalGateSequenceStreamer{
		gateIndex: 1, gateStarted: make(chan struct{}), release: make(chan struct{}),
		sequences: [][]llm.StreamEvent{
			{goalToolCall("first-complete", "complete", "Initially verified."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
			{{Type: llm.EventToken, Content: "Initial final summary."}, {Type: llm.EventComplete, FinishReason: "stop"}},
			{goalToolCall("revised-complete", "complete", "Late guidance was incorporated and verified."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
			{{Type: llm.EventToken, Content: "The revised goal is complete."}, {Type: llm.EventComplete, FinishReason: "stop"}},
		},
	}
	server.llm = streamer
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "goal-terminal-steering-request", "message": "Finish after all guidance",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamer.gateStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("tools-disabled final response did not start")
	}
	finalizing := loadActiveTabTranscript(t, workspace).Goals[0]
	if finalizing.Status != sessions.GoalStatusActive || finalizing.PendingStatus != sessions.GoalStatusCompleted || finalizing.CompletedAt != nil {
		t.Fatalf("terminal declaration ended the goal before its final response: %#v", finalizing)
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_steer", "workspaceId": workspace.ID,
		"requestId": "late-terminal-guidance", "message": "Also verify the late requirement",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, conn, "goal_steering_queued")
	close(streamer.release)
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("goal did not finish after boundary steering: %v", finished)
	}
	readUntilMessageType(t, conn, "chat_completed")
	requests := streamer.snapshot()
	if len(requests) != 4 {
		t.Fatalf("expected final response, steered continuation, and revised final response; got %d requests", len(requests))
	}
	foundGuidance := false
	for _, message := range requests[2].Messages {
		foundGuidance = foundGuidance || (message.Role == llm.RoleUser && strings.Contains(message.Content, "late requirement"))
	}
	if !foundGuidance {
		t.Fatalf("late guidance was not applied at the next boundary: %#v", requests[2].Messages)
	}
	goal := loadActiveTabTranscript(t, workspace).Goals[0]
	if goal.Status != sessions.GoalStatusCompleted || goal.PendingStatus != "" || goal.Outcome != "Late guidance was incorporated and verified." {
		t.Fatalf("unexpected final goal state after late guidance: %#v", goal)
	}
}

func TestGoalModeRejectsEntireMixedTerminalToolBatch(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-solo-tool")
	rootLabel := normalizeWorkspaceFolderLabel(filepath.Base(workspace.MainPath))
	marker := filepath.Join(workspace.MainPath, "must-not-exist.txt")
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{
			goalToolCall("mixed-goal", "complete", "Too early"),
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
				Index: 1, ID: "mixed-write", Type: "function",
				Function: llm.FunctionCallDelta{
					Name:      "filesystem_create_text",
					Arguments: `{"path":"` + rootLabel + `/must-not-exist.txt","content":"mutation"}`,
				},
			}},
			{Type: llm.EventComplete, FinishReason: "tool_calls"},
		},
		{
			goalToolCall("solo-goal", "complete", "Verified after correcting the call."),
			{Type: llm.EventComplete, FinishReason: "tool_calls"},
		},
		{
			{Type: llm.EventToken, Content: "Verified completion."},
			{Type: llm.EventComplete, FinishReason: "stop"},
		},
	}}
	server.llm = streamer

	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "goal-solo-request", "message": "Do not mutate after completion",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("goal did not recover from a mixed terminal batch: %v", finished)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the mixed tool batch executed a mutation after update_goal: %v", err)
	}
	requests := streamer.snapshot()
	if len(requests) != 3 {
		t.Fatalf("expected mixed rejection, corrected terminal call, and summary; got %d requests", len(requests))
	}
	foundRejection := false
	for _, message := range requests[1].Messages {
		if message.Role == llm.RoleTool && strings.Contains(message.Content, "goal_status_must_be_solo") {
			foundRejection = true
		}
	}
	if !foundRejection {
		t.Fatalf("model did not receive the mixed-batch rejection: %#v", requests[1].Messages)
	}
}

func TestGoalPauseQueuesSteeringAndResumeUsesLockedModel(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-resume")
	gated := &gatedStreamer{started: make(chan struct{}), release: make(chan struct{})}
	server.llm = gated
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "goal-pause-request", "message": "Keep working until verified",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, conn, "token")
	if err := conn.WriteJSON(map[string]any{"type": "goal_pause", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	pausedTurn := readUntilSessionEvent(t, conn, "turn_finished")
	if pausedTurn["status"] != "stopped" {
		t.Fatalf("expected cooperatively stopped turn, got %v", pausedTurn)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "goal_steer", "workspaceId": workspace.ID,
		"requestId": "queued-guidance", "message": "Also validate the edge case",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, conn, "goal_steering_queued")
	queued := loadActiveTabTranscript(t, workspace)
	if len(queued.Goals) != 1 || queued.Goals[0].Status != sessions.GoalStatusPaused || len(queued.Goals[0].PendingSteering) != 1 {
		t.Fatalf("guidance submitted while paused should queue without resuming: %#v", queued.Goals)
	}

	resumeStreamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{goalToolCall("resumed-complete", "complete", "Edge case validated."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{{Type: llm.EventToken, Content: "Resumed work is verified."}, {Type: llm.EventComplete, FinishReason: "stop"}},
	}}
	server.llm = resumeStreamer
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_resume", "workspaceId": workspace.ID, "requestId": "resume-request",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("resumed goal did not complete: %v", finished)
	}
	requests := resumeStreamer.snapshot()
	if len(requests) != 2 {
		t.Fatalf("unexpected resumed request count: %d", len(requests))
	}
	if requests[0].Model != llm.DefaultModel {
		t.Fatalf("resume changed the goal's locked model: got %q want %q", requests[0].Model, llm.DefaultModel)
	}
	foundGuidance := false
	for _, message := range requests[0].Messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Content, "validate the edge case") {
			foundGuidance = true
		}
	}
	if !foundGuidance {
		t.Fatalf("queued steering was not applied on resume: %#v", requests[0].Messages)
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Goals) != 1 || transcript.Goals[0].Status != sessions.GoalStatusCompleted || len(transcript.Goals[0].PendingSteering) != 0 {
		t.Fatalf("unexpected resumed goal state: %#v", transcript.Goals)
	}
	if len(transcript.Turns) != 2 || transcript.Turns[1].GoalOrigin != "resume" || len(transcript.Turns[1].GoalSteering) != 1 {
		t.Fatalf("resume/steering metadata was not preserved: %#v", transcript.Turns)
	}
}

func TestBlockedGoalGuidanceAutomaticallyResumesSameGoal(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-blocked-resume")
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{goalToolCall("blocked", "blocked", "Need the missing deployment target."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{{Type: llm.EventToken, Content: "I need the deployment target."}, {Type: llm.EventComplete, FinishReason: "stop"}},
		{goalToolCall("complete", "complete", "Deployment target supplied and verified."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{{Type: llm.EventToken, Content: "The deployment is verified."}, {Type: llm.EventComplete, FinishReason: "stop"}},
	}}
	server.llm = streamer
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "blocked-goal", "message": "Deploy and verify",
	}); err != nil {
		t.Fatal(err)
	}
	firstFinished := readUntilSessionEvent(t, conn, "turn_finished")
	if firstFinished["status"] != "done" {
		t.Fatalf("blocked goal summary failed: %v", firstFinished)
	}
	attention := readUntilMessageType(t, conn, "goal_attention")
	attentionGoal, _ := attention["goal"].(map[string]any)
	if attentionGoal["status"] != string(sessions.GoalStatusBlocked) {
		t.Fatalf("expected needs-attention notification for blocked goal: %v", attention)
	}

	if err := conn.WriteJSON(map[string]any{
		"type": "goal_steer", "workspaceId": workspace.ID,
		"requestId": "blocking-answer", "message": "Deploy to staging",
	}); err != nil {
		t.Fatal(err)
	}
	secondFinished := readUntilSessionEvent(t, conn, "turn_finished")
	if secondFinished["status"] != "done" {
		t.Fatalf("guided blocked goal did not resume: %v", secondFinished)
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Goals) != 1 || transcript.Goals[0].Status != sessions.GoalStatusCompleted {
		t.Fatalf("guidance should resume the same goal identity: %#v", transcript.Goals)
	}
	if len(transcript.Turns) != 2 || transcript.Turns[0].GoalID != transcript.Turns[1].GoalID || len(transcript.Turns[1].GoalSteering) != 1 {
		t.Fatalf("blocked-resume turns were not grouped with their steering: %#v", transcript.Turns)
	}
	if len(streamer.snapshot()) != 4 {
		t.Fatalf("expected blocked summary plus automatically resumed completion, got %d requests", len(streamer.snapshot()))
	}
}

func TestGoalProviderErrorPausesAndRequestsAttention(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-provider-error")
	server.llm = errorStreamer{}
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "errored-goal", "message": "Complete this despite provider failure",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "error" {
		t.Fatalf("expected provider error, got %v", finished)
	}
	attention := readUntilMessageType(t, conn, "goal_attention")
	attentionGoal, _ := attention["goal"].(map[string]any)
	if attentionGoal["status"] != string(sessions.GoalStatusPaused) || !strings.Contains(attentionGoal["lastError"].(string), "model failed") {
		t.Fatalf("provider error did not produce a paused needs-attention goal: %v", attention)
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Goals) != 1 || transcript.Goals[0].Status != sessions.GoalStatusPaused || !strings.Contains(transcript.Goals[0].LastError, "model failed") {
		t.Fatalf("provider error was not durably paused: %#v", transcript.Goals)
	}
}

func TestGoalFinalSummaryErrorReopensGoalAsPaused(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-final-error")
	server.llm = &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{goalToolCall("premature-complete", "complete", "Work was verified."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{{Type: llm.EventToken, Content: "Partial final summary."}, {Type: llm.EventError, Error: "final summary provider failed"}},
	}}
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "goal-final-error-request", "message": "Finish and summarize safely",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "error" {
		t.Fatalf("expected terminal summary failure, got %v", finished)
	}
	attention := readUntilMessageType(t, conn, "goal_attention")
	attentionGoal, _ := attention["goal"].(map[string]any)
	if attentionGoal["status"] != string(sessions.GoalStatusPaused) {
		t.Fatalf("final summary failure left a false terminal state: %v", attention)
	}
	transcript := loadActiveTabTranscript(t, workspace)
	goal := transcript.Goals[0]
	if goal.Status != sessions.GoalStatusPaused || goal.CompletedAt != nil || goal.Outcome != "" || !strings.Contains(goal.LastError, "final summary provider failed") {
		t.Fatalf("final summary failure was not durably recoverable: %#v", goal)
	}
}

func TestGoalFinalSummaryCannotExecuteUnexpectedToolCalls(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-final-tool-guard")
	rootLabel := normalizeWorkspaceFolderLabel(filepath.Base(workspace.MainPath))
	marker := filepath.Join(workspace.MainPath, "final-must-not-exist.txt")
	server.llm = &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{goalToolCall("complete-before-summary", "complete", "Verified."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
				Index: 0, ID: "forbidden-final-write", Type: "function",
				Function: llm.FunctionCallDelta{
					Name:      "filesystem_create_text",
					Arguments: `{"path":"` + rootLabel + `/final-must-not-exist.txt","content":"mutation"}`,
				},
			}},
			{Type: llm.EventComplete, FinishReason: "tool_calls"},
		},
	}}
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "goal-final-tool-request", "message": "Do not mutate after declaring completion",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "error" {
		t.Fatalf("unexpected final tool call should fail safely: %v", finished)
	}
	readUntilMessageType(t, conn, "goal_attention")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("a tool ran after update_goal was accepted: %v", err)
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if transcript.Goals[0].Status != sessions.GoalStatusPaused || !strings.Contains(transcript.Goals[0].LastError, "tools-disabled") {
		t.Fatalf("unexpected final tool call did not pause the goal: %#v", transcript.Goals[0])
	}
	requests := server.llm.(*lifecycleSequenceStreamer).snapshot()
	if len(requests) != 2 || len(requests[1].Tools) != 0 {
		t.Fatalf("final request exposed tools: %#v", requests)
	}
}

func TestGoalModeRetainsContextCompressionAcrossItsLoop(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-context-compression")
	settings := llm.DefaultSettings()
	settings.ContextLength = 200000
	settings.MaxTokens = 1024
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	server.llmSettings = settings.NormalizedEndpointProfiles()
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "Opening request"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("a", 150000)},
	}
	transcript := sessions.TabTranscript{
		ChatID: "goal-context-chat", Preview: "Opening request", Revision: 1, Messages: canonical,
		Turns: []sessions.Turn{{
			ID: "goal-context-seed", RequestID: "goal-context-seed-request", UserContent: "Opening request",
			UserMessageIndex: 0, Status: "done", AssistantTurns: []sessions.AssistantTurn{},
		}},
	}
	store := sessions.NewWorkspaceStore(workspace.MainPath)
	if err := store.Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: workspace.ID, ActiveChatID: transcript.ChatID,
		Tabs: []sessions.TabTranscript{transcript},
	}); err != nil {
		t.Fatal(err)
	}
	rejection := `llm endpoint returned 400 Bad Request: {"error":{"message":"request exceeds maximum context length 65536"}}`
	server.llm = &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{{Type: llm.EventError, Error: rejection}},
		{goalToolCall("compressed-complete", "complete", "Compressed history and verification passed."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{{Type: llm.EventToken, Content: "The compressed long-running goal is complete."}, {Type: llm.EventComplete, FinishReason: "stop"}},
	}}
	server.llmCompleter = &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Progress\n### Done\nArchived work.\n### In Progress\nVerify the goal.\n### Blocked\nNone.\n## Next Steps\nVerify and finish.\n## Critical Exact Context\nOpening request."}
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID, "chatId": snapshot["activeChatId"],
		"requestId": "goal-context-request", "message": "Complete this long-running goal",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("goal did not recover through context compression: %v", finished)
	}
	stored := loadActiveTabTranscript(t, workspace)
	if stored.ContextCheckpoint == nil || stored.ContextCheckpoint.CompactedThrough < 2 {
		t.Fatalf("goal did not persist its context checkpoint: %#v", stored.ContextCheckpoint)
	}
	goal := stored.Goals[0]
	if goal.Status != sessions.GoalStatusCompleted || goal.StepCount != 2 {
		t.Fatalf("goal state did not survive compression: %#v", goal)
	}
}

func TestGoalClearArchivesStateWithoutDeletingTranscript(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "goal-clear")
	server.llm = &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{goalToolCall("blocked-before-clear", "blocked", "Waiting for user choice."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{{Type: llm.EventToken, Content: "Please provide the missing choice."}, {Type: llm.EventComplete, FinishReason: "stop"}},
	}}
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID,
		"requestId": "goal-to-clear", "message": "Reach a decision",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, conn, "turn_finished")
	readUntilMessageType(t, conn, "goal_attention")
	if err := conn.WriteJSON(map[string]any{"type": "goal_clear", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	cleared := readUntilSessionEvent(t, conn, "goal_updated")
	if cleared["goal"] != nil {
		t.Fatalf("cleared goal should no longer be current: %v", cleared)
	}
	transcript := loadActiveTabTranscript(t, workspace)
	if transcript.CurrentGoalID != "" || len(transcript.Goals) != 1 || transcript.Goals[0].Status != sessions.GoalStatusCleared {
		t.Fatalf("goal was not archived as cleared: %#v", transcript.Goals)
	}
	if len(transcript.Turns) != 1 || len(transcript.Messages) == 0 || !strings.Contains(transcript.Turns[0].UserContent, "Reach a decision") {
		t.Fatalf("clearing the goal deleted its transcript: %#v", transcript)
	}
}
