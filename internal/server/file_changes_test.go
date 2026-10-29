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
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/model"
)

func TestChangedFilesOpenInEditorOrRevealWhenDisabled(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	path := filepath.Join(root, "changed.txt")
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "editor-changes", Name: "Editor Changes", Roots: []string{t.TempDir(), root}})
	if err != nil {
		t.Fatal(err)
	}
	c := chat(t, s, domain.Generation{})
	message := domain.Message{ID: "change", ChatID: c.ID, BotID: bot.ID, Role: "tool", Status: "completed", FileChanges: []domain.FileChange{{Path: path, Operation: "created"}}}
	if err := s.Store.AddMessage(message); err != nil {
		t.Fatal(err)
	}
	var revealed []broker.RevealTarget
	s.revealFile = func(target broker.RevealTarget) error { revealed = append(revealed, target); return nil }
	open := func(chatID, file string) *httptest.ResponseRecorder {
		return scmAPI(t, s, "POST", "/chats/"+chatID+"/files/open", map[string]any{"messageId": message.ID, "path": file})
	}
	roots, err := s.EditorFS.Roots(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEditor := func(response *httptest.ResponseRecorder, relative, kind string) {
		t.Helper()
		var result struct {
			Editor struct {
				BotID string           `json:"botId"`
				Ref   editorfs.FileRef `json:"ref"`
				Kind  string           `json:"kind"`
			} `json:"editor"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Editor.BotID != bot.ID || result.Editor.Ref != (editorfs.FileRef{RootID: roots[1].ID, Path: relative}) || result.Editor.Kind != kind || len(revealed) != 0 {
			t.Fatalf("unexpected editor target: %d %s; revealed: %+v", response.Code, response.Body.String(), revealed)
		}
	}
	assertEditor(open(c.ID, path), "changed.txt", "file")
	if response := open(c.ID, filepath.Join(root, "unrecorded.txt")); response.Code == http.StatusOK {
		t.Fatal("opened an unrecorded path")
	}
	other := chat(t, s, domain.Generation{})
	if response := open(other.ID, path); response.Code == http.StatusOK {
		t.Fatal("opened a cross-chat path")
	}
	// The editor works for authenticated remote browsers without opening host UI.
	setRemotePassword(t, s)
	owner := remoteLogin(t, s)
	body, _ := json.Marshal(map[string]any{"messageId": message.ID, "path": path})
	assertEditor(remoteRequest(s, "POST", "/chats/"+c.ID+"/files/open", string(body), owner), "changed.txt", "file")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertEditor(open(c.ID, path), "", "directory")
	plugin, _ := s.Plugins.Get("monaco-editor")
	plugin.Enabled = false
	if err := s.Plugins.Save(plugin); err != nil {
		t.Fatal(err)
	}
	if response := open(c.ID, path); response.Code != http.StatusOK || len(revealed) != 1 || revealed[0].Select || !strings.EqualFold(revealed[0].Path, root) {
		t.Fatalf("disabled-editor fallback: %d %s; %+v", response.Code, response.Body.String(), revealed)
	}
	if response := remoteRequest(s, "POST", "/chats/"+c.ID+"/files/open", string(body), owner); response.Code != http.StatusForbidden || len(revealed) != 1 {
		t.Fatal("remote browser opened host UI with the editor disabled")
	}
	plugin.Enabled = true
	if err := s.Plugins.Save(plugin); err != nil {
		t.Fatal(err)
	}
	bot.Roots = []string{t.TempDir()}
	if _, err := s.saveBot(bot); err != nil {
		t.Fatal(err)
	}
	if response := open(c.ID, path); response.Code == http.StatusOK {
		t.Fatal("ignored workspace revocation")
	}
}

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
