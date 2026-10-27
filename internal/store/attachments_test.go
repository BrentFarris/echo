package store

import (
	"bytes"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestAttachmentsAndModelSettingsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Chat{ID: "chat", Title: "Keep my title", Bots: map[string]string{"bot": "Historical name"}}
	if err := s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("contents\n"), 1000)
	a := domain.Attachment{ID: "file", ChatID: c.ID, Name: "test.txt", MediaType: "text/plain", Size: int64(len(data))}
	if err := s.AddAttachment(a, data); err != nil {
		t.Fatal(err)
	}
	m := domain.Message{ID: "message", ChatID: c.ID, Attachments: []domain.Attachment{a}}
	if err := s.Accept(c, m, nil, "request", "dispatch"); err != nil {
		t.Fatal(err)
	}
	g := domain.Generation{EndpointID: "model", Model: "test", Thinking: "high", Temperature: .12}
	if err := s.SetChatGeneration(c.ID, g); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Chat(c.ID)
	if err != nil || loaded.Generation != g || loaded.Title != c.Title || loaded.Bots["bot"] != c.Bots["bot"] {
		t.Fatal(loaded, err)
	}
	meta, contents, err := s.Attachment(c.ID, a.ID, false)
	if err != nil || meta != a || !bytes.Equal(data, contents) {
		t.Fatal(meta, err)
	}
	if err := s.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatGeneration(c.ID, g); err == nil {
		t.Fatal("late selection resurrected deleted chat")
	}
}

func TestGeneratedMediaSurvivesCrashAndDoesNotConsumeDraftQuota(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Chat{ID: "generated-chat"}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	m := domain.Message{ID: "tool", ChatID: c.ID, Role: "tool", BotID: "echo", Status: "running"}
	if err = s.AddMessage(m); err != nil {
		t.Fatal(err)
	}
	// Generated video can exceed the composer's upload cap.
	data := bytes.Repeat([]byte{42}, MaxAttachmentBytes+1)
	a := domain.Attachment{ID: "generated", ChatID: c.ID, Name: "result.mp4", MediaType: "video/mp4", Size: int64(len(data))}
	if err = s.AddGeneratedAttachment(a, m.ID, data); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxAttachments; i++ {
		pending := domain.Attachment{ID: domain.ID(), ChatID: c.ID, Name: "draft.txt", MediaType: "text/plain", Size: 1}
		if err = s.AddAttachment(pending, []byte("x")); err != nil {
			t.Fatal("generated output consumed draft quota", err)
		}
	}
	// No FinishMessage: simulate a crash immediately after bytes were persisted.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ms, err := s.Messages(c.ID)
	if err != nil || len(ms) != 1 || len(ms[0].Attachments) != 1 || ms[0].Attachments[0] != a {
		t.Fatal(ms, err)
	}
	_, got, err := s.Attachment(c.ID, a.ID, false)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("media changed across restart", err)
	}
	if err = s.RemoveAttachment(c.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Attachment(c.ID, a.ID, false); err != nil {
		t.Fatal("draft deletion removed generated media", err)
	}
	if err = s.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Attachment(c.ID, a.ID, false); err == nil {
		t.Fatal("chat deletion retained generated media")
	}
}
