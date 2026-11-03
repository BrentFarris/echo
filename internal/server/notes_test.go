package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestNotesOwnerAPI(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	owner := remoteLogin(t, s)
	base := "/plugins/notes/notes"
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		path := base
		if method == "PUT" || method == "DELETE" {
			path += "/missing?revision=1"
		}
		if w := remoteRequest(s, method, path, `{}`, nil); w.Code != 401 {
			t.Fatal("unauthenticated request", method, w.Code)
		}
	}
	w := remoteRequest(s, "POST", base, `{"title":"My note","markdown":"# Hello\n\n#plans @echo","mentions":[]}`, owner)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var note domain.Note
	if err := json.Unmarshal(w.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note.Revision != 1 {
		t.Fatal(note)
	}
	path := base + "/" + note.ID
	if w = remoteRequest(s, "PUT", path, `{"title":"Changed","markdown":"new","revision":1}`, owner); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = remoteRequest(s, "PUT", path, `{"title":"Stale","markdown":"old","revision":1}`, owner); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = remoteRequest(s, "DELETE", path+"?revision=1", "", owner); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = remoteRequest(s, "PUT", path, `{"title":"No revision"}`, owner); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = remoteRequest(s, "PATCH", "/plugins/notes", `{"enabled":false,"config":{}}`, owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = remoteRequest(s, "GET", base, "", owner); w.Code != 403 {
		t.Fatal("disabled API", w.Code)
	}
	if w = remoteRequest(s, "PATCH", "/plugins/notes", `{"enabled":true,"config":{}}`, owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = remoteRequest(s, "GET", base, "", owner); w.Code != 200 || !strings.Contains(w.Body.String(), "Changed") {
		t.Fatal("data not retained", w.Body.String())
	}
	if w = remoteRequest(s, "GET", "/state", "", owner); strings.Contains(w.Body.String(), `"markdown":"new"`) {
		t.Fatal("notes leaked into general state")
	}
	plugin, err := s.Plugins.Get("notes")
	if err != nil || len(plugin.Tools) != 0 || len(plugin.Capabilities) != 0 || plugin.Scope != "app" {
		t.Fatal("model access exposed", plugin, err)
	}
	if w = remoteRequest(s, "DELETE", path+"?revision=2", "", owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = remoteRequest(s, "PUT", path, `{"title":"Late","revision":2}`, owner); w.Code != 404 {
		t.Fatal("deleted note recreated", w.Code)
	}
	if w = remoteRequest(s, "GET", path, "", owner); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestNotesValidation(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	owner := remoteLogin(t, s)
	for _, body := range []string{
		`{"title":"x","markdown":"abc","mentions":[{"nodeId":"bad","botId":"echo","label":"@echo","start":0,"end":5}]}`,
		`{"title":"x","markdown":"@echo","mentions":[{"nodeId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","botId":"echo","label":"@other","start":0,"end":5}]}`,
		`{"title":"x","workspaceId":"old"}`,
		fmt.Sprintf(`{"title":%q}`, strings.Repeat("a", 121)),
		fmt.Sprintf(`{"markdown":%q}`, strings.Repeat("a", (1<<20)+1)),
	} {
		if w := remoteRequest(s, "POST", "/plugins/notes/notes", body, owner); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	body := `{"markdown":"😀 @echo","mentions":[{"nodeId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","botId":"echo","label":"@echo","start":3,"end":8}]}`
	if w := remoteRequest(s, "POST", "/plugins/notes/notes", body, owner); w.Code != 200 {
		t.Fatal("UTF-16 offsets", w.Code, w.Body.String())
	}
}

func TestNotesSwarmOwnership(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	owner := remoteLogin(t, a)
	base := "/swarm/instances/" + b.Swarm.Node().ID + "/plugins/notes/notes"
	w := remoteRequest(a, "POST", base, `{"title":"Remote only","markdown":"#private"}`, owner)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var note domain.Note
	if err := json.Unmarshal(w.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.Note(note.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.Note(note.ID); err == nil {
		t.Fatal("remote note stored on entry machine")
	}
	if w = remoteRequest(a, "GET", base, "", owner); w.Code != 200 || !strings.Contains(w.Body.String(), "Remote only") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = remoteRequest(a, "PATCH", "/swarm/instances/"+b.Swarm.Node().ID+"/plugins/notes", `{"enabled":false,"config":{}}`, owner); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = remoteRequest(a, "GET", base, "", owner); w.Code != 403 {
		t.Fatal("remote disable bypassed", w.Code, w.Body.String())
	}
}
