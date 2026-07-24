package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestResearchAgentConcurrencyZeroPersistsAndMissingLegacyValueMigrates(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	settings := service.LoadState().Settings
	settings.ResearchAgentConcurrency = 0
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save disabled research setting: %v", err)
	}

	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read saved state: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode saved state: %v", err)
	}
	settingsRaw, ok := raw["settings"].(map[string]any)
	if !ok {
		t.Fatalf("saved settings were missing: %#v", raw)
	}
	if value, exists := settingsRaw["researchAgentConcurrency"]; !exists || value != float64(0) {
		t.Fatalf("expected explicit zero in saved state, got %#v", settingsRaw)
	}
	if got := NewSystemServiceWithStorePath(storePath).LoadState().Settings.ResearchAgentConcurrency; got != 0 {
		t.Fatalf("expected explicit zero to survive reload, got %d", got)
	}

	delete(settingsRaw, "researchAgentConcurrency")
	legacyData, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode legacy state: %v", err)
	}
	if err := os.WriteFile(storePath, legacyData, 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	legacyService := NewSystemServiceWithStorePath(storePath)
	if got := legacyService.LoadState().Settings.ResearchAgentConcurrency; got != llm.DefaultResearchAgentConcurrency {
		t.Fatalf("expected missing legacy value to migrate to %d, got %d", llm.DefaultResearchAgentConcurrency, got)
	}
	migratedData, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read migrated state: %v", err)
	}
	if !stateFileHasSettingKey(migratedData, "researchAgentConcurrency") {
		t.Fatal("expected migrated state to persist researchAgentConcurrency")
	}
}

func TestSaveSettingsPreservesIndependentEndpointModels(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	settings := service.LoadState().Settings
	settings.Endpoints = append(settings.Endpoints, llm.LLMEndpoint{
		ID:                  "second",
		Name:                "Second",
		Endpoint:            "https://second.example.test/v1",
		Model:               "second-model",
		Temperature:         0.2,
		ContextLength:       16384,
		MaxTokens:           4096,
		RepetitionPenalty:   1,
		TimeoutSeconds:      60,
		ThinkingTokenBudget: 0,
	})
	settings.EndpointSelection = llm.EndpointSelection{
		Chat:            "second",
		Research:        "second",
		KanbanDecompose: "second",
		Kanban:          "second",
		InlineCode:      "second",
	}

	// Leave the legacy top-level fields pointing at the original endpoint, as
	// can happen while a newly-added endpoint draft is being saved.
	saved, err := service.SaveSettings(settings)
	if err != nil {
		t.Fatalf("save endpoint settings: %v", err)
	}
	if got := saved.Settings.Endpoints[1].Model; got != "second-model" {
		t.Fatalf("expected new endpoint model to survive first save, got %q", got)
	}
	if saved.Settings.Model != "second-model" {
		t.Fatalf("expected top-level model mirror to follow selected endpoint, got %q", saved.Settings.Model)
	}

	saved.Settings.Endpoints[0].Model = "first-model-updated"
	savedAgain, err := service.SaveSettings(saved.Settings)
	if err != nil {
		t.Fatalf("save updated first endpoint: %v", err)
	}
	if got := savedAgain.Settings.Endpoints[0].Model; got != "first-model-updated" {
		t.Fatalf("expected first endpoint model update to persist, got %q", got)
	}
	if got := savedAgain.Settings.Endpoints[1].Model; got != "second-model" {
		t.Fatalf("expected second endpoint model to remain unchanged, got %q", got)
	}

	reloaded := NewSystemServiceWithStorePath(storePath).LoadState().Settings
	if reloaded.Endpoints[0].Model != "first-model-updated" || reloaded.Endpoints[1].Model != "second-model" {
		t.Fatalf("expected endpoint models to remain isolated after reload, got %#v", reloaded.Endpoints)
	}
}

func TestSaveSettingsStillAcceptsLegacyTopLevelEndpointUpdates(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	settings := service.LoadState().Settings
	settings.Endpoint = "https://legacy-client.example.test/v1"
	settings.Model = "legacy-client-model"

	saved, err := service.SaveSettings(settings)
	if err != nil {
		t.Fatalf("save legacy endpoint fields: %v", err)
	}
	if saved.Settings.Endpoint != "https://legacy-client.example.test/v1" {
		t.Fatalf("expected legacy endpoint update to persist, got %q", saved.Settings.Endpoint)
	}
	if saved.Settings.Model != "legacy-client-model" {
		t.Fatalf("expected legacy model update to persist, got %q", saved.Settings.Model)
	}
	if saved.Settings.Endpoints[0].Endpoint != saved.Settings.Endpoint ||
		saved.Settings.Endpoints[0].Model != saved.Settings.Model {
		t.Fatalf("expected legacy fields to migrate into endpoint profile, got %#v", saved.Settings.Endpoints[0])
	}
}

func TestSystemServiceResolvesSettingsForInteraction(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	settings := state.Settings
	settings.Endpoints = []llm.LLMEndpoint{
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
			SystemPromptAppendage: "Use research-specific instructions.",
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
	settings.EndpointSelection = llm.EndpointSelection{
		Chat:            "chat",
		Research:        "research",
		KanbanDecompose: "decompose",
		Kanban:          "kanban",
		InlineCode:      "inline",
	}
	settings.Endpoint = "https://chat.example.test/v1"
	settings.Model = "chat-model"
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	_, researchSettings, err := service.workspaceAndSettingsFor(state.ActiveWorkspaceID, llm.InteractionResearch)
	if err != nil {
		t.Fatalf("load research settings: %v", err)
	}
	if researchSettings.Endpoint != "https://research.example.test/v1" || researchSettings.Model != "research-model" {
		t.Fatalf("expected research endpoint, got %q / %q", researchSettings.Endpoint, researchSettings.Model)
	}
	if researchSettings.SystemPromptAppendage != "Use research-specific instructions." {
		t.Fatalf("expected research system prompt appendage, got %q", researchSettings.SystemPromptAppendage)
	}

	_, decomposeSettings, err := service.workspaceAndSettingsFor(state.ActiveWorkspaceID, llm.InteractionKanbanDecompose)
	if err != nil {
		t.Fatalf("load decomposition settings: %v", err)
	}
	if decomposeSettings.Endpoint != "https://decompose.example.test/v1" || decomposeSettings.Model != "decompose-model" {
		t.Fatalf("expected decomposition endpoint, got %q / %q", decomposeSettings.Endpoint, decomposeSettings.Model)
	}
	if decomposeSettings.Temperature != 0.3 || decomposeSettings.ContextLength != 12288 || decomposeSettings.TimeoutSeconds != 35 {
		t.Fatalf("expected decomposition generation settings, got %#v", decomposeSettings)
	}

	_, kanbanSettings, err := service.workspaceAndSettingsFor(state.ActiveWorkspaceID, llm.InteractionKanban)
	if err != nil {
		t.Fatalf("load kanban settings: %v", err)
	}
	if kanbanSettings.Endpoint != "https://kanban.example.test/v1" || kanbanSettings.Model != "kanban-model" {
		t.Fatalf("expected kanban endpoint, got %q / %q", kanbanSettings.Endpoint, kanbanSettings.Model)
	}
	if kanbanSettings.Temperature != 0.7 || kanbanSettings.ContextLength != 8192 || kanbanSettings.TimeoutSeconds != 45 {
		t.Fatalf("expected kanban generation settings, got %#v", kanbanSettings)
	}

	_, inlineSettings, err := service.workspaceAndSettingsFor(state.ActiveWorkspaceID, llm.InteractionInlineCode)
	if err != nil {
		t.Fatalf("load inline settings: %v", err)
	}
	if inlineSettings.Endpoint != "https://inline.example.test/v1" || inlineSettings.Model != "inline-model" {
		t.Fatalf("expected inline endpoint, got %q / %q", inlineSettings.Endpoint, inlineSettings.Model)
	}
	if inlineSettings.Temperature != 0.2 || inlineSettings.ContextLength != 16384 || inlineSettings.TimeoutSeconds != 10 {
		t.Fatalf("expected inline generation settings, got %#v", inlineSettings)
	}
}
