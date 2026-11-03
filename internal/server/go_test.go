package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
)

func TestGoPluginContractAndSettings(t *testing.T) {
	s := testServer(t)
	bot, _ := s.Bot("echo")
	shell, _ := s.Plugins.Get("shell")
	shell.Enabled = false
	s.Plugins.Save(shell)
	m, err := s.Plugins.Get("go")
	if err != nil || !m.Enabled || !m.Builtin || m.Scope != "bot" {
		t.Fatal(m, err)
	}
	found := false
	for _, tool := range s.Plugins.Tools(bot) {
		if tool.Name == "go_command" {
			found = true
		}
	}
	if !found {
		t.Fatal("Go depends on Shell")
	}
	bot.Plugins = map[string]bool{"go": false}
	if _, err = s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: bot}, "go_command", json.RawMessage(`{"command":"version"}`)); err == nil {
		t.Fatal("disabled Go ran")
	}
	for _, config := range []map[string]any{{"internet": true}, {"privateNetwork": true}, {"executable": "/tools/go"}, {"toolchains": []string{t.TempDir()}}} {
		if _, err := s.goConfig(m, config, false); err == nil {
			t.Fatal("bot expanded grants", config)
		}
	}
	r := scmAPI(t, s, "PATCH", "/plugins/go", map[string]any{"enabled": true, "config": map[string]any{"internet": true, "proxy": "https://proxy.golang.org,direct"}})
	if r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	m, _ = s.Plugins.Get("go")
	if m.Config["internet"] != true {
		t.Fatal(m.Config)
	}
	if _, err = s.goConfig(m, map[string]any{"internet": false}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.goConfig(m, map[string]any{"proxy": "https://user:secret@proxy.example"}, true); err == nil {
		t.Fatal("accepted unprotected credential")
	}
	// Both a counterfeit manifest and a different built-in must be rejected.
	for _, fake := range []domain.PluginManifest{{ID: "go"}, {ID: "shell", Builtin: true}} {
		if _, err = s.runGo(context.Background(), domain.ExecutionScope{Bot: bot, PluginID: fake.ID}, fake, map[string]any{"command": "version"}); err == nil {
			t.Fatal("foreign plugin invoked Go")
		}
	}
	// Reloading built-in definitions preserves saved owner settings.
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	host, err := plugins.New(context.Background(), s.Store, t.TempDir(), guest, s.pluginCall)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close(context.Background())
	saved, err := host.Get("go")
	if err != nil || saved.Config["internet"] != true || saved.Config["proxy"] != m.Config["proxy"] {
		t.Fatal(saved, err)
	}
}
