package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestBotPluginDefaultsOnlyOnCreation(t *testing.T) {
	s := testServer(t)
	installed := installTestApp(t, s, "already-installed")
	installed.Enabled = false
	if err := s.Plugins.Save(installed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"save", "api", "tool"} {
		t.Run(path, func(t *testing.T) {
			input := domain.Bot{ID: "new-" + path, Handle: "new-" + path, Plugins: map[string]bool{"shell": false}}
			var bot domain.Bot
			var err error
			switch path {
			case "save":
				bot, err = s.saveBot(input)
			case "api":
				w := scmAPI(t, s, "POST", "/bots", input)
				if w.Code != http.StatusOK {
					t.Fatal(w.Body.String())
				}
				err = json.Unmarshal(w.Body.Bytes(), &bot)
			case "tool":
				echo, _ := s.Bot("echo")
				raw := json.RawMessage(`{"handle":"new-tool","plugins":{"shell":false}}`)
				var value any
				value, err = s.tool(context.Background(), nil, domain.ExecutionScope{Bot: echo}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_save", Arguments: string(raw)}})
				if err == nil {
					bot = value.(domain.Bot)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if bot.Plugins["shell"] || !bot.Plugins[installed.ID] || !bot.Plugins["web-get"] || !bot.Plugins["jira"] {
				t.Fatal("creation defaults or override lost", bot.Plugins)
			}
			if _, exists := bot.Plugins["monaco-editor"]; exists {
				t.Fatal("app-scoped plugin received a bot selection")
			}
			if _, exists := bot.Plugins["plugin-authoring"]; exists {
				t.Fatal("Echo-scoped plugin received a named bot selection")
			}
			if !maps.Equal(input.Plugins, map[string]bool{"shell": false}) {
				t.Fatal("normalization changed the caller's map")
			}
			// Explicitly clearing selections is an update, even with a caller-supplied ID.
			bot.Plugins = map[string]bool{}
			bot, err = s.updateBot(bot, nil)
			if err != nil || len(bot.Plugins) != 0 {
				t.Fatal("update reapplied defaults", bot, err)
			}
		})
	}
}

func TestAddedPluginsStayDisabledForExistingBots(t *testing.T) {
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	late := installTestApp(t, s, "added-later")
	echo, _ := s.Bot("echo")
	if s.Plugins.Enabled(bot, late.ID) || !s.Plugins.Enabled(echo, late.ID) {
		t.Fatal("new plugin was not limited to Echo")
	}
	viewPath := "/plugins/" + late.ID + "/views/main?botId=" + bot.ID
	if w := scmAPI(t, s, "GET", viewPath, nil); w.Code != http.StatusBadRequest {
		t.Fatal("unselected view available", w.Code, w.Body.String())
	}
	for _, enabled := range []bool{false, true} {
		late.Enabled = enabled
		if err = s.Plugins.Save(late); err != nil {
			t.Fatal(err)
		}
		// Unrelated API updates can omit the entire plugin map.
		w := scmAPI(t, s, "POST", "/bots", map[string]any{"id": bot.ID, "handle": bot.Handle, "enableMultitasking": true})
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
		bot, _ = s.Bot(bot.ID)
		if !bot.Plugins["web-get"] || s.Plugins.Enabled(bot, late.ID) {
			t.Fatal("API update lost saved choices or enabled new plugin", bot)
		}
	}
	raw, _ := json.Marshal(map[string]any{"id": bot.ID, "handle": bot.Handle, "name": "Renamed"})
	if _, err = s.tool(context.Background(), nil, domain.ExecutionScope{Bot: echo}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_save", Arguments: string(raw)}}); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.Bot(bot.ID)
	if s.Plugins.Enabled(bot, late.ID) || !bot.Plugins["web-get"] {
		t.Fatal("tool update changed plugin choices")
	}
	bot.Plugins[late.ID] = true
	bot, err = s.updateBot(bot, nil)
	if err != nil || !s.Plugins.Enabled(bot, late.ID) {
		t.Fatal("explicit opt-in failed", err)
	}
	if w := scmAPI(t, s, "GET", viewPath, nil); w.Code != http.StatusOK {
		t.Fatal("selected view unavailable", w.Code, w.Body.String())
	}
	bot.Plugins[late.ID] = false
	bot, err = s.updateBot(bot, nil)
	if err != nil || s.Plugins.Enabled(bot, late.ID) {
		t.Fatal("explicit opt-out failed", err)
	}
}

func TestBotPluginUpgradeAndRestart(t *testing.T) {
	dir := t.TempDir()
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	bot, err := s.saveBot(domain.Bot{Handle: "legacy", Plugins: map[string]bool{"shell": false}})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an older build whose catalog did not contain Web Get yet.
	delete(bot.Plugins, "web-get")
	delete(bot.Plugins, "calendar")
	if err = s.Store.Put("bots", bot.ID, bot); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Delete("plugins", "web-get"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(dir, "echo2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("PRAGMA user_version=4")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err = New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	bot, _ = s.Bot(bot.ID)
	if s.Plugins.Enabled(bot, "web-get") || !s.Plugins.Enabled(bot, "calendar") || bot.Plugins["shell"] || bot.Revision != 1 {
		t.Fatal("upgrade did not preserve the old catalog choices", bot)
	}
	late := installTestApp(t, s, "after-upgrade")
	s.Close()
	s, err = New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	bot, _ = s.Bot(bot.ID)
	if s.Plugins.Enabled(bot, late.ID) || s.Plugins.Enabled(bot, "web-get") {
		t.Fatal("restart granted newly installed plugins")
	}
	fresh, err := s.saveBot(domain.Bot{Handle: "fresh"})
	if err != nil || !s.Plugins.Enabled(fresh, late.ID) || !s.Plugins.Enabled(fresh, "web-get") {
		t.Fatal("fresh bot did not receive the current defaults", fresh, err)
	}
}

func TestBotPluginScopeComparison(t *testing.T) {
	for _, id := range []string{"worker", "echo"} {
		base := domain.Bot{ID: id}
		for _, value := range []bool{false, true} {
			selected := base
			selected.Plugins = map[string]bool{"plugin": value}
			want := value == (id == "echo")
			if sameBotScope(base, selected) != want || sameBotScope(selected, base) != want {
				t.Fatal("incorrect missing-entry equivalence", id, value)
			}
		}
	}
}

func TestNamedBotPluginChangesRevokeActiveWork(t *testing.T) {
	s := testServer(t)
	bot, err := s.saveBot(domain.Bot{Handle: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	late := installTestApp(t, s, "late-permission")
	g, started := controlledModel(t, s)
	active := sendWork(t, s, g, "@worker active")
	nextInvocation(t, started, "worker", "active")
	revision := bot.Revision
	bot.Plugins[late.ID] = false
	bot, err = s.updateBot(bot, nil)
	if err != nil || bot.Revision != revision {
		t.Fatal("equivalent false entry revoked work", bot, err)
	}
	select {
	case <-active.done:
		t.Fatal("equivalent selection cancelled active work")
	default:
	}
	bot.Plugins[late.ID] = true
	bot, err = s.updateBot(bot, nil)
	if err != nil || bot.Revision != revision+1 {
		t.Fatal("opt-in did not update execution scope", bot, err)
	}
	waitDispatch(t, active)
	for _, run := range dispatchRuns(t, s, active) {
		if run.Status != "cancelled" {
			t.Fatal("scope change did not cancel active work", run)
		}
	}
	active = sendWork(t, s, g, "@worker again")
	nextInvocation(t, started, "worker", "again")
	delete(bot.Plugins, late.ID)
	bot, err = s.updateBot(bot, nil)
	if err != nil || bot.Revision != revision+2 {
		t.Fatal("removing opt-in did not revoke execution scope", bot, err)
	}
	waitDispatch(t, active)
}
