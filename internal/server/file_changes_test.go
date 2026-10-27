package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func TestToolFileChangesPersistAndRevealOnlyRecordedScopedPaths(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	for _, name := range []string{"edited.txt", "deleted.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bot, err := s.saveBot(domain.Bot{Handle: "kaiju", Name: "Kaiju", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		if request.Messages[len(request.Messages)-1].Role == "tool" {
			stream(w, "Finished the file changes.", nil)
			return
		}
		calls := []domain.ToolCall{}
		for i, item := range []struct{ name, path, content string }{{"file_write", "created.txt", "new"}, {"file_write", "created.txt", "updated"}, {"file_write", "edited.txt", "after"}, {"file_write", "edited.txt", "after"}, {"file_delete", "deleted.txt", ""}} {
			args, _ := json.Marshal(map[string]any{"path": item.path, "content": item.content})
			if item.name == "file_delete" {
				args, _ = json.Marshal(map[string]any{"path": item.path})
			}
			calls = append(calls, domain.ToolCall{ID: string(rune('a' + i)), Type: "function", Function: domain.FunctionCall{Name: item.name, Arguments: string(args)}})
		}
		stream(w, "Updating files.", calls)
	})
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "@kaiju update these files", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	messages, err := s.Store.Messages(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	changes := []domain.FileChange{}
	byOperation := map[string]domain.Message{}
	for _, message := range messages {
		for _, change := range message.FileChanges {
			changes = append(changes, change)
			byOperation[change.Operation] = message
		}
	}
	if len(changes) != 4 || changes[0].Operation != "created" || changes[1].Operation != "edited" || changes[2].Operation != "edited" || changes[3].Operation != "deleted" {
		t.Fatalf("%+v\n%+v", changes, messages)
	}
	var revealed []broker.RevealTarget
	s.revealFile = func(target broker.RevealTarget) error { revealed = append(revealed, target); return nil }
	reveal := func(message domain.Message, path string) *httptest.ResponseRecorder {
		return scmAPI(t, s, "POST", "/chats/"+c.ID+"/files/reveal", map[string]any{"messageId": message.ID, "path": path})
	}
	created := byOperation["created"]
	if response := reveal(created, created.FileChanges[0].Path); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	deleted := byOperation["deleted"]
	if response := reveal(deleted, deleted.FileChanges[0].Path); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if len(revealed) != 2 || !revealed[0].Select || revealed[1].Select || !strings.EqualFold(revealed[1].Path, root) {
		t.Fatal(revealed)
	}
	if response := reveal(created, filepath.Join(root, "unrecorded.txt")); response.Code == 200 {
		t.Fatal("accepted an unrecorded path")
	}
	other := chat(t, s, g)
	if response := scmAPI(t, s, "POST", "/chats/"+other.ID+"/files/reveal", map[string]any{"messageId": created.ID, "path": created.FileChanges[0].Path}); response.Code == 200 {
		t.Fatal("accepted a cross-chat path")
	}
	remote := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(`{}`))
	remote.RemoteAddr = "192.0.2.1:1234"
	response := httptest.NewRecorder()
	s.revealChangedFile(response, remote)
	if response.Code != http.StatusForbidden || len(revealed) != 2 {
		t.Fatal("remote request opened server UI")
	}
	bot.Roots = []string{t.TempDir()}
	if _, err = s.saveBot(bot); err != nil {
		t.Fatal(err)
	}
	if response := reveal(created, created.FileChanges[0].Path); response.Code == 200 {
		t.Fatal("ignored workspace revocation")
	}
	s.Store.DeleteChat(c.ID)
	if response := reveal(created, created.FileChanges[0].Path); response.Code == 200 {
		t.Fatal("revealed a deleted chat's file")
	}
}

func TestCancelledFileOperationRetainsObservedChanges(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	collector := &fileChangeCollector{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), fileChangesKey{}, collector))
	_, err := s.trackFileChanges(ctx, domain.Bot{Roots: []string{root}}, []string{root}, func() (any, error) {
		if err := os.WriteFile(filepath.Join(root, "partial.txt"), []byte("saved before stop"), 0600); err != nil {
			t.Fatal(err)
		}
		cancel()
		return nil, context.Canceled
	})
	if err != context.Canceled || len(collector.changes) != 1 || collector.changes[0].Operation != "created" {
		t.Fatalf("%+v %v", collector, err)
	}
}
