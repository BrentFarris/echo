package server

import (
	"github.com/brent/echo/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEditorDoesNotInterfereWithChat(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "Editor chat compatibility", nil) })
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "Hello", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	for _, run := range dispatchRuns(t, s, d) {
		if run.Status != "completed" {
			t.Fatalf("run: %+v", run)
		}
	}
}

func TestEditorRoutesAndRevocation(t *testing.T) {
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "editor-bot", Name: "Editor Bot", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	setRemotePassword(t, s)
	owner := remoteLogin(t, s)
	request := func(path string) *httptest.ResponseRecorder { return remoteRequest(s, "GET", path, "", owner) }
	path := "/plugins/monaco-editor/bots/" + bot.ID + "/fs/roots"
	if w := request(path); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	remoteRequest(s, "PATCH", "/plugins/monaco-editor", `{"enabled":false,"config":{}}`, owner)
	if w := request(path); w.Code != 403 {
		t.Fatal("disabled editor remained accessible", w.Code, w.Body.String())
	}
}

func TestEditorBookmarkMutations(t *testing.T) {
	ref := BookmarkRef{RootID: "root", Path: "src/file.ts"}
	state, err := mutateEditorBookmarks(BookmarkState{Version: 1}, BookmarkAction{Action: "toggle", Ref: ref, Line: 3, Preview: "first"})
	if err != nil || len(state.Bookmarks) != 1 {
		t.Fatal(state, err)
	}
	id := state.Bookmarks[0].ID
	for _, action := range []BookmarkAction{
		{Action: "rename", ID: id, Label: " My mark "},
		{Action: "positions", Positions: []BookmarkPosition{{ID: id, Ref: ref, Line: 8, Preview: "moved line"}}},
		{Action: "remap", PreviousRef: BookmarkRef{RootID: "root", Path: "src"}, NextRef: BookmarkRef{RootID: "root", Path: "lib"}},
	} {
		state, err = mutateEditorBookmarks(state, action)
		if err != nil {
			t.Fatal(err)
		}
	}
	mark := state.Bookmarks[0]
	if mark.Label != "My mark" || mark.Line != 8 || mark.Ref.Path != "lib/file.ts" || mark.Preview != "moved line" || state.Revision != 4 {
		t.Fatal(state)
	}
	if _, err = mutateEditorBookmarks(state, BookmarkAction{Action: "toggle", Ref: BookmarkRef{RootID: "root", Path: "../outside"}, Line: 1}); err == nil {
		t.Fatal("invalid reference accepted")
	}
	state, err = mutateEditorBookmarks(state, BookmarkAction{Action: "toggle", Ref: mark.Ref, Line: mark.Line})
	if err != nil || len(state.Bookmarks) != 0 {
		t.Fatal(state, err)
	}
}

func TestEditorEventOverflowRequestsResync(t *testing.T) {
	s := testServer(t)
	ch := make(chan any, 2)
	s.editorClients["echo"] = map[chan any]bool{ch: true}
	for range 3 {
		s.editorBroadcast("echo", "change")
	}
	<-ch
	event, ok := (<-ch).(map[string]any)
	if !ok || event["type"] != "fs_resync_required" {
		t.Fatal("overflow did not request recovery", event)
	}
}
