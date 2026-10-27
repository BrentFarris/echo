package store

import (
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestChunksRecoveryDeletion(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c := domain.Chat{ID: "c"}
	if e = s.CreateChat(c); e != nil {
		t.Fatal(e)
	}
	m := domain.Message{ID: "m", ChatID: "c", Status: "running"}
	if e = s.AddMessage(m); e != nil {
		t.Fatal(e)
	}
	s.AppendChunk("m", strings.Repeat("stream ", 1000), "thinking")
	s.Put("runs", "r", domain.Run{ID: "r", ChatID: "c", Status: "running"})
	if e = s.Recover(); e != nil {
		t.Fatal(e)
	}
	ms, e := s.Messages("c")
	if e != nil || len(ms) != 1 || ms[0].Status != "interrupted" || len(ms[0].Content) != 7000 {
		t.Fatalf("%+v %v", ms, e)
	}
	if e = s.DeleteChat("c"); e != nil {
		t.Fatal(e)
	}
	if e = s.AppendChunk("m", "late", ""); e == nil {
		t.Fatal("late stream resurrected deleted chat")
	}
	if e = s.SaveChat(c); e == nil {
		t.Fatal("late metadata resurrected deleted chat")
	}
}
