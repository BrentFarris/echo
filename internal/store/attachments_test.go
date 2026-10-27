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
