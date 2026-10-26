package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

// workerFlowStreamer drives a full worker subagent turn: the parent spawns a
// worker, the worker inspects the workspace and reports, the parent waits and
// finalizes. Parent and child requests are distinguished by the child system
// message name; worker children additionally carry the worker system prompt.
type workerFlowStreamer struct {
	mu              sync.Mutex
	parentRequests  []llm.ChatRequest
	workerRequests  []llm.ChatRequest
	workspaceLabel  string
	childToolNames  []string
	childToolSchema []llm.Tool
}

func (f *workerFlowStreamer) isWorker(request llm.ChatRequest) bool {
	return len(request.Messages) > 0 && request.Messages[0].Name == "echo-research-agent" &&
		strings.Contains(request.Messages[0].Content, "implementation worker subagent")
}

func (f *workerFlowStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	worker := f.isWorker(request)
	f.mu.Lock()
	if worker {
		f.workerRequests = append(f.workerRequests, request)
	} else {
		f.parentRequests = append(f.parentRequests, request)
	}
	var callCount int
	if worker {
		callCount = len(f.workerRequests)
		if callCount == 1 {
			f.childToolSchema = append([]llm.Tool(nil), request.Tools...)
			for _, tool := range request.Tools {
				f.childToolNames = append(f.childToolNames, tool.Function.Name)
			}
		}
	} else {
		callCount = len(f.parentRequests)
	}
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 6)
	if worker {
		switch callCount {
		case 1:
			events <- researchToolCall("worker-list", "filesystem_list", `{"path":"`+f.workspaceLabel+`"}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		default:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "Worker report: the workspace was inspected; no changes were required."}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		}
	} else {
		switch callCount {
		case 1:
			events <- researchToolCall("spawn-worker", tools.WorkerAgentsSpawnToolName, `{"workers":[{"name":"Fixture builder","task":"Inspect the workspace listing and report what you find."}]}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		case 2:
			events <- researchToolCall("wait-worker", tools.WorkerAgentsWaitToolName, `{"waitFor":"all","timeoutSeconds":10}`)
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
		default:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "Final answer using the worker report."}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		}
	}
	close(events)
	return &llm.Stream{ID: "worker-flow", Events: events}
}

func TestWorkerAgentsRunPrivatelyAndReport(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "worker-flow")
	streamer := &workerFlowStreamer{workspaceLabel: normalizeWorkspaceFolderLabel(filepath.Base(workspace.MainPath))}
	server.llm = streamer

	url := startWebSocketTestServer(t, server)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "worker-request",
		"message": "Delegate the workspace inspection to a worker.",
	}); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("worker chat failed: %#v", finished)
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
	if !strings.Contains(visible.String(), "Final answer using the worker report.") {
		t.Fatalf("final synthesis missing: %q", visible.String())
	}
	if len(turn.ResearchAgents) != 0 {
		t.Fatalf("transient worker status was persisted: %#v", turn.ResearchAgents)
	}

	// The worker child schema must expose implementation tools but never
	// orchestration or mode-mutation tools.
	childNames := make(map[string]bool)
	for _, name := range streamer.childToolNames {
		childNames[name] = true
	}
	if !childNames["filesystem_list"] {
		t.Fatal("worker child schema omitted filesystem_list")
	}
	for _, forbidden := range []string{"create_agent_mode", tools.WorkerAgentsSpawnToolName, tools.ResearchAgentsSpawnToolName, "ask_user_questions"} {
		if childNames[forbidden] {
			t.Fatalf("worker child schema exposed forbidden tool %s", forbidden)
		}
	}

	parent, err := server.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	tab, _, err := parent.resolveSurfaceTab(transcript.ChatID, chatSurfaceMain)
	if err != nil {
		t.Fatal(err)
	}
	page, err := tab.trajectory.Page(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	createdWorker, deliveredReport := false, false
	for _, event := range page.Events {
		payload := string(event.Data)
		if event.Type == "research/agent_created" && strings.Contains(payload, `"kind":"worker"`) && strings.Contains(payload, "Fixture builder") {
			createdWorker = true
		}
		if event.Type == "research/report_delivered" && strings.Contains(payload, "no changes were required") {
			deliveredReport = true
		}
	}
	if !createdWorker {
		t.Fatalf("worker creation was not attributed in the trajectory")
	}
	if !deliveredReport {
		t.Fatalf("worker report was not delivered to the parent")
	}
}

func TestWorkerModeGating(t *testing.T) {
	if !modeAllowsWorkers(agentmodes.Defaults()[0]) {
		t.Fatal("General mode must allow worker subagents")
	}
	if modeAllowsWorkers(agentmodes.Defaults()[1]) {
		t.Fatal("Plan mode must not allow worker subagents")
	}

	generalScopes := workerToolScopes(agentmodes.Defaults()[0])
	if !generalScopes.HasTool("filesystem_edit_text") || !generalScopes.HasTool("shell_command") {
		t.Fatal("General-mode worker scopes omitted implementation tools")
	}
	if generalScopes.HasTool("create_agent_mode") {
		t.Fatal("General-mode worker scopes exposed create_agent_mode")
	}
	planScopes := workerToolScopes(agentmodes.Defaults()[1])
	if planScopes.HasTool("filesystem_edit_text") {
		t.Fatal("Plan-mode worker scopes must be deny-all")
	}
}

func TestWorkerSpawnIsBlockedForWorkerChildren(t *testing.T) {
	// A worker child tool context never receives a coordinator, so a
	// hallucinated recursive spawn fails safely.
	result := tools.Execute(tools.ExecutionContext{}, tools.WorkerAgentsSpawnToolName, json.RawMessage(`{"workers":[{"task":"nested"}]}`))
	if result.Success || result.Error == nil || result.Error.Code != "worker_agents_unavailable" {
		t.Fatalf("recursive worker spawn did not fail safely: %#v", result)
	}
}
