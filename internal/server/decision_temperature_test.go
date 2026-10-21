// Tests for decision-model temperature selection (Phase 2): the decision
// model overrides the endpoint temperature per prompt, an explicit
// per-message temperature wins, and the feature fails open when disabled or
// the endpoint errors.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/brent/echo/internal/decision"
	"github.com/brent/echo/internal/llm"
)

type temperatureCapturingStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (f *temperatureCapturingStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	events := make(chan llm.StreamEvent, 2)
	events <- llm.StreamEvent{Type: llm.EventToken, Content: "ok"}
	events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	close(events)
	return &llm.Stream{ID: "temperature", Events: events}
}

func (f *temperatureCapturingStreamer) captured() []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.ChatRequest(nil), f.requests...)
}

// newDecisionTemperatureHarness starts a fake decision-model endpoint that
// records requests and replies with the given score, plus a chat server with
// the decision-model temperature feature enabled.
func newDecisionTemperatureHarness(t *testing.T, score float64) (*Server, *temperatureCapturingStreamer, *httptest.Server) {
	t.Helper()
	var mu sync.Mutex
	decisionRequests := 0
	decisionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		decisionRequests++
		mu.Unlock()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode decision request: %v", err)
		}
		if body["state"] == "" {
			t.Error("decision request missing state")
		}
		questions, _ := body["questions"].(map[string]any)
		temperature, _ := questions["temperature"].(map[string]any)
		if temperature["type"] != "score" {
			t.Errorf("decision question type = %v, want score", temperature["type"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "clef-flash",
			"answers": map[string]any{
				"temperature": map[string]any{
					"type":  "score",
					"score": score,
					"legend": map[string]string{
						"0": "Deterministic", "1": "Focused", "2": "Balanced", "3": "Creative",
					},
					"probabilities": map[string]float64{"0": 0.0, "1": 0.0, "2": 0.0, "3": 1.0},
					"confidence":    0.9,
				},
			},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 0},
		})
	}))
	t.Cleanup(decisionServer.Close)

	s, _ := newTestServer(t)
	s.llmSettings.DecisionModelTemperature = true
	s.decisionModel = decision.New(decisionServer.URL, "")
	fake := &temperatureCapturingStreamer{}
	s.llm = fake
	return s, fake, decisionServer
}

func decisionRequestCount(t *testing.T, decisionServer *httptest.Server) int {
	t.Helper()
	// The counter lives in the handler closure; poll via a fresh request is
	// not possible, so tests assert on behavior instead. This helper only
	// exists to document intent.
	_ = decisionServer
	return -1
}

func sendChatAndWaitForTurn(t *testing.T, s *Server, workspaceID string, extra map[string]any) map[string]any {
	t.Helper()
	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspaceID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	chatID := snapshot["activeChatId"].(string)
	payload := map[string]any{
		"type": "chat_send", "workspaceId": workspaceID, "chatId": chatID,
		"requestId": "temperature-test", "message": "Write a haiku about refactoring",
	}
	for key, value := range extra {
		payload[key] = value
	}
	if err := conn.WriteJSON(payload); err != nil {
		t.Fatal(err)
	}
	// turn_started is emitted after the decision-model call resolves; the
	// first token proves the streamer has since seen the final request.
	started := readSessionEventForChat(t, conn, chatID, "turn_started")
	readSessionEventForChat(t, conn, chatID, "token")
	return started
}

func TestDecisionModelTemperatureOverridesEndpointTemperature(t *testing.T) {
	s, fake, _ := newDecisionTemperatureHarness(t, 3) // most creative band -> 1.0
	workspace := createChatWorkspace(t, s, "decision-temp")

	started := sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	if got := *requests[0].Temperature; got != 1.0 {
		t.Errorf("request temperature = %v, want 1.0 (score 3 mapped to ceiling)", got)
	}
	if started["decisionTemperature"] != 1.0 {
		t.Errorf("turn_started decisionTemperature = %v, want 1.0", started["decisionTemperature"])
	}
}

func TestDecisionModelTemperatureLowScoreMapsToFloor(t *testing.T) {
	s, fake, _ := newDecisionTemperatureHarness(t, 0) // deterministic band -> 0.1
	workspace := createChatWorkspace(t, s, "decision-temp-low")

	sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	if got := *requests[0].Temperature; got != 0.1 {
		t.Errorf("request temperature = %v, want 0.1 (score 0 mapped to floor)", got)
	}
}

func TestDecisionModelTemperatureExplicitUserTemperatureWins(t *testing.T) {
	s, fake, _ := newDecisionTemperatureHarness(t, 3)
	workspace := createChatWorkspace(t, s, "decision-temp-user")

	sendChatAndWaitForTurn(t, s, workspace.ID, map[string]any{"temperature": 0.5})

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	if got := *requests[0].Temperature; got != 0.5 {
		t.Errorf("request temperature = %v, want explicit 0.5 to win", got)
	}
}

func TestDecisionModelTemperatureDisabledKeepsEndpointTemperature(t *testing.T) {
	s, _ := newTestServer(t)
	s.llmSettings.DecisionModelTemperature = false
	s.decisionModel = decision.New("http://127.0.0.1:1", "") // enabled client, unreachable endpoint
	fake := &temperatureCapturingStreamer{}
	s.llm = fake
	workspace := createChatWorkspace(t, s, "decision-temp-disabled")

	sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	if got := *requests[0].Temperature; got != s.llmSettings.Temperature {
		t.Errorf("request temperature = %v, want endpoint default %v", got, s.llmSettings.Temperature)
	}
}

func TestDecisionModelTemperatureEndpointFailureFailsOpen(t *testing.T) {
	s, _ := newTestServer(t)
	s.llmSettings.DecisionModelTemperature = true
	s.decisionModel = decision.New("http://127.0.0.1:1", "") // unreachable: connection refused
	fake := &temperatureCapturingStreamer{}
	s.llm = fake
	workspace := createChatWorkspace(t, s, "decision-temp-fail")

	started := sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	if got := *requests[0].Temperature; got != s.llmSettings.Temperature {
		t.Errorf("request temperature = %v, want endpoint default %v on failure", got, s.llmSettings.Temperature)
	}
	if _, present := started["decisionTemperature"]; present {
		t.Errorf("turn_started should not carry decisionTemperature on failure: %v", started)
	}
}
