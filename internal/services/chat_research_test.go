package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func TestChatResearchAgentsRunConcurrentlyAndKeepPrivateHistoryOutOfParent(t *testing.T) {
	var parentRequests atomic.Int32
	var activeResearch atomic.Int32
	var peakResearch atomic.Int32
	service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if requestHasSystemText(request, "focused, read-only research sub-agent") {
			current := activeResearch.Add(1)
			for {
				peak := peakResearch.Load()
				if current <= peak || peakResearch.CompareAndSwap(peak, current) {
					break
				}
			}
			defer activeResearch.Add(-1)
			if requestHasRole(request, llm.RoleTool) {
				time.Sleep(40 * time.Millisecond)
				writeSSE(t, w,
					`{"choices":[{"index":0,"delta":{"content":"Finding with evidence from the workspace."}}]}`,
					`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				)
				return
			}
			reasoning := "Inspecting code structure before choosing evidence."
			if requestHasMessageText(request, "test structure") {
				reasoning = "Inspecting test structure before choosing evidence."
			}
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, reasoning),
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"inspect","type":"function","function":{"name":"filesystem_list","arguments":%q}}]}}]}`, `{"path":"."}`),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
			return
		}

		switch parentRequests.Add(1) {
		case 1:
			args := `{"agents":[{"name":"Code","task":"Inspect the code structure."},{"name":"Tests","task":"Inspect the test structure."}]}`
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"spawn","type":"function","function":{"name":"research_agents_spawn","arguments":%q}}]}}]}`, args),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			args := `{"waitFor":"all","timeoutSeconds":2}`
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"wait","type":"function","function":{"name":"research_agents_wait","arguments":%q}}]}}]}`, args),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 3:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Combined synthesis."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Errorf("unexpected parent request %d", parentRequests.Load())
		}
	}))

	if _, err := service.SendChatMessage(workspaceID, "Coordinate independent code and test investigators."); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if peakResearch.Load() < 2 {
		t.Fatalf("expected overlapping research agents, peak concurrency was %d", peakResearch.Load())
	}
	assistant := session.Messages[1]
	if assistant.Content != "Combined synthesis." || len(assistant.ResearchAgents) != 0 {
		t.Fatalf("unexpected completed assistant message: %#v", assistant)
	}
	attributed := 0
	for _, activity := range assistant.ToolCalls {
		if activity.AgentID != "" {
			attributed++
			if activity.AgentName == "" || activity.Name != "filesystem_list" {
				t.Fatalf("unexpected attributed activity: %#v", activity)
			}
		}
	}
	if attributed != 2 {
		t.Fatalf("expected one visible child tool call per agent, got %#v", assistant.ToolCalls)
	}
	if len(assistant.ResearchReasoning) != 2 {
		t.Fatalf("expected reasoning from both research agents, got %#v", assistant.ResearchReasoning)
	}
	for _, reasoning := range assistant.ResearchReasoning {
		if reasoning.AgentID == "" || reasoning.AgentName == "" || !strings.Contains(reasoning.Reasoning, "Inspecting") {
			t.Fatalf("unexpected attributed research reasoning: %#v", reasoning)
		}
	}

	service.chatMu.Lock()
	history := cloneLLMMessages(service.chatSessions[workspaceID].History)
	service.chatMu.Unlock()
	for _, message := range history {
		if strings.Contains(message.Content, "Inspecting code structure") || strings.Contains(message.Content, "Inspecting test structure") {
			t.Fatalf("child reasoning leaked into parent LLM history: %#v", history)
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == "filesystem_list" {
				t.Fatalf("child tool call leaked into parent LLM history: %#v", history)
			}
		}
	}
}

func TestGeneralModeScoutsThenFansOutBroadWorkspaceResearch(t *testing.T) {
	var parentRequests atomic.Int32
	var researchRequests atomic.Int32
	var activeSpecialists atomic.Int32
	var peakSpecialists atomic.Int32
	service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if requestHasSystemText(request, "focused, read-only research sub-agent") {
			researchRequests.Add(1)
			content := "Specialist evidence with exact paths."
			if requestHasMessageText(request, "Scout the overall workspace layout") {
				content = "Major aspects: layout and rendering; editing input and tools; persistence and generated assets."
			} else {
				current := activeSpecialists.Add(1)
				for {
					peak := peakSpecialists.Load()
					if current <= peak || peakSpecialists.CompareAndSwap(peak, current) {
						break
					}
				}
				time.Sleep(40 * time.Millisecond)
				activeSpecialists.Add(-1)
			}
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, content),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
			return
		}

		switch parentRequests.Add(1) {
		case 1:
			if !requestHasToolCall(request, "research_agents_spawn") {
				t.Errorf("general-mode parent request did not receive the automatic spawn acknowledgement: %#v", request.Messages)
			}
			writeResearchToolCall(t, w, "wait", "research_agents_wait", `{"waitFor":"all","timeoutSeconds":2}`)
		case 2:
			if !requestHasMessageText(request, "between 2 and 7 focused specialist agents") || !requestHasMessageText(request, "Major aspects:") {
				t.Errorf("parent did not receive the scout-to-fan-out handoff: %#v", request.Messages)
			}
			writeResearchToolCall(t, w, "fanout", "research_agents_spawn", `{"agents":[{"name":"Layout","task":"Research layout and rendering."},{"name":"Input","task":"Research editing input and tools."},{"name":"Persistence","task":"Research persistence and generated assets."}]}`)
		case 3:
			writeResearchToolCall(t, w, "wait-specialists", "research_agents_wait", `{"waitFor":"all","timeoutSeconds":2}`)
		case 4:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Terrain editor synthesis."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Errorf("unexpected parent request %d", parentRequests.Load())
		}
	}))

	if _, err := service.SendChatMessage(workspaceID, "Look through src/game/flash_war and explain the terrain editor."); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if researchRequests.Load() != 4 {
		t.Fatalf("expected one scout and three specialists, got %d research requests", researchRequests.Load())
	}
	if peakSpecialists.Load() < 3 {
		t.Fatalf("expected three specialist agents to overlap, peak concurrency was %d", peakSpecialists.Load())
	}
	if session.Messages[1].Content != "Terrain editor synthesis." {
		t.Fatalf("unexpected final message: %#v", session.Messages[1])
	}
	spawnAudits := 0
	for _, activity := range session.Messages[1].ToolCalls {
		if activity.Name == "research_agents_spawn" && activity.Status == "complete" {
			spawnAudits++
		}
	}
	if spawnAudits != 2 {
		t.Fatalf("expected scout and specialist spawn audits, got %#v", session.Messages[1].ToolCalls)
	}
}

func TestBroadResearchBootstrapHonorsExplicitOptOut(t *testing.T) {
	message := llm.Message{Role: llm.RoleUser, Content: "Look through src/game/flash_war, but do not spawn agents."}
	if shouldBootstrapResearch(message, AgentModeIDGeneral) {
		t.Fatal("expected explicit no-agent request to disable automatic delegation")
	}
	if shouldBootstrapResearch(llm.Message{Role: llm.RoleUser, Content: "Look through src/game/flash_war."}, "custom") {
		t.Fatal("expected automatic delegation to remain scoped to General and Plan modes")
	}
	mediaReview := llm.Message{Role: llm.RoleUser, Content: "Review @workspace/ui.png\n\nAttached media:\n- Image 1: ui.png (workspace/ui.png), image/png, 1 KB"}
	if shouldBootstrapResearch(mediaReview, AgentModeIDGeneral) {
		t.Fatal("expected a narrow attached-image review not to be treated as directory research")
	}
}

func TestResearchConcurrencyZeroUsesDirectChatInGeneralAndPlanModes(t *testing.T) {
	for _, modeID := range []string{AgentModeIDGeneral, AgentModeIDPlan} {
		t.Run(modeID, func(t *testing.T) {
			var parentRequests atomic.Int32
			var researchRequests atomic.Int32
			researchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				researchRequests.Add(1)
				writeSSE(t, w,
					`{"choices":[{"index":0,"delta":{"content":"Unexpected research response."}}]}`,
					`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				)
			}))
			t.Cleanup(researchServer.Close)

			service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request llm.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode parent request: %v", err)
					return
				}
				if requestHasSystemText(request, researchOrchestratorSystemGuidance) {
					t.Error("disabled research guidance leaked into the parent system prompt")
				}
				if requestHasToolCall(request, "research_agents_spawn") {
					t.Errorf("disabled research bootstrap leaked into parent history: %#v", request.Messages)
				}
				names := chatRequestToolNames(request)
				for _, name := range []string{"research_agents_spawn", "research_agent_send", "research_agents_wait", "research_agents_cancel"} {
					if names[name] {
						t.Errorf("disabled research tool %s was exposed in %s mode", name, modeID)
					}
				}
				if !names["filesystem_list"] {
					t.Errorf("direct filesystem tool was unavailable in %s mode: %#v", modeID, names)
				}
				if modeID == AgentModeIDPlan && !names["workspace_task_create"] {
					t.Errorf("direct Plan mode lost workspace_task_create: %#v", names)
				}

				switch parentRequests.Add(1) {
				case 1:
					writeResearchToolCall(t, w, "direct-list", "filesystem_list", `{"path":"."}`)
				case 2:
					if !requestHasRole(request, llm.RoleTool) {
						t.Errorf("direct tool result was not returned to the parent: %#v", request.Messages)
					}
					writeSSE(t, w,
						`{"choices":[{"index":0,"delta":{"content":"Direct inspection complete."}}]}`,
						`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
					)
				default:
					t.Errorf("unexpected parent request %d", parentRequests.Load())
				}
			}))

			settings := service.LoadState().Settings
			researchEndpoint := settings.Endpoints[0]
			researchEndpoint.ID = "research-only"
			researchEndpoint.Name = "Research only"
			researchEndpoint.Endpoint = researchServer.URL + "/v1"
			researchEndpoint.Model = "research-model"
			settings.Endpoints = append(settings.Endpoints, researchEndpoint)
			settings.EndpointSelection.Research = researchEndpoint.ID
			settings.ResearchAgentConcurrency = 0
			if _, err := service.SaveSettings(settings); err != nil {
				t.Fatalf("disable research agents: %v", err)
			}

			if _, err := service.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{
				Content:     "Look through this entire workspace and explain all major subsystems.",
				AgentModeID: modeID,
			}); err != nil {
				t.Fatalf("send %s chat: %v", modeID, err)
			}
			session := waitForChatIdle(t, service, workspaceID)
			if parentRequests.Load() != 2 || researchRequests.Load() != 0 {
				t.Fatalf("expected two direct parent requests and no research requests, parent=%d research=%d", parentRequests.Load(), researchRequests.Load())
			}
			assistant := session.Messages[1]
			if assistant.Content != "Direct inspection complete." || len(assistant.ResearchAgents) != 0 {
				t.Fatalf("unexpected direct assistant result: %#v", assistant)
			}
			if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != "filesystem_list" {
				t.Fatalf("expected one direct filesystem audit, got %#v", assistant.ToolCalls)
			}
			service.researchMu.Lock()
			activeRuns := len(service.researchRuns)
			service.researchMu.Unlock()
			if activeRuns != 0 {
				t.Fatalf("expected no registered research runs, got %d", activeRuns)
			}
		})
	}
}

func TestResearchConcurrencyZeroRejectsHallucinatedAgentToolCall(t *testing.T) {
	var parentRequests atomic.Int32
	service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch parentRequests.Add(1) {
		case 1:
			writeResearchToolCall(t, w, "hallucinated-spawn", "research_agents_spawn", `{"agents":[{"task":"Should not run"}]}`)
		case 2:
			if !requestHasMessageText(request, "research_agents_disabled") {
				t.Errorf("parent did not receive the disabled-tool error: %#v", request.Messages)
			}
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Continued without research agents."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Errorf("unexpected parent request %d", parentRequests.Load())
		}
	}))
	settings := service.LoadState().Settings
	settings.ResearchAgentConcurrency = 0
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("disable research agents: %v", err)
	}

	if _, err := service.SendChatMessage(workspaceID, "Answer directly."); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	assistant := session.Messages[1]
	if assistant.Content != "Continued without research agents." || len(assistant.ResearchAgents) != 0 {
		t.Fatalf("unexpected assistant result: %#v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Status != "error" || !strings.Contains(assistant.ToolCalls[0].Result, "research_agents_disabled") {
		t.Fatalf("expected a bounded disabled-tool audit, got %#v", assistant.ToolCalls)
	}
}

func TestChatResearchAgentFollowUpRetainsPrivateHistory(t *testing.T) {
	var parentRequests atomic.Int32
	var researchRequests atomic.Int32
	service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if requestHasSystemText(request, "focused, read-only research sub-agent") {
			researchRequests.Add(1)
			content := "Initial report."
			if requestHasMessageText(request, "Clarify the evidence") {
				if !requestHasMessageText(request, "Initial report.") {
					t.Errorf("follow-up request omitted the private prior report: %#v", request.Messages)
				}
				content = "Clarified report."
			}
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, content),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
			return
		}

		switch parentRequests.Add(1) {
		case 1:
			writeResearchToolCall(t, w, "spawn", "research_agents_spawn", `{"agents":[{"name":"Docs","task":"Find the evidence."}]}`)
		case 2:
			writeResearchToolCall(t, w, "wait1", "research_agents_wait", `{"waitFor":"all","timeoutSeconds":2}`)
		case 3:
			writeResearchToolCall(t, w, "send", "research_agent_send", `{"agentId":"agent-1","message":"Clarify the evidence."}`)
		case 4:
			writeResearchToolCall(t, w, "wait2", "research_agents_wait", `{"waitFor":"all","timeoutSeconds":2}`)
		case 5:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Final answer from the clarification."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Errorf("unexpected parent request %d", parentRequests.Load())
		}
	}))

	if _, err := service.SendChatMessage(workspaceID, "Delegate this research and clarify it."); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if researchRequests.Load() != 2 || session.Messages[1].Content != "Final answer from the clarification." {
		t.Fatalf("unexpected follow-up result: requests=%d message=%#v", researchRequests.Load(), session.Messages[1])
	}
}

func TestChatResearchAgentsRespectConcurrencyAndQueueExcessWork(t *testing.T) {
	var parentRequests atomic.Int32
	var researchRequests atomic.Int32
	var activeResearch atomic.Int32
	var peakResearch atomic.Int32
	service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if requestHasSystemText(request, "focused, read-only research sub-agent") {
			researchRequests.Add(1)
			current := activeResearch.Add(1)
			for {
				peak := peakResearch.Load()
				if current <= peak || peakResearch.CompareAndSwap(peak, current) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			activeResearch.Add(-1)
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Bounded report."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
			return
		}

		switch parentRequests.Add(1) {
		case 1:
			writeResearchToolCall(t, w, "spawn", "research_agents_spawn", `{"agents":[{"task":"One"},{"task":"Two"},{"task":"Three"},{"task":"Four"},{"task":"Five"}]}`)
		case 2:
			writeResearchToolCall(t, w, "wait", "research_agents_wait", `{"waitFor":"all","timeoutSeconds":3}`)
		case 3:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Queued synthesis."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Errorf("unexpected parent request %d", parentRequests.Load())
		}
	}))
	settings := service.LoadState().Settings
	settings.ResearchAgentConcurrency = 2
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	if _, err := service.SendChatMessage(workspaceID, "Delegate five independent branches."); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if researchRequests.Load() != 5 || peakResearch.Load() != 2 {
		t.Fatalf("expected five jobs with peak concurrency two, requests=%d peak=%d", researchRequests.Load(), peakResearch.Load())
	}
	if session.Messages[1].Content != "Queued synthesis." {
		t.Fatalf("unexpected final message: %#v", session.Messages[1])
	}
}

func TestStoppingChatCancelsResearchAndClearsIndicators(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var parentRequests atomic.Int32
	service, workspaceID := newChatTestService(t, t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if requestHasSystemText(request, "focused, read-only research sub-agent") {
			close(started)
			<-r.Context().Done()
			close(canceled)
			return
		}
		if parentRequests.Add(1) == 1 {
			writeResearchToolCall(t, w, "spawn", "research_agents_spawn", `{"agents":[{"name":"Slow","task":"Wait for evidence."}]}`)
			return
		}
		writeResearchToolCall(t, w, "wait", "research_agents_wait", `{"waitFor":"all","timeoutSeconds":120}`)
	}))

	if _, err := service.SendChatMessage(workspaceID, "Start slow research."); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("research request did not start")
	}
	if _, err := service.StopChatStream(workspaceID); err != nil {
		t.Fatalf("stop chat: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("research request did not observe cancellation")
	}
	session := waitForChatIdle(t, service, workspaceID)
	if len(session.Messages[1].ResearchAgents) != 0 || session.Messages[1].Status != "canceled" {
		t.Fatalf("expected canceled chat without active agents, got %#v", session.Messages[1])
	}
	service.researchMu.Lock()
	activeRuns := len(service.researchRuns)
	service.researchMu.Unlock()
	if activeRuns != 0 {
		t.Fatalf("expected no retained research runs, got %d", activeRuns)
	}
}

func TestResearchPermissionsIntersectParentModeAndReadOnlyAllowlist(t *testing.T) {
	mode := AgentMode{Permissions: map[string]tools.ToolPermission{
		"filesystem_read_text": {Name: "filesystem_read_text", Paths: []string{"src/**"}},
		"filesystem_edit_text": {Name: "filesystem_edit_text", Paths: []string{"src/**"}},
	}}
	scopes := researchToolScopes(mode)
	if !scopes.Allowed("filesystem_read_text", "src/main.go") || scopes.Allowed("filesystem_read_text", "docs/readme.md") {
		t.Fatal("expected parent path restrictions to apply to child reads")
	}
	for _, name := range []string{"filesystem_edit_text", "shell_command", "research_agents_spawn"} {
		if scopes.HasTool(name) {
			t.Fatalf("expected research scope to reject %s", name)
		}
	}

	denyAll := researchToolScopes(AgentMode{Permissions: map[string]tools.ToolPermission{
		"filesystem_edit_text": {Name: "filesystem_edit_text"},
	}})
	if denyAll.HasTool("filesystem_read_text") || denyAll.HasTool("web_search") {
		t.Fatal("expected a parent mode without read permissions to deny all child tools")
	}
}

func TestPersistedChatSessionOmitsActiveResearchRuntimeState(t *testing.T) {
	session := &chatSessionState{WorkspaceID: "workspace", Messages: []ChatMessage{{
		ID:             "assistant",
		Role:           llm.RoleAssistant,
		Status:         "streaming",
		ResearchAgents: []ChatResearchAgent{{ID: "agent-1", Name: "Docs", Status: "running"}},
		ResearchReasoning: []ChatResearchReasoning{{
			AgentID: "agent-1", AgentName: "Docs", Reasoning: "Checking documentation.", Replace: true,
		}},
		ToolCalls: []ChatToolActivity{{ID: "agent-1:call", Name: "web_search", AgentID: "agent-1", AgentName: "Docs", Status: "complete"}},
	}}}
	persisted := persistedChatSessionFrom(session)
	if len(persisted.Messages[0].ResearchAgents) != 0 {
		t.Fatalf("active research state leaked into persistence: %#v", persisted.Messages[0])
	}
	if len(persisted.Messages[0].ToolCalls) != 1 || persisted.Messages[0].ToolCalls[0].AgentID != "agent-1" {
		t.Fatalf("bounded attributed audit was not persisted: %#v", persisted.Messages[0])
	}
	if len(persisted.Messages[0].ResearchReasoning) != 1 || persisted.Messages[0].ResearchReasoning[0].Reasoning != "Checking documentation." || persisted.Messages[0].ResearchReasoning[0].Replace {
		t.Fatalf("bounded research reasoning was not persisted safely: %#v", persisted.Messages[0])
	}
}

func TestResearchAgentReasoningIsUTF8SafeAndBounded(t *testing.T) {
	input := strings.Repeat("\U0001F642 evidence ", maxResearchAgentReasoning/8)
	bounded, truncated := appendBoundedResearchReasoning("", input)
	if !truncated || len(bounded) > maxResearchAgentReasoning {
		t.Fatalf("expected bounded reasoning, bytes=%d truncated=%v", len(bounded), truncated)
	}
	if !utf8.ValidString(bounded) || !strings.HasPrefix(bounded, researchReasoningTruncatedMarker) {
		t.Fatalf("expected valid UTF-8 reasoning with truncation marker, prefix=%q", bounded[:min(len(bounded), 64)])
	}
	next, truncated := appendBoundedResearchReasoning(bounded, "newest evidence")
	if !truncated || len(next) > maxResearchAgentReasoning || !utf8.ValidString(next) || !strings.HasSuffix(next, "newest evidence") {
		t.Fatalf("expected bounded reasoning to preserve the newest UTF-8 content")
	}
}

func TestResearchAgentReasoningEmitsAttributedRevisionedEvent(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	service.chatSessions["workspace"] = &chatSessionState{
		WorkspaceID: "workspace",
		Messages:    []ChatMessage{{ID: "assistant", Role: llm.RoleAssistant, Status: "streaming"}},
	}
	events, unsubscribe := SubscribeEvents(service, 8)
	defer unsubscribe()
	run := &chatResearchRun{service: service, workspace: Workspace{ID: "workspace"}, streamID: "stream", messageID: "assistant"}
	agent := &chatResearchAgentRun{id: "agent-1", name: "Docs"}

	service.appendResearchAgentReasoning(run, agent, "Checking sources.")
	select {
	case runtimeEvent := <-events:
		event, ok := runtimeEvent.Data.(ChatStreamEvent)
		if !ok || event.Type != "agent_reasoning" || event.Revision != 1 || event.ResearchReasoning == nil {
			t.Fatalf("unexpected research reasoning event: %#v", runtimeEvent)
		}
		if event.ResearchReasoning.AgentID != "agent-1" || event.ResearchReasoning.AgentName != "Docs" || event.ResearchReasoning.Reasoning != "Checking sources." {
			t.Fatalf("unexpected attributed reasoning delta: %#v", event.ResearchReasoning)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for research reasoning event")
	}

	service.chatMu.Lock()
	message := service.chatSessions["workspace"].Messages[0]
	service.chatMu.Unlock()
	if len(message.ResearchReasoning) != 1 || message.ResearchReasoning[0].Reasoning != "Checking sources." {
		t.Fatalf("research reasoning was not retained on the visible message: %#v", message)
	}
}

func TestResearchRunRejectsMoreThanEightAgents(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	run := &chatResearchRun{service: service, ctx: context.Background(), workspace: Workspace{ID: state.ActiveWorkspaceID}, agents: make(map[string]*chatResearchAgentRun)}
	specs := make([]tools.ResearchAgentSpec, maxResearchAgentsPerTurn+1)
	for i := range specs {
		specs[i].Task = fmt.Sprintf("Branch %d", i+1)
	}
	if _, err := run.SpawnResearchAgents(context.Background(), specs); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Fatal("expected more than eight agents to be rejected")
	}
}

func TestParentContextCompactionReservesResearchHandoffHeadroom(t *testing.T) {
	settings := llm.DefaultSettings()
	settings.ContextLength = 32000
	settings.MaxTokens = 4000
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: strings.Repeat("evidence ", 7000)},
	}
	run := &chatResearchRun{
		parentSettings: settings,
		agents: map[string]*chatResearchAgentRun{
			"agent-1": {id: "agent-1", workerActive: true, status: "running"},
		},
	}
	if contextNeedsCompaction(settings, messages, nil) {
		t.Fatal("test fixture unexpectedly reached the normal compaction threshold")
	}
	if !run.parentContextNeedsCompaction(messages, nil) {
		t.Fatal("expected research handoff headroom to trigger proactive compaction")
	}
}

func TestAggregateResearchHandoffsStayWithinParentBudget(t *testing.T) {
	settings := llm.DefaultSettings()
	settings.ContextLength = 8192
	settings.MaxTokens = 2048
	run := &chatResearchRun{parentSettings: settings, agents: make(map[string]*chatResearchAgentRun)}
	selected := make([]*chatResearchAgentRun, 0, maxResearchAgentsPerTurn)
	for i := 0; i < maxResearchAgentsPerTurn; i++ {
		id := fmt.Sprintf("agent-%d", i+1)
		agent := &chatResearchAgentRun{id: id, name: id, status: "completed", report: strings.Repeat("evidence ", 500), sequence: 1}
		run.agents[id] = agent
		run.order = append(run.order, id)
		selected = append(selected, agent)
	}
	result := run.waitResultLocked(selected, true)
	reportChars := 0
	for _, agent := range result.Agents {
		reportChars += len(agent.Report)
	}
	maxChars := run.aggregateReportTokenBudget() * contextCompactionCharsPerToken
	if reportChars > maxChars {
		t.Fatalf("aggregate reports exceeded budget: %d > %d", reportChars, maxChars)
	}
	if fallback := run.FallbackMarkdown(); len(fallback) > maxChars {
		t.Fatalf("fallback exceeded budget: %d > %d", len(fallback), maxChars)
	}
}

func writeResearchToolCall(t *testing.T, w http.ResponseWriter, id string, name string, arguments string) {
	t.Helper()
	writeSSE(t, w,
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, id, name, arguments),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
}

func requestHasSystemText(request llm.ChatRequest, text string) bool {
	for _, message := range request.Messages {
		if message.Role == llm.RoleSystem && strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

func requestHasRole(request llm.ChatRequest, role string) bool {
	for _, message := range request.Messages {
		if message.Role == role {
			return true
		}
	}
	return false
}

func requestHasMessageText(request llm.ChatRequest, text string) bool {
	for _, message := range request.Messages {
		if strings.Contains(message.Content, text) {
			return true
		}
	}
	return false
}

func requestHasToolCall(request llm.ChatRequest, name string) bool {
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			if call.Function.Name == name {
				return true
			}
		}
	}
	return false
}
