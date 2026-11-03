package server

import (
	"context"
	"encoding/json"
	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugDiscoveryConfigurationAndRevocation(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".vscode"), 0700)
	file := filepath.Join(root, ".vscode", "launch.json")
	os.WriteFile(file, []byte(`{// comments stay on disk
 "configurations":[{"name":"Run","type":"go","request":"launch","mode":"debug","program":"${workspaceFolder}",}],}`), 0600)
	bot, err := s.saveBot(domain.Bot{Handle: "debugger-bot", Name: "Debugger", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	setRemotePassword(t, s)
	owner := remoteLogin(t, s)
	base := "/plugins/monaco-editor/bots/" + bot.ID + "/debug/"
	if w := remoteRequest(s, "GET", base+"snapshot", "", nil); w.Code != 401 {
		t.Fatal("unauthenticated debugger access", w.Code)
	}
	m, _ := s.Plugins.Get("go")
	m.Config["languageServices"] = false
	s.Plugins.Save(m)
	if _, err = s.debugProvider(bot.ID, "go"); err != nil {
		t.Fatal("debugger depended on language services", err)
	}
	w := remoteRequest(s, "GET", base+"config", "", owner)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Config debugconfig.WorkspaceConfig `json:"config"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Config.Configurations) != 1 {
		t.Fatal(w.Body.String())
	}
	os.WriteFile(file, []byte(`{"configurations":[{"name":"Edited externally","type":"go","request":"launch","preLaunchTask":"build"}]}`), 0600)
	w = remoteRequest(s, "GET", base+"config", "", owner)
	if !strings.Contains(w.Body.String(), "Edited externally") || !strings.Contains(w.Body.String(), "preLaunchTask") {
		t.Fatal(w.Body.String())
	}
	ctx, release, err := s.leaseDebug(context.Background(), bot.ID, "go")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	m.Config["debugging"] = false
	s.Plugins.Save(m)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("plugin settings failed to revoke debugger lease")
	}
	if _, err = s.debugProvider(bot.ID, "go"); err == nil {
		t.Fatal("disabled Go debugging available")
	}
	remoteRequest(s, "PATCH", "/plugins/monaco-editor", `{"enabled":false,"config":{}}`, owner)
	if w = remoteRequest(s, "GET", base+"snapshot", "", owner); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}
