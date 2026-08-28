package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

type researchFlowStreamer struct {
	mu               sync.Mutex
	parentRequests   []llm.ChatRequest
	researchRequests []llm.ChatRequest
	workspaceLabel   string
}

type disabledResearchStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (f *disabledResearchStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	count := len(f.requests)
	f.mu.Unlock()
	events := make(chan llm.StreamEvent, 3)
	if count == 1 {
		events <- researchToolCall("hallucinated", tools.ResearchAgentsSpawnToolName, `{"agents":[{"task":"Should not run"}]}`)
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Research agents were unavailable."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "disabled-research", Events: events}
}

func (f *disabledResearchStreamer) captured() []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.ChatRequest(nil), f.requests...)
}

func (f *researchFlowStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	isResearch := len(request.Messages) > 0 && request.Messages[0].Name == "echo-research-agent"
	f.mu.Lock()
	if isResearch {
		f.researchRequests = append(f.researchRequests, request)
	} else {
		f.parentRequests = append(f.parentRequests, request)
	}
	var callCount int
	if isResearch {
		callCount = len(f.researchRequests)
	} else {
		callCount = len(f.parentRequests)
	}
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 6)
	if isResearch {
		if callCount == 1 {
			events <- llm.StreamEvent{Type: llm.EventReasoning, Content: "Inspecting the workspace.", Raw: json.RawMessage(`{"choices":[{"delta":{"reasoning_content":"Inspecting"}}]}`)}
			events <- researchToolCall("child-list", "filesystem_list", `{"path":"`+f.workspaceLabel+`"}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		} else if callCount == 2 {
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "The workspace listing was inspected and contains the expected files."}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop", Usage: &llm.Usage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20}}
		} else {
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "Follow-up confirmed the original workspace evidence."}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		}
	} else {
		switch callCount {
		case 1:
			events <- researchToolCall("spawn", tools.ResearchAgentsSpawnToolName, `{"agents":[{"name":"Workspace scout","task":"Inspect the workspace listing."}]}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		case 2:
			// This is intentionally premature. The finalization guard must keep it
			// out of the visible and persisted assistant response.
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "PREMATURE FINAL"}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		case 3:
			events <- researchToolCall("wait", tools.ResearchAgentsWaitToolName, `{"waitFor":"all","timeoutSeconds":5}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		case 4:
			events <- researchToolCall("send", tools.ResearchAgentSendToolName, `{"agentId":"agent-1","message":"Confirm the original evidence."}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		case 5:
			events <- researchToolCall("wait-follow-up", tools.ResearchAgentsWaitToolName, `{"waitFor":"all","timeoutSeconds":5}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		default:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "Final answer using the collected research."}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		}
	}
	close(events)
	return &llm.Stream{ID: "research-flow", Events: events}
}

func researchToolCall(id, name, arguments string) llm.StreamEvent {
	return llm.StreamEvent{
		Type: llm.EventToolCall,
		ToolCall: &llm.ToolCallDelta{
			Index: 0, ID: id, Type: "function",
			Function: llm.FunctionCallDelta{Name: name, Arguments: arguments},
		},
	}
}

func (f *researchFlowStreamer) requests() ([]llm.ChatRequest, []llm.ChatRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.ChatRequest(nil), f.parentRequests...), append([]llm.ChatRequest(nil), f.researchRequests...)
}

func TestResearchAgentsRunPrivatelyAndGuardFinalization(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "research-flow")
	streamer := &researchFlowStreamer{workspaceLabel: normalizeWorkspaceFolderLabel(filepath.Base(workspace.MainPath))}
	server.llm = streamer

	url := startWebSocketTestServer(t, server)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "research-request",
		"message": "Research the workspace and summarize it.",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("research chat failed: %#v", finished)
	}

	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Turns) != 1 {
		t.Fatalf("expected one persisted turn, got %#v", transcript.Turns)
	}
	turn := transcript.Turns[0]
	var visible strings.Builder
	for _, assistant := range turn.AssistantTurns {
		visible.WriteString(assistant.Content)
	}
	if strings.Contains(visible.String(), "PREMATURE FINAL") {
		t.Fatalf("premature finalization leaked into the response: %q", visible.String())
	}
	if !strings.Contains(visible.String(), "Final answer using the collected research.") {
		t.Fatalf("final synthesis missing: %q", visible.String())
	}
	if len(turn.ResearchAgents) != 0 {
		t.Fatalf("transient research status was persisted: %#v", turn.ResearchAgents)
	}
	if len(turn.ResearchReasoning) != 1 || !strings.Contains(turn.ResearchReasoning[0].Reasoning, "Inspecting") {
		t.Fatalf("attributed research reasoning missing: %#v", turn.ResearchReasoning)
	}
	if len(turn.ResearchTools) != 1 || turn.ResearchTools[0].Name != "filesystem_list" || turn.ResearchTools[0].AgentName != "Workspace scout" {
		t.Fatalf("attributed research tool activity missing: %#v", turn.ResearchTools)
	}

	parent, err := server.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := parent.resolveSurfaceTab(transcript.ChatID, chatSurfaceMain)
	if err != nil {
		t.Fatal(err)
	}
	page, err := session.trajectory.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	wantedResearch := map[string]int{
		"research/agent_created": 1, "research/job_queued": 2, "research/job_start": 2,
		"research/job_end": 2, "research/request_start": 3, "research/assistant_message": 3,
		"research/tool_call": 1, "research/tool_result": 1, "research/report_delivered": 2,
	}
	seenResearch := make(map[string]int)
	for _, event := range page.Events {
		if strings.HasPrefix(event.Type, "research/") {
			seenResearch[event.Type]++
		}
		payload := string(event.Data)
		switch event.Type {
		case "research/request_start":
			if !strings.Contains(payload, `"agentId":"agent-1"`) || !strings.Contains(payload, `"jobId":"agent-1-job-`) || !strings.Contains(payload, "Inspect the workspace listing") {
				t.Fatalf("research request was not fully attributed: %s", event.Data)
			}
		case "research/chunk":
			if strings.Contains(payload, "reasoning_content") && !strings.Contains(payload, `"raw"`) {
				t.Fatalf("research provider raw frame was not retained: %s", event.Data)
			}
		case "research/assistant_message":
			if strings.Contains(payload, "expected files") && !strings.Contains(payload, `"total_tokens":20`) {
				t.Fatalf("research usage was not retained: %s", event.Data)
			}
		case "research/tool_result":
			if !strings.Contains(payload, `"tool":"filesystem_list"`) || !strings.Contains(payload, `"result"`) || !strings.Contains(payload, `"durationMs"`) {
				t.Fatalf("research tool result was incomplete: %s", event.Data)
			}
		}
	}
	for eventType, minimum := range wantedResearch {
		if seenResearch[eventType] < minimum {
			t.Fatalf("trajectory captured %d %s events, want at least %d: %#v", seenResearch[eventType], eventType, minimum, seenResearch)
		}
	}

	parentRequests, researchRequests := streamer.requests()
	if len(parentRequests) != 6 || len(researchRequests) != 3 {
		t.Fatalf("unexpected request counts: parent=%d research=%d", len(parentRequests), len(researchRequests))
	}
	for _, request := range researchRequests {
		for _, schema := range request.Tools {
			if tools.IsResearchAgentToolName(schema.Function.Name) {
				t.Fatalf("child schema exposed recursive orchestration tool %s", schema.Function.Name)
			}
			if !tools.IsResearchWorkerToolName(schema.Function.Name) {
				t.Fatalf("child schema exposed non-research tool %s", schema.Function.Name)
			}
		}
	}
	followUpContext := researchRequests[2].Messages
	if !messagesContain(followUpContext, "The workspace listing was inspected") || !messagesContain(followUpContext, "Confirm the original evidence") {
		t.Fatalf("research follow-up did not retain private context: %#v", followUpContext)
	}
}

func messagesContain(messages []llm.Message, text string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

func TestResearchJobTerminalEventIsRecordedOnce(t *testing.T) {
	startedAt := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(325 * time.Millisecond)
	run := &chatResearchRun{}
	agent := &chatResearchAgentRun{id: "agent-1", name: "Docs"}
	job := &chatResearchJob{
		id: "agent-1-job-2", number: 2, kind: "follow_up", prompt: "Verify the exact source",
		queuedAt: startedAt.Add(-time.Second), startedAt: startedAt,
	}

	run.mu.Lock()
	first := run.markJobTerminalLocked(agent, job, "completed", "Verified.", "", completedAt)
	second := run.markJobTerminalLocked(agent, job, "canceled", "", "late cancellation", completedAt.Add(time.Second))
	run.mu.Unlock()

	if first == nil || second != nil {
		t.Fatalf("terminal event was not deduplicated: first=%#v second=%#v", first, second)
	}
	if first["prompt"] != job.prompt || first["jobNumber"] != 2 || first["durationMs"] != int64(325) {
		t.Fatalf("terminal event lost job identity or timing: %#v", first)
	}
}

func TestDisabledResearchToolsAreHiddenAndFailHallucinatedCalls(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "research-disabled")
	server.settings.ResearchAgentConcurrency = 0
	streamer := &disabledResearchStreamer{}
	server.llm = streamer

	url := startWebSocketTestServer(t, server)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "disabled-research-request",
		"message": "Try research.",
	}); err != nil {
		t.Fatal(err)
	}
	result := readUntilSessionEvent(t, conn, "tool_result")
	if result["success"] != false || !strings.Contains(result["content"].(string), "research_agents_disabled") {
		t.Fatalf("hallucinated research call did not fail safely: %#v", result)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat did not recover from disabled research call: %#v", finished)
	}
	requests := streamer.captured()
	if len(requests) != 2 {
		t.Fatalf("unexpected parent request count: %d", len(requests))
	}
	for _, schema := range requests[0].Tools {
		if tools.IsResearchAgentToolName(schema.Function.Name) {
			t.Fatalf("disabled request exposed %s", schema.Function.Name)
		}
	}
}
