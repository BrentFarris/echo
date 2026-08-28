package server

import (
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

func deletionTestTranscript() sessions.TabTranscript {
	return sessions.TabTranscript{
		ChatID:  "chat-delete",
		Preview: "second prompt",
		Turns: []sessions.Turn{
			{
				ID: "turn-one", UserContent: "first prompt", UserMessageIndex: 0, Status: "done",
				References:        []sessions.PromptReference{{Kind: "file", Label: "main.go", ReferencePath: "echo/main.go", Ref: sessions.FileReference{RootID: "root", Path: "main.go"}}},
				EditorContext:     &sessions.EditorContextSummary{Tabs: []sessions.EditorContextTab{{Kind: "file", Title: "main.go", Active: true}}},
				FileChanges:       []sessions.FileChange{{Path: "echo/main.go", Operation: tools.FileChangeEdited, Ref: &sessions.FileReference{RootID: "root", Path: "main.go"}}},
				ResearchReasoning: []sessions.ResearchReasoning{{AgentID: "agent-one", AgentName: "Scout", Reasoning: "private work"}},
				ResearchTools:     []sessions.ToolActivity{{CallID: "agent-one:read", Name: "filesystem_read_text", AgentID: "agent-one"}},
				AssistantTurns: []sessions.AssistantTurn{{
					Number: 0, Content: "working", HasToolCalls: true,
					Tools: []sessions.ToolActivity{{CallID: "call-one", Name: "filesystem_read_text", Result: "tool result"}},
				}, {Number: 1, Content: "first answer"}},
			},
			{
				ID: "turn-two", UserContent: "second prompt", UserMessageIndex: 5, Status: "done",
				AssistantTurns: []sessions.AssistantTurn{{Number: 0, Content: "second answer"}},
			},
		},
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "first prompt"},
			{Role: llm.RoleAssistant, Content: "working", ToolCalls: []llm.ToolCall{{ID: "call-one"}}},
			{Role: llm.RoleTool, ToolCallID: "call-one", Content: "tool result"},
			{Role: llm.RoleUser, Content: "visual tool result"},
			{Role: llm.RoleAssistant, Content: "first answer"},
			{Role: llm.RoleUser, Content: "second prompt"},
			{Role: llm.RoleAssistant, Content: "second answer"},
		},
	}
}

func TestDeleteTranscriptAssistantRemovesEntireToolChain(t *testing.T) {
	transcript := deletionTestTranscript()
	if err := deleteTranscriptMessage(&transcript, "turn-one", llm.RoleAssistant); err != nil {
		t.Fatal(err)
	}

	if len(transcript.Messages) != 3 || transcript.Messages[0].Content != "first prompt" ||
		transcript.Messages[1].Content != "second prompt" || transcript.Messages[2].Content != "second answer" {
		t.Fatalf("assistant context was not fully removed: %#v", transcript.Messages)
	}
	if !transcript.Turns[0].AssistantDeleted || len(transcript.Turns[0].AssistantTurns) != 0 || len(transcript.Turns[0].FileChanges) != 0 ||
		len(transcript.Turns[0].ResearchReasoning) != 0 || len(transcript.Turns[0].ResearchTools) != 0 {
		t.Fatalf("assistant display payload was retained: %#v", transcript.Turns[0])
	}
	if transcript.Turns[1].UserMessageIndex != 1 {
		t.Fatalf("later user index was not repaired: %d", transcript.Turns[1].UserMessageIndex)
	}
}

func TestDeleteTranscriptHalvesInEitherOrderRemovesEmptyTurn(t *testing.T) {
	for _, roles := range [][]string{{llm.RoleUser, llm.RoleAssistant}, {llm.RoleAssistant, llm.RoleUser}} {
		transcript := deletionTestTranscript()
		for _, role := range roles {
			if err := deleteTranscriptMessage(&transcript, "turn-one", role); err != nil {
				t.Fatalf("delete %s after %v: %v", role, roles, err)
			}
		}
		if len(transcript.Turns) != 1 || transcript.Turns[0].ID != "turn-two" {
			t.Fatalf("empty turn remained after %v: %#v", roles, transcript.Turns)
		}
		if transcript.Turns[0].UserMessageIndex != 0 || len(transcript.Messages) != 2 || transcript.Messages[0].Content != "second prompt" {
			t.Fatalf("remaining context was corrupt after %v: %#v / %#v", roles, transcript.Turns, transcript.Messages)
		}
	}
}

func TestDeleteTranscriptUserClearsPromptResources(t *testing.T) {
	transcript := deletionTestTranscript()
	if err := deleteTranscriptMessage(&transcript, "turn-one", llm.RoleUser); err != nil {
		t.Fatal(err)
	}
	if len(transcript.Turns[0].References) != 0 || transcript.Turns[0].EditorContext != nil {
		t.Fatalf("deleted user prompt retained resources: %#v", transcript.Turns[0])
	}
}

func TestRerunTurnReturnsInputAndDropsSelectedAndLaterContext(t *testing.T) {
	transcript := deletionTestTranscript()
	transcript.Turns[0].Model = "model-one"
	transcript.Turns[0].AgentModeID = "general"
	transcript.Turns[0].Images = []sessions.MediaAttachment{{ID: "image-one", Name: "input.png", DataURL: "data:image/png;base64,eA=="}}
	selected, updated, err := rerunTurn(transcript, "turn-one")
	if err != nil {
		t.Fatal(err)
	}

	if selected.UserContent != "first prompt" || selected.Model != "model-one" || selected.AgentModeID != "general" || len(selected.Images) != 1 ||
		len(selected.References) != 1 || selected.EditorContext == nil {
		t.Fatalf("selected input was not preserved: %#v", selected)
	}
	if len(selected.FileChanges) != 0 {
		t.Fatalf("rerun retained obsolete file changes: %#v", selected.FileChanges)
	}
	if len(updated.Turns) != 0 || len(updated.Messages) != 0 {
		t.Fatalf("selected and later context remained: %#v / %#v", updated.Turns, updated.Messages)
	}
	if updated.Revision != transcript.Revision+1 {
		t.Fatalf("rerun prefix revision was not advanced: %d", updated.Revision)
	}
}

func TestRerunTurnRequiresPreservedUserMessage(t *testing.T) {
	transcript := deletionTestTranscript()
	transcript.Turns[0].UserDeleted = true
	transcript.Turns[0].UserContent = ""
	if _, _, err := rerunTurn(transcript, "turn-one"); err == nil {
		t.Fatal("expected rerun to reject a deleted user message")
	}
}

func TestEditAssistantTranscriptChangesOnlyFinalResponse(t *testing.T) {
	original := deletionTestTranscript()
	updated := cloneTabTranscript(original)
	if err := editAssistantTranscript(&updated, "turn-one", "edited first answer"); err != nil {
		t.Fatal(err)
	}

	if updated.Turns[0].AssistantTurns[1].Content != "edited first answer" || updated.Messages[4].Content != "edited first answer" {
		t.Fatalf("final response was not updated in both histories: %#v / %#v", updated.Turns[0], updated.Messages)
	}
	if updated.Turns[0].AssistantTurns[0].Content != "working" || updated.Messages[1].Content != "working" ||
		updated.Messages[2].Content != "tool result" || updated.Turns[1].AssistantTurns[0].Content != "second answer" {
		t.Fatalf("assistant edit changed tool or later context: %#v / %#v", updated.Turns, updated.Messages)
	}
	if len(updated.Turns[0].FileChanges) != 1 || updated.Turns[0].FileChanges[0].Path != "echo/main.go" {
		t.Fatalf("assistant edit removed its file change summary: %#v", updated.Turns[0].FileChanges)
	}
	if original.Turns[0].AssistantTurns[1].Content != "first answer" {
		t.Fatalf("failed edit rollback clone isolation: %#v", original.Turns[0])
	}
}
