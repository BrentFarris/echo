package services

import (
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/llm"
)

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
		KanbanDecompose: "decompose",
		Kanban:          "kanban",
		InlineCode:      "inline",
	}
	settings.Endpoint = "https://chat.example.test/v1"
	settings.Model = "chat-model"
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
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
