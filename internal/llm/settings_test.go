package llm

import "testing"

func TestSettingsForInteractionUsesSelectedEndpoint(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoints = []LLMEndpoint{
		{
			ID:                  "chat",
			Name:                "Chat",
			Endpoint:            "https://chat.example.test/v1",
			Model:               "chat-model",
			Temperature:         0.1,
			ContextLength:       4096,
			MaxTokens:           1024,
			RepetitionPenalty:   1,
			TimeoutSeconds:      30,
			ThinkingTokenBudget: 0,
		},
		{
			ID:                  "decompose",
			Name:                "Decompose",
			Endpoint:            "https://decompose.example.test/v1",
			Model:               "decompose-model",
			Temperature:         0.3,
			ContextLength:       12288,
			MaxTokens:           3072,
			RepetitionPenalty:   1.1,
			TimeoutSeconds:      35,
			ThinkingTokenBudget: 0,
		},
		{
			ID:                  "kanban",
			Name:                "Kanban",
			Endpoint:            "https://kanban.example.test/v1",
			Model:               "kanban-model",
			Temperature:         0.7,
			ContextLength:       8192,
			MaxTokens:           2048,
			RepetitionPenalty:   1.2,
			TimeoutSeconds:      45,
			ThinkingTokenBudget: -1,
		},
		{
			ID:                  "inline",
			Name:                "Inline",
			Endpoint:            "https://inline.example.test/v1",
			Model:               "inline-model",
			Temperature:         0.2,
			ContextLength:       16384,
			MaxTokens:           512,
			RepetitionPenalty:   1,
			TimeoutSeconds:      10,
			ThinkingTokenBudget: 0,
		},
	}
	settings.EndpointSelection = EndpointSelection{
		Chat:            "chat",
		KanbanDecompose: "decompose",
		Kanban:          "kanban",
		InlineCode:      "inline",
	}
	settings.Endpoint = "https://chat.example.test/v1"
	settings.Model = "chat-model"

	decompose := settings.ForInteraction(InteractionKanbanDecompose)
	if decompose.Endpoint != "https://decompose.example.test/v1" {
		t.Fatalf("expected decomposition endpoint, got %q", decompose.Endpoint)
	}
	if decompose.Model != "decompose-model" {
		t.Fatalf("expected decomposition model, got %q", decompose.Model)
	}
	if decompose.Temperature != 0.3 || decompose.ContextLength != 12288 || decompose.TimeoutSeconds != 35 {
		t.Fatalf("expected decomposition generation settings, got %#v", decompose)
	}

	kanban := settings.ForInteraction(InteractionKanban)
	if kanban.Endpoint != "https://kanban.example.test/v1" {
		t.Fatalf("expected kanban endpoint, got %q", kanban.Endpoint)
	}
	if kanban.Model != "kanban-model" {
		t.Fatalf("expected kanban model, got %q", kanban.Model)
	}
	if kanban.Temperature != 0.7 || kanban.ContextLength != 8192 || kanban.TimeoutSeconds != 45 {
		t.Fatalf("expected kanban generation settings, got %#v", kanban)
	}

	inline := settings.ForInteraction(InteractionInlineCode)
	if inline.Endpoint != "https://inline.example.test/v1" {
		t.Fatalf("expected inline endpoint, got %q", inline.Endpoint)
	}
	if inline.Model != "inline-model" {
		t.Fatalf("expected inline model, got %q", inline.Model)
	}
	if inline.Temperature != 0.2 || inline.ContextLength != 16384 || inline.TimeoutSeconds != 10 {
		t.Fatalf("expected inline generation settings, got %#v", inline)
	}
}

func TestSettingsDefaultsMissingKanbanDecomposeSelectionToKanban(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoints = append(settings.Endpoints, LLMEndpoint{
		ID:       "kanban",
		Name:     "Kanban",
		Endpoint: "https://kanban.example.test/v1",
		Model:    "kanban-model",
	})
	settings.EndpointSelection.Kanban = "kanban"
	settings.EndpointSelection.KanbanDecompose = ""

	normalized := settings.Normalized()
	if normalized.EndpointSelection.KanbanDecompose != "kanban" {
		t.Fatalf("expected missing decomposition selection to inherit kanban, got %q", normalized.EndpointSelection.KanbanDecompose)
	}
}

func TestSettingsNormalizesLegacyEndpointFieldsIntoSelectedChatEndpoint(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://legacy.example.test/v1"
	settings.Model = "legacy-model"

	normalized := settings.Normalized()
	if normalized.Endpoint != "https://legacy.example.test/v1" {
		t.Fatalf("expected legacy endpoint at top level, got %q", normalized.Endpoint)
	}
	if normalized.Model != "legacy-model" {
		t.Fatalf("expected legacy model at top level, got %q", normalized.Model)
	}
	endpoint := normalized.Endpoints[0]
	if endpoint.Endpoint != "https://legacy.example.test/v1" {
		t.Fatalf("expected migrated endpoint profile URL, got %q", endpoint.Endpoint)
	}
	if endpoint.Model != "legacy-model" {
		t.Fatalf("expected migrated endpoint profile model, got %q", endpoint.Model)
	}
	if endpoint.Temperature != settings.Temperature {
		t.Fatalf("expected migrated endpoint profile temperature, got %v", endpoint.Temperature)
	}
}

func TestSettingsCopiesLegacyGenerationFieldsIntoChatEndpoint(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://legacy.example.test/v1"
	settings.Model = "legacy-model"
	settings.Temperature = 0
	settings.ContextLength = 32768
	settings.TimeoutSeconds = 90
	settings.ThinkingTokenBudget = 0

	normalized := settings.Normalized()
	endpoint := normalized.Endpoints[0]
	if endpoint.Temperature != 0 {
		t.Fatalf("expected migrated zero temperature, got %v", endpoint.Temperature)
	}
	if endpoint.ContextLength != 32768 {
		t.Fatalf("expected migrated context length, got %d", endpoint.ContextLength)
	}
	if endpoint.TimeoutSeconds != 90 {
		t.Fatalf("expected migrated timeout, got %d", endpoint.TimeoutSeconds)
	}
	if endpoint.ThinkingTokenBudget != 0 {
		t.Fatalf("expected migrated thinking budget, got %d", endpoint.ThinkingTokenBudget)
	}
}

func TestSettingsRejectsDuplicateEndpointNames(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoints = []LLMEndpoint{
		{ID: "first", Name: "Local", Endpoint: "https://first.example.test/v1", Model: "model-a"},
		{ID: "second", Name: "local", Endpoint: "https://second.example.test/v1", Model: "model-b"},
	}
	settings.EndpointSelection = defaultEndpointSelection("first")
	settings.Endpoint = "https://first.example.test/v1"
	settings.Model = "model-a"

	if err := settings.Validate(); err == nil {
		t.Fatal("expected duplicate endpoint names to be rejected")
	}
}
