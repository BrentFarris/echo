package store

import (
	"github.com/brent/echo/internal/domain"
	"testing"
)

func TestMessageActionsOwnershipChunksAndDependencies(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := domain.Chat{ID: "chat"}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	call := domain.ToolCall{ID: "call"}
	ms := []domain.Message{
		{ID: "user", Role: "user"},
		{ID: "assistant", Role: "assistant", RunID: "run", Content: "original", Calls: []domain.ToolCall{call}},
		{ID: "tool", Role: "tool", RunID: "run", CallID: "call"},
		{ID: "other", Role: "tool", RunID: "other-run", CallID: "call"},
		{ID: "later", Role: "assistant", RunID: "run", Content: "final"},
	}
	for _, m := range ms {
		m.ChatID = c.ID
		if err = s.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.AppendChunk("assistant", " stale", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateMessageContent("wrong-chat", "assistant", "wrong"); err == nil {
		t.Fatal("cross-chat edit succeeded")
	}
	if err = s.DeleteMessage("wrong-chat", "assistant"); err == nil {
		t.Fatal("cross-chat delete succeeded")
	}
	if err = s.UpdateMessageContent(c.ID, "assistant", "edited"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Messages(c.ID)
	if got[1].Content != "edited" || len(got[1].Calls) != 1 {
		t.Fatal(got[1])
	}
	if err = s.DeleteMessage(c.ID, "user"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Messages(c.ID)
	if len(got) != 4 {
		t.Fatal(got)
	}
	if err = s.DeleteMessage(c.ID, "assistant"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Messages(c.ID)
	if len(got) != 2 || got[0].ID != "other" || got[1].ID != "later" {
		t.Fatal(got)
	}
}

func TestResubmitAcceptanceRollsBackAndKeepsUploads(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := domain.Chat{ID: "chat"}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	a := domain.Attachment{ID: "upload", ChatID: c.ID, Name: "file.txt", MediaType: "text/plain", Size: 4}
	if err = s.AddAttachment(a, []byte("data")); err != nil {
		t.Fatal(err)
	}
	original := domain.Message{ID: "user", ChatID: c.ID, Role: "user", Attachments: []domain.Attachment{a}}
	if err = s.Accept(c, original, nil, "first", "first-dispatch"); err != nil {
		t.Fatal(err)
	}
	if err = s.AddMessage(domain.Message{ID: "later", ChatID: c.ID, Role: "assistant"}); err != nil {
		t.Fatal(err)
	}
	replacement := domain.Message{ID: "replacement", ChatID: c.ID, Role: "user", Content: "edited", Attachments: []domain.Attachment{a}}
	if err = s.Accept(c, replacement, nil, "first", "duplicate", original.ID); err == nil {
		t.Fatal("duplicate retry key accepted")
	}
	got, _ := s.Messages(c.ID)
	if len(got) != 2 || got[0].ID != "user" {
		t.Fatal("failed acceptance lost history", got)
	}
	if _, data, err := s.Attachment(c.ID, a.ID, false); err != nil || string(data) != "data" {
		t.Fatal(err, data)
	}
	if err = s.Accept(c, replacement, nil, "edit", "edit-dispatch", original.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Messages(c.ID)
	if len(got) != 1 || got[0].ID != "replacement" {
		t.Fatal(got)
	}
	if s.Accepted(c.ID, "edit") != "edit-dispatch" {
		t.Fatal("missing retry key")
	}
	if err = s.DeleteMessage(c.ID, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Attachment(c.ID, a.ID, false); err == nil {
		t.Fatal("attachment did not cascade")
	}
}
