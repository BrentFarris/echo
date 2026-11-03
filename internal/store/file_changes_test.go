package store

import (
	"reflect"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestFileChangesSurviveRestartAndDisappearWithChat(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateChat(domain.Chat{ID: "chat"}); err != nil {
		t.Fatal(err)
	}
	m := domain.Message{ID: "tool", ChatID: "chat", RunID: "run", Role: "tool", Status: "running"}
	if err = s.AddMessage(m); err != nil {
		t.Fatal(err)
	}
	m.Status = "completed"
	m.FileChanges = []domain.FileChange{{Path: "C:\\workspace\\file.go", DisplayPath: "workspace/file.go", Operation: "created", After: "revision"}}
	if err = s.FinishMessage(m); err != nil {
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
	if err != nil || len(messages) != 1 || !reflect.DeepEqual(messages[0].FileChanges, m.FileChanges) {
		t.Fatalf("%+v %v", messages, err)
	}
	if err = s.DeleteChat("chat"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishMessage(m); err == nil {
		t.Fatal("resurrected deleted change metadata")
	}
}
