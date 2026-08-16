package services

import (
	"testing"

	"github.com/brent/echo/internal/llm"
)

// Temporary diagnostic: simulate the ORIGINAL chat model-switch payload
// (selection changes A->B but top-level mirrors still point at A, endpoints
// unchanged from stored). Print what survives.
func TestDiagnosticChatSwitchOriginalPayload(t *testing.T) {
	storePath := t.TempDir() + "/state.json"
	service := NewSystemServiceWithStorePath(storePath)

	settings := service.LoadState().Settings
	settings.Endpoints = []llm.LLMEndpoint{
		{ID: "a", Name: "A", Endpoint: "https://a.example.test/v1", Model: "model-a", ContextLength: 8192, MaxTokens: 2048, RepetitionPenalty: 1, TimeoutSeconds: 30},
		{ID: "b", Name: "B", Endpoint: "https://b.example.test/v1", Model: "model-b", ContextLength: 16384, MaxTokens: 4096, RepetitionPenalty: 1, TimeoutSeconds: 60},
	}
	settings.EndpointSelection = llm.EndpointSelection{Chat: "a", Research: "a", KanbanDecompose: "a", Kanban: "a", InlineCode: "a"}
	saved, err := service.SaveSettings(settings)
	if err != nil {
		t.Fatalf("initial save: %v", err)
	}
	for _, ep := range saved.Settings.Endpoints {
		t.Logf("after init: %s -> endpoint=%q model=%q", ep.ID, ep.Endpoint, ep.Model)
	}

	// Simulate original frontend: switch chat to b, but top-level mirrors still = a.
	incoming := saved.Settings.Clone()
	incoming.EndpointSelection.Chat = "b"
	// mirrors remain pointing at a (stale), endpoints unchanged
	if _, ok := llmEndpointByID(incoming.Endpoints, "a"); !ok {
		t.Fatalf("endpoint a missing")
	}
	_ = incoming

	saved2, err := service.SaveSettings(incoming)
	if err != nil {
		t.Fatalf("switch save: %v", err)
	}
	for _, ep := range saved2.Settings.Endpoints {
		t.Logf("after switch to b: %s -> endpoint=%q model=%q", ep.ID, ep.Endpoint, ep.Model)
	}
	t.Logf("top-level mirror after switch: endpoint=%q model=%q chatSel=%q", saved2.Settings.Endpoint, saved2.Settings.Model, saved2.Settings.EndpointSelection.Chat)
}

func llmEndpointByID(endpoints []llm.LLMEndpoint, id string) (llm.LLMEndpoint, bool) {
	for _, e := range endpoints {
		if e.ID == id {
			return e, true
		}
	}
	return llm.LLMEndpoint{}, false
}
