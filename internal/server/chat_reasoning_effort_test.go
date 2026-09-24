package server

import (
	"testing"

	"github.com/brent/echo/internal/llm"
)

// TestChatSendReasoningEffortOverride verifies that a per-request reasoning
// effort selected in the agent mode popup reaches the LLM request, while the
// endpoint-configured effort is preserved when no override is sent.
func TestChatSendReasoningEffortOverride(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "reasoning-effort-chat")
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "reasoning-request",
		"message": "inspect this", "agentModeId": "general", "reasoningEffort": "low",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat failed: %v", finished)
	}

	request := capturing.lastRequest()
	if request.ReasoningEffort != llm.ReasoningEffortLow {
		t.Fatalf("expected reasoning effort %q, got %q", llm.ReasoningEffortLow, request.ReasoningEffort)
	}
}

// TestChatSendReasoningEffortInvalid verifies an unsupported reasoning effort
// is rejected before any LLM request is made.
func TestChatSendReasoningEffortInvalid(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "reasoning-effort-invalid")
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "reasoning-invalid",
		"message": "inspect this", "agentModeId": "general", "reasoningEffort": "turbo",
	}); err != nil {
		t.Fatal(err)
	}
	errMessage := readUntilMessageType(t, conn, "command_error")
	if errMessage["code"] != "invalid_reasoning_effort" {
		t.Fatalf("expected invalid_reasoning_effort command error, got %v", errMessage)
	}

	request := capturing.lastRequest()
	if request.ReasoningEffort != "" {
		t.Fatalf("expected no request to reach the LLM, got request with effort %q", request.ReasoningEffort)
	}
}

// TestGoalStartReasoningEffortOverride verifies goal_start also carries the
// popup-level thinking selection (goal_start is forwarded into send()).
func TestGoalStartReasoningEffortOverride(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "reasoning-effort-goal")
	streamer := &lifecycleSequenceStreamer{sequences: [][]llm.StreamEvent{
		{goalToolCall("goal-reasoning-complete", "complete", "Verified."), {Type: llm.EventComplete, FinishReason: "tool_calls"}},
		{{Type: llm.EventToken, Content: "Verified completion."}, {Type: llm.EventComplete, FinishReason: "stop"}},
	}}
	s.llm = streamer

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "goal_start", "workspaceId": workspace.ID, "requestId": "goal-reasoning-request",
		"message": "Deliver a verified result", "reasoningEffort": "xhigh",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("goal failed: %v", finished)
	}

	requests := streamer.snapshot()
	if len(requests) == 0 || requests[0].ReasoningEffort != llm.ReasoningEffortXHigh {
		t.Fatalf("expected reasoning effort %q on first goal request, got %+v", llm.ReasoningEffortXHigh, requests)
	}
}

// TestChatSendNoOverridePreservesEndpointEffort verifies that omitting the
// override leaves the endpoint-configured reasoning effort intact.
func TestChatSendNoOverridePreservesEndpointEffort(t *testing.T) {
	s, _ := newTestServer(t)
	cfg := llm.DefaultSettings()
	cfg.Endpoints[0].ReasoningEffort = llm.ReasoningEffortMax
	cfg.EndpointSelection.Chat = cfg.Endpoints[0].ID
	if err := s.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	s.initLLM()
	workspace := createChatWorkspace(t, s, "reasoning-effort-default")
	capturing := &capturingStreamer{}
	s.llm = capturing

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	defer conn.Close()
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "reasoning-default",
		"message": "inspect this", "agentModeId": "general",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat failed: %v", finished)
	}

	request := capturing.lastRequest()
	if request.ReasoningEffort != llm.ReasoningEffortMax {
		t.Fatalf("expected endpoint reasoning effort %q to be preserved, got %q", llm.ReasoningEffortMax, request.ReasoningEffort)
	}
}
