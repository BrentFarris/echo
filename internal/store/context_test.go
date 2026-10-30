package store

import (
	"github.com/brent/echo/internal/domain"
	"testing"
)

func TestContextMetadataSurvivesRestartWithoutReplacingTranscript(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateChat(domain.Chat{ID: "chat"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []domain.Message{
		{ID: "old", ChatID: "chat", Role: "user", Content: "old message without usage", Status: "completed"},
		{ID: "assistant", ChatID: "chat", Role: "assistant", Content: "answer", Status: "completed", Usage: &domain.TokenUsage{InputTokens: 500, OutputTokens: 20, TotalTokens: 520}},
		{ID: "tool", ChatID: "chat", Role: "tool", Content: "full original output", Status: "completed"},
	} {
		if err = s.AddMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.SetModelContent("wrong-chat", "tool", "omitted"); err == nil {
		t.Fatal("cross-chat write")
	}
	if _, err = s.SetModelContent("chat", "tool", "omitted"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	messages, err := s.Messages("chat")
	if err != nil || len(messages) != 3 {
		t.Fatal(messages, err)
	}
	if messages[0].Usage != nil || messages[1].Usage == nil || messages[1].Usage.InputTokens != 500 {
		t.Fatal(messages)
	}
	if messages[2].Content != "full original output" || messages[2].ModelContent == nil || *messages[2].ModelContent != "omitted" {
		t.Fatal(messages[2])
	}
	if err = s.DeleteChat("chat"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetModelContent("chat", "tool", "omitted again"); err == nil {
		t.Fatal("resurrected deleted message")
	}
}
