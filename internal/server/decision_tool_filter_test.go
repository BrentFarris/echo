// Tests for decision-model tool filtering (Phase 4): the decision model
// removes tool categories the prompt does not need from the request schema,
// the feature fails open when disabled or the endpoint errors, and a model
// that calls a filtered tool anyway gets a soft per-turn error.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/decision"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func schemaToolNames(schema []llm.Tool) map[string]bool {
	names := make(map[string]bool, len(schema))
	for _, tool := range schema {
		names[tool.Function.Name] = true
	}
	return names
}

func TestFilterToolSchemaByCategoriesNilKeepsSchema(t *testing.T) {
	schema := []llm.Tool{{Type: "function", Function: llm.ToolFunction{Name: "shell_command"}}}
	filtered := filterToolSchemaByCategories(schema, nil)
	if len(filtered) != 1 || filtered[0].Function.Name != "shell_command" {
		t.Fatalf("nil filter must return the schema unchanged, got %v", filtered)
	}
}

func TestFilterToolSchemaByCategoriesRemovesFilteredCategories(t *testing.T) {
	schema := tools.ChatLLMSchemaForScopes(nil, tools.ChatSchemaOptions{})
	if len(schema) == 0 {
		t.Fatal("registry produced no tools")
	}
	kept := map[string]bool{"filesystem_read": true, "web": true}
	filtered := filterToolSchemaByCategories(schema, kept)
	names := schemaToolNames(filtered)

	if !names["filesystem_read_text"] || !names["web_search"] {
		t.Errorf("kept categories must survive: %v", names)
	}
	for _, removed := range []string{"shell_command", "filesystem_edit_text", "comfyui_generate", "git_inspect"} {
		if names[removed] {
			t.Errorf("tool %s should have been filtered out", removed)
		}
	}
	// The decision-model escape hatch and unclassifiable tools stay.
	if !names[tools.DecisionModelToolName] {
		t.Errorf("decision_model must never be filtered")
	}
	if len(filtered) >= len(schema) {
		t.Errorf("filtering removed nothing: %d -> %d", len(schema), len(filtered))
	}
}

func TestToolFilteredForTurn(t *testing.T) {
	if toolFilteredForTurn("shell_command", nil) {
		t.Error("nil filter must never report filtered")
	}
	kept := map[string]bool{"filesystem_read": true}
	if toolFilteredForTurn("filesystem_read_text", kept) {
		t.Error("kept category reported filtered")
	}
	if !toolFilteredForTurn("shell_command", kept) {
		t.Error("filtered category not reported")
	}
	if toolFilteredForTurn("decision_model", kept) || toolFilteredForTurn("ask_user_questions", kept) {
		t.Error("always-kept tools must never be reported filtered")
	}
	if toolFilteredForTurn("some_plugin_tool", kept) {
		t.Error("unclassifiable tools must never be reported filtered")
	}
}

// toolFilterDecisionServer returns a fake decision-model endpoint that
// answers every needs_<category> noul question with keepProb for kept
// categories and 1-keepProb otherwise.
func toolFilterDecisionServer(t *testing.T, kept map[string]bool, keepProb float64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode decision request: %v", err)
		}
		answers := map[string]any{}
		for name := range body.Questions {
			category := strings.TrimPrefix(name, "needs_")
			prob := 1 - keepProb
			if kept[category] {
				prob = keepProb
			}
			answers[name] = map[string]any{"type": "noul", "noul": prob}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "clef-flash",
			"answers": answers,
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 0},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// newToolFilterHarness wires a chat server with decision-model tool filtering
// enabled against the given fake endpoint and capturing streamer.
func newToolFilterHarness(t *testing.T, decisionServer *httptest.Server) (*Server, *temperatureCapturingStreamer) {
	t.Helper()
	s, _ := newTestServer(t)
	s.llmSettings.DecisionModelToolFiltering = true
	if decisionServer != nil {
		s.decisionModel = decision.New(decisionServer.URL, "")
	}
	fake := &temperatureCapturingStreamer{}
	s.llm = fake
	return s, fake
}

func requestToolNames(request llm.ChatRequest) map[string]bool {
	names := make(map[string]bool, len(request.Tools))
	for _, tool := range request.Tools {
		names[tool.Function.Name] = true
	}
	return names
}

func TestDecisionModelToolFilterRemovesUnneededCategories(t *testing.T) {
	decisionServer := toolFilterDecisionServer(t, map[string]bool{"filesystem_read": true, "web": true}, 0.95)
	s, fake := newToolFilterHarness(t, decisionServer)
	workspace := createChatWorkspace(t, s, "decision-tool-filter")

	started := sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	names := requestToolNames(requests[0])
	if !names["filesystem_read_text"] || !names["web_search"] {
		t.Errorf("kept categories missing from request tools: %v", names)
	}
	for _, removed := range []string{"shell_command", "filesystem_edit_text", "comfyui_generate"} {
		if names[removed] {
			t.Errorf("tool %s should have been filtered from the request", removed)
		}
	}
	if !names[tools.DecisionModelToolName] {
		t.Errorf("decision_model must stay in the request tools: %v", names)
	}
	filter, ok := started["decisionToolFilter"].(map[string]any)
	if !ok {
		t.Fatalf("turn_started missing decisionToolFilter: %v", started)
	}
	keptList, _ := filter["keptCategories"].([]any)
	foundKept := false
	for _, name := range keptList {
		if name == "filesystem_read" {
			foundKept = true
		}
	}
	if !foundKept {
		t.Errorf("decisionToolFilter keptCategories missing filesystem_read: %v", filter)
	}
}

func TestDecisionModelToolFilterDisabledKeepsAllTools(t *testing.T) {
	s, fake := newToolFilterHarness(t, nil) // feature flag off, no endpoint
	workspace := createChatWorkspace(t, s, "decision-tool-filter-off")

	sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	names := requestToolNames(requests[0])
	if !names["shell_command"] || !names["filesystem_edit_text"] {
		t.Errorf("unfiltered request lost tools: %v", names)
	}
}

func TestDecisionModelToolFilterEndpointFailureFailsOpen(t *testing.T) {
	s, fake := newToolFilterHarness(t, nil)
	s.llmSettings.DecisionModelToolFiltering = true
	s.decisionModel = decision.New("http://127.0.0.1:1", "") // unreachable
	workspace := createChatWorkspace(t, s, "decision-tool-filter-fail")

	started := sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	names := requestToolNames(requests[0])
	if !names["shell_command"] {
		t.Errorf("endpoint failure must fail open to the full schema: %v", names)
	}
	if _, present := started["decisionToolFilter"]; present {
		t.Errorf("turn_started should not carry decisionToolFilter on failure: %v", started)
	}
}

func TestDecisionModelToolFilterMissingAnswersFailOpen(t *testing.T) {
	// Endpoint answers 200 with no answers at all: every category is kept.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "clef-flash",
			"answers": map[string]any{},
			"usage":   map[string]int{"input_tokens": 10, "output_tokens": 0},
		})
	}))
	t.Cleanup(server.Close)
	s, fake := newToolFilterHarness(t, server)
	workspace := createChatWorkspace(t, s, "decision-tool-filter-empty")

	sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) == 0 {
		t.Fatal("streamer saw no requests")
	}
	names := requestToolNames(requests[0])
	if !names["shell_command"] {
		t.Errorf("missing answers must keep every category: %v", names)
	}
}

// toolFilterSequenceStreamer plays a scripted sequence of stream events per
// request: the first request calls a filtered tool, later ones finish plain.
type toolFilterSequenceStreamer struct {
	mu        sync.Mutex
	sequences [][]llm.StreamEvent
	requests  []llm.ChatRequest
}

func (f *toolFilterSequenceStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	events := make(chan llm.StreamEvent, 8)
	if index < len(f.sequences) {
		for _, event := range f.sequences[index] {
			events <- event
		}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "done"}
	}
	events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	close(events)
	return &llm.Stream{ID: "tool-filter", Events: events}
}

func (f *toolFilterSequenceStreamer) captured() []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.ChatRequest(nil), f.requests...)
}

func TestDecisionModelToolFilterSoftErrorsOnFilteredToolCall(t *testing.T) {
	decisionServer := toolFilterDecisionServer(t, map[string]bool{"filesystem_read": true}, 0.95)
	s, _ := newToolFilterHarness(t, decisionServer)
	fake := &toolFilterSequenceStreamer{sequences: [][]llm.StreamEvent{{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
			Index: 0, ID: "call-shell-1", Type: "function",
			Function: llm.FunctionCallDelta{Name: "shell_command", Arguments: `{"command":"echo hi"}`},
		}},
		{Type: llm.EventComplete, FinishReason: "tool_calls"},
	}}}
	s.llm = fake
	workspace := createChatWorkspace(t, s, "decision-tool-filter-soft")

	sendChatAndWaitForTurn(t, s, workspace.ID, nil)

	requests := fake.captured()
	if len(requests) < 2 {
		t.Fatalf("expected a follow-up round after the filtered tool call, saw %d requests", len(requests))
	}
	var toolMessages []string
	for _, message := range requests[1].Messages {
		if message.Role == llm.RoleTool {
			toolMessages = append(toolMessages, message.Content)
		}
	}
	if len(toolMessages) == 0 {
		t.Fatal("follow-up request carried no tool result message")
	}
	if !strings.Contains(toolMessages[0], "tool_filtered") {
		t.Errorf("filtered tool call should produce a soft tool_filtered error, got %q", toolMessages[0])
	}
}
