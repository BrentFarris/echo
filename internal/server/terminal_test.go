package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/terminal"
)

func TestTerminalOwnerAPIAndDisableCleanup(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	owner := remoteLogin(t, s)
	base := "/plugins/terminal/bots/echo"
	if w := remoteRequest(s, "POST", base+"/sessions", `{"cols":80,"rows":24}`, nil); w.Code == 200 {
		t.Fatal("unauthenticated terminal allowed")
	}
	w := remoteRequest(s, "POST", base+"/sessions", `{"cols":80,"rows":24}`, owner)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var snapshot terminal.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "other", Name: "Other", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if w = remoteRequest(s, "POST", "/plugins/terminal/bots/"+bot.ID+"/sessions/"+snapshot.ID+"/input", `{"data":"bad"}`, owner); w.Code != http.StatusNotFound {
		t.Fatal("cross-bot write", w.Code, w.Body.String())
	}
	if w = remoteRequest(s, "POST", base+"/saved-commands", `{"name":"Status","command":"git status"}`, owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = remoteRequest(s, "PATCH", "/plugins/terminal", `{"enabled":false,"config":{}}`, owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = remoteRequest(s, "GET", base+"/sessions", "", owner); w.Code != http.StatusForbidden {
		t.Fatal("disabled terminal accessible", w.Code)
	}
	if w = remoteRequest(s, "PATCH", "/plugins/terminal", `{"enabled":true,"config":{}}`, owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	list, err := s.Terminal.List("echo")
	if err != nil || len(list) != 0 {
		t.Fatal("disabled shell retained", list, err)
	}
	commands, err := s.Terminal.ListSavedCommands("echo")
	if err != nil || len(commands) != 1 {
		t.Fatal("disable lost commands", commands, err)
	}
}
