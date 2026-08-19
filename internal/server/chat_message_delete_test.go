package server

import (
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
)

func deletionTestTranscript() sessions.TabTranscript {
	return sessions.TabTranscript{
		ChatID:  "chat-delete",
		Preview: "second prompt",
		Turns: []sessions.Turn{
			{
				ID: "turn-one", UserContent: "first prompt", UserMessageIndex: 0, Status: "done",
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
	if !transcript.Turns[0].AssistantDeleted || len(transcript.Turns[0].AssistantTurns) != 0 {
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
