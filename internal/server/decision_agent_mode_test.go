// Tests for decision-model agent-mode selection (Phase 3): the decision model
// overrides the request's agent mode per prompt, explicit goal starts are
// never overridden, the decision model can never select Goal mode, and the
// feature fails open when disabled or the endpoint errors. The rerun/edit
// path applies the same override.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/decision"
)

// decisionAgentModeHarness wraps a fake decision-model endpoint whose choice
// answer is mutable between requests, plus a chat server with the
// decision-model agent-mode feature enabled.
type decisionAgentModeHarness struct {
	server   *Server
	streamer *temperatureCapturingStreamer
	decision *httptest.Server

	mu     sync.Mutex
	choice string
	calls  int
}

func (h *decisionAgentModeHarness) setChoice(choice string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.choice = choice
}

func (h *decisionAgentModeHarness) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func newDecisionAgentModeHarness(t *testing.T, initialChoice string) *decisionAgentModeHarness {
	t.Helper()
	h := &decisionAgentModeHarness{choice: initialChoice}
	decisionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.calls++
		choice := h.choice
		h.mu.Unlock()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode decision request: %v", err)
		}
		if body["state"] == "" {
			t.Error("decision request missing state")
		}
		questions, _ := body["questions"].(map[string]any)
		agentMode, _ := questions["agentMode"].(map[string]any)
		if agentMode["type"] != "choice" {
			t.Errorf("decision question type = %v, want choice", agentMode["type"])
		}
		criteria, _ := agentMode["criteria"].(map[string]any)
		if _, ok := criteria["plan"]; !ok {
			t.Error("decision criteria missing plan mode")
		}
		if _, ok := criteria["general"]; !ok {
			t.Error("decision criteria missing general mode")
		}
		if _, ok := criteria["goal"]; ok {
			t.Error("decision criteria must not offer goal mode")
		}
		w.Header().Set("Content-Type", "application/json")
		probabilities := map[string]float64{"general": 0.05, "plan": 0.05, "pair-programming": 0.05, "goal": 0.05}
		probabilities[choice] = 0.9
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "clef-flash",
			"answers": map[string]any{
				"agentMode": map[string]any{
					"type":          "choice",
					"choice":        choice,
					"probabilities": probabilities,
					"confidence":    0.9,
				},
			},
			"usage": map[string]int{"input_tokens": 42, "output_tokens": 0},
		})
	}))
	t.Cleanup(decisionServer.Close)

	s, _ := newTestServer(t)
	s.settings.DecisionModelAgentMode = true
	s.llmSettings.DecisionModelAgentMode = true
	s.decisionModel = decision.New(decisionServer.URL, "")
	fake := &temperatureCapturingStreamer{}
	s.llm = fake
	h.server = s
	h.streamer = fake
	h.decision = decisionServer
	return h
}

func TestDecisionModelAgentModeOverridesUserMode(t *testing.T) {
	h := newDecisionAgentModeHarness(t, "plan")
	workspace := createChatWorkspace(t, h.server, "decision-agent-mode")

	started := sendChatAndWaitForTurn(t, h.server, workspace.ID, nil)

	if got := started["agentModeId"]; got != "plan" {
		t.Errorf("turn_started agentModeId = %v, want plan", got)
	}
	selected, ok := started["decisionAgentMode"].(map[string]any)
	if !ok {
		t.Fatalf("turn_started missing decisionAgentMode: %v", started)
	}
	if selected["name"] != "Plan" {
		t.Errorf("decisionAgentMode name = %v, want Plan", selected["name"])
	}
	if confidence, _ := selected["confidence"].(float64); confidence != 0.9 {
		t.Errorf("decisionAgentMode confidence = %v, want 0.9", selected["confidence"])
	}
	requests := h.streamer.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	if !strings.Contains(requests[0].Messages[0].Content, "Planning only") {
		t.Errorf("request system prompt is not the plan mode prompt: %.120s", requests[0].Messages[0].Content)
	}
}

func TestDecisionModelAgentModeExplicitGoalStartNotOverridden(t *testing.T) {
	h := newDecisionAgentModeHarness(t, "plan")
	workspace := createChatWorkspace(t, h.server, "decision-agent-mode-goal")

	started := sendChatAndWaitForTurn(t, h.server, workspace.ID, map[string]any{"agentModeId": "goal"})

	if got := started["agentModeId"]; got != "goal" {
		t.Errorf("turn_started agentModeId = %v, want goal to win", got)
	}
	if _, present := started["decisionAgentMode"]; present {
		t.Errorf("turn_started should not carry decisionAgentMode for explicit goal starts: %v", started)
	}
	if calls := h.callCount(); calls != 0 {
		t.Errorf("decision model called %d times for explicit goal start, want 0", calls)
	}
}

func TestDecisionModelAgentModeCannotSelectGoal(t *testing.T) {
	h := newDecisionAgentModeHarness(t, "goal")
	workspace := createChatWorkspace(t, h.server, "decision-agent-mode-no-goal")

	started := sendChatAndWaitForTurn(t, h.server, workspace.ID, nil)

	if got := started["agentModeId"]; got != "general" {
		t.Errorf("turn_started agentModeId = %v, want general (goal choice ignored)", got)
	}
	if _, present := started["decisionAgentMode"]; present {
		t.Errorf("turn_started should not carry decisionAgentMode when the choice is unusable: %v", started)
	}
}

func TestDecisionModelAgentModeFailureFailsOpen(t *testing.T) {
	s, _ := newTestServer(t)
	s.llmSettings.DecisionModelAgentMode = true
	s.decisionModel = decision.New("http://127.0.0.1:1", "") // unreachable: connection refused
	fake := &temperatureCapturingStreamer{}
	s.llm = fake
	workspace := createChatWorkspace(t, s, "decision-agent-mode-fail")

	started := sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	if got := started["agentModeId"]; got != "general" {
		t.Errorf("turn_started agentModeId = %v, want general on failure", got)
	}
	if _, present := started["decisionAgentMode"]; present {
		t.Errorf("turn_started should not carry decisionAgentMode on failure: %v", started)
	}
}

func TestDecisionModelAgentModeDisabledKeepsResolvedMode(t *testing.T) {
	s, _ := newTestServer(t)
	s.llmSettings.DecisionModelAgentMode = false
	s.decisionModel = decision.New("http://127.0.0.1:1", "")
	fake := &temperatureCapturingStreamer{}
	s.llm = fake
	workspace := createChatWorkspace(t, s, "decision-agent-mode-disabled")

	started := sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	if got := started["agentModeId"]; got != "general" {
		t.Errorf("turn_started agentModeId = %v, want general when disabled", got)
	}
	if _, present := started["decisionAgentMode"]; present {
		t.Errorf("turn_started should not carry decisionAgentMode when disabled: %v", started)
	}
}

func TestDecisionModelAgentModeAppliesOnRerun(t *testing.T) {
	h := newDecisionAgentModeHarness(t, "plan")
	workspace := createChatWorkspace(t, h.server, "decision-agent-mode-rerun")

	url := startWebSocketTestServer(t, h.server)
	conn := dialSharedClient(t, url)
	if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	chatID := snapshot["activeChatId"].(string)

	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID,
		"requestId": "agent-mode-rerun", "message": "Plan a refactor of the auth module",
	}); err != nil {
		t.Fatal(err)
	}
	started := readSessionEventForChat(t, conn, chatID, "turn_started")
	if got := started["agentModeId"]; got != "plan" {
		t.Fatalf("turn_started agentModeId = %v, want plan", got)
	}
	turnID, _ := started["turnId"].(string)
	if turnID == "" {
		t.Fatal("turn_started missing turnId")
	}
	readSessionEventForChat(t, conn, chatID, "turn_finished")

	// The recorded turn now carries the decision-selected plan mode; the
	// rerun re-asks and the decision model picks general this time.
	h.setChoice("general")
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_message_rerun", "workspaceId": workspace.ID,
		"chatId": chatID, "turnId": turnID,
	}); err != nil {
		t.Fatal(err)
	}
	rerunStarted := readSessionEventForChat(t, conn, chatID, "turn_rerun_started")
	if got := rerunStarted["agentModeId"]; got != "general" {
		t.Errorf("turn_rerun_started agentModeId = %v, want general", got)
	}
	selected, ok := rerunStarted["decisionAgentMode"].(map[string]any)
	if !ok {
		t.Fatalf("turn_rerun_started missing decisionAgentMode: %v", rerunStarted)
	}
	if selected["name"] != "General" {
		t.Errorf("decisionAgentMode name = %v, want General", selected["name"])
	}
}
