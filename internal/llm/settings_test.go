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
			ID:                    "research",
			Name:                  "Research",
			Endpoint:              "https://research.example.test/v1",
			Model:                 "research-model",
			Temperature:           0.15,
			ContextLength:         24576,
			MaxTokens:             1536,
			RepetitionPenalty:     1,
			TimeoutSeconds:        60,
			ThinkingTokenBudget:   0,
			SystemPromptAppendage: "Use the research model instructions.",
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
		Research:        "research",
		KanbanDecompose: "decompose",
		Kanban:          "kanban",
		InlineCode:      "inline",
	}
	settings.Endpoint = "https://chat.example.test/v1"
	settings.Model = "chat-model"

	research := settings.ForInteraction(InteractionResearch)
	if research.Endpoint != "https://research.example.test/v1" || research.Model != "research-model" {
		t.Fatalf("expected research endpoint, got %#v", research)
	}
	if research.ContextLength != 24576 || research.TimeoutSeconds != 60 {
		t.Fatalf("expected research generation settings, got %#v", research)
	}
	if research.SystemPromptAppendage != "Use the research model instructions." {
		t.Fatalf("expected research system prompt appendage, got %q", research.SystemPromptAppendage)
	}

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

func TestSettingsDefaultsResearchSelectionToChatAndNormalizesConcurrency(t *testing.T) {
	settings := DefaultSettings()
	settings.EndpointSelection.Research = ""
	settings.ResearchAgentConcurrency = 0

	normalized := settings.Normalized()
	if normalized.EndpointSelection.Research != normalized.EndpointSelection.Chat {
		t.Fatalf("expected research to inherit chat, got %q", normalized.EndpointSelection.Research)
	}
	if normalized.ResearchAgentConcurrency != 0 {
		t.Fatalf("expected zero concurrency to remain disabled, got %d", normalized.ResearchAgentConcurrency)
	}

	settings.ResearchAgentConcurrency = 99
	if got := settings.Normalized().ResearchAgentConcurrency; got != 8 {
		t.Fatalf("expected concurrency cap 8, got %d", got)
	}
	settings.ResearchAgentConcurrency = -3
	if got := settings.Normalized().ResearchAgentConcurrency; got != 0 {
		t.Fatalf("expected negative concurrency to normalize to disabled, got %d", got)
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

func TestNormalizedEndpointProfilesKeepsEndpointModelsIsolated(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoints = []LLMEndpoint{
		{
			ID:                "first",
			Name:              "First",
			Endpoint:          "https://first.example.test/v1",
			Model:             "first-model",
			ContextLength:     8192,
			MaxTokens:         2048,
			RepetitionPenalty: 1,
			TimeoutSeconds:    30,
		},
		{
			ID:                "second",
			Name:              "Second",
			Endpoint:          "https://second.example.test/v1",
			Model:             "second-model",
			ContextLength:     16384,
			MaxTokens:         4096,
			RepetitionPenalty: 1,
			TimeoutSeconds:    60,
		},
	}
	settings.EndpointSelection = defaultEndpointSelection("second")

	// Simulate stale legacy mirrors arriving alongside modern endpoint
	// profiles. These used to overwrite the selected profile on save.
	settings.Endpoint = "https://first.example.test/v1"
	settings.Model = "first-model"

	normalized := settings.NormalizedEndpointProfiles()
	if normalized.Endpoints[0].Model != "first-model" {
		t.Fatalf("expected first endpoint model to remain isolated, got %q", normalized.Endpoints[0].Model)
	}
	if normalized.Endpoints[1].Model != "second-model" {
		t.Fatalf("expected second endpoint model to remain isolated, got %q", normalized.Endpoints[1].Model)
	}
	if normalized.Endpoint != "https://second.example.test/v1" {
		t.Fatalf("expected legacy endpoint mirror to follow selected profile, got %q", normalized.Endpoint)
	}
	if normalized.Model != "second-model" {
		t.Fatalf("expected legacy model mirror to follow selected profile, got %q", normalized.Model)
	}
}

func TestNormalizedPreservesEndpointHeadersWhenNoGenerationConfig(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoints = []LLMEndpoint{
		{
			ID:       "custom",
			Name:     "Custom",
			Endpoint: "https://custom.example.test/v1",
			Model:    "custom-model",
			Headers:  map[string]string{"X-Api-Key": "secret123", "X-Custom": "value"},
		},
	}
	settings.EndpointSelection = defaultEndpointSelection("custom")
	settings.Endpoint = "https://custom.example.test/v1"
	settings.Model = "custom-model"

	normalized := settings.Normalized()
	endpoint := normalized.Endpoints[0]
	if endpoint.Headers["X-Api-Key"] != "secret123" {
		t.Fatalf("expected X-Api-Key header to be preserved, got %q", endpoint.Headers["X-Api-Key"])
	}
	if endpoint.Headers["X-Custom"] != "value" {
		t.Fatalf("expected X-Custom header to be preserved, got %q", endpoint.Headers["X-Custom"])
	}
	if len(endpoint.Headers) != 2 {
		t.Fatalf("expected 2 headers, got %d: %v", len(endpoint.Headers), endpoint.Headers)
	}
}

func TestNormalizedPreservesEndpointHeadersWhenLegacyFieldsApplied(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoints = []LLMEndpoint{
		{
			ID:       "custom",
			Name:     "Custom",
			Endpoint: "https://custom.example.test/v1",
			Model:    "custom-model",
			Headers:  map[string]string{"X-Api-Key": "secret123"},
		},
	}
	settings.EndpointSelection = defaultEndpointSelection("custom")
	// Legacy fields that trigger applyLegacyEndpointFields
	settings.Endpoint = "https://legacy-override.test/v1"
	settings.Model = "legacy-override-model"
	// Top-level headers should NOT overwrite per-endpoint headers
	settings.Headers = map[string]string{"Authorization": "Bearer token"}

	normalized := settings.Normalized()
	endpoint := normalized.Endpoints[0]
	// Per-endpoint headers must survive — not overwritten by settings.Headers
	if endpoint.Headers["X-Api-Key"] != "secret123" {
		t.Fatalf("expected X-Api-Key header to be preserved, got %q", endpoint.Headers["X-Api-Key"])
	}
	// The legacy endpoint fields should inherit endpoint + model from top-level
	if endpoint.Endpoint != "https://legacy-override.test/v1" {
		t.Fatalf("expected endpoint to be overridden by legacy field, got %q", endpoint.Endpoint)
	}
	if endpoint.Model != "legacy-override-model" {
		t.Fatalf("expected model to be overridden by legacy field, got %q", endpoint.Model)
	}
}

func TestNormalizedPreservesEndpointHeadersWithGenerationConfig(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoints = []LLMEndpoint{
		{
			ID:                "custom",
			Name:              "Custom",
			Endpoint:          "https://custom.example.test/v1",
			Model:             "custom-model",
			Temperature:       0.5,
			ContextLength:     8192,
			MaxTokens:         2048,
			RepetitionPenalty: 1,
			TimeoutSeconds:    30,
			Headers:           map[string]string{"X-Api-Key": "secret123"},
		},
	}
	settings.EndpointSelection = defaultEndpointSelection("custom")
	settings.Endpoint = "https://custom.example.test/v1"
	settings.Model = "custom-model"

	normalized := settings.Normalized()
	endpoint := normalized.Endpoints[0]
	// The endpoint already has a generation config, so Normalized() should not
	// call WithGenerationFromSettings and should preserve headers directly.
	if endpoint.Headers["X-Api-Key"] != "secret123" {
		t.Fatalf("expected X-Api-Key header to be preserved, got %q", endpoint.Headers["X-Api-Key"])
	}
}

func TestApplyToSettingsPreservesEndpointHeaders(t *testing.T) {
	endpoint := LLMEndpoint{
		ID:       "custom",
		Name:     "Custom",
		Endpoint: "https://custom.example.test/v1",
		Model:    "custom-model",
		Headers:  map[string]string{"X-Api-Key": "secret123"},
	}
	settings := DefaultSettings()
	settings.Headers = map[string]string{"Authorization": "Bearer token"}

	applied := endpoint.ApplyToSettings(settings)
	// Endpoint headers should replace settings headers entirely
	if applied.Headers["X-Api-Key"] != "secret123" {
		t.Fatalf("expected X-Api-Key header from endpoint to be applied, got %q", applied.Headers["X-Api-Key"])
	}
	// The previous settings.Headers should be gone (replaced by endpoint headers)
	if len(applied.Headers) != 1 {
		t.Fatalf("expected exactly 1 header (endpoint's), got %d: %v", len(applied.Headers), applied.Headers)
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
