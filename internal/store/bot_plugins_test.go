package store

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestBotPluginMigrationPreservesOnlyStoredCatalog(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	for id, scope := range map[string]string{"existing": "bot", "disabled": "bot", "pending": "bot", "legacy": "", "app-only": "app", "echo-only": "echo"} {
		if err = s.Put("plugins", id, domain.PluginManifest{ID: id, Scope: scope, Enabled: id != "disabled", Pending: id == "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	legacy := json.RawMessage(`{"id":"worker","handle":"worker","name":"Worker","prompt":"Keep me","roots":["unchanged"],"enableMultitasking":true,"revision":7,"futureField":{"value":42},"plugins":{"existing":false,"unknown":true}}`)
	for id, bot := range map[string]json.RawMessage{
		"worker": legacy,
		"empty":  json.RawMessage(`{"id":"empty","handle":"empty","plugins":{}}`),
		"nil":    json.RawMessage(`{"id":"nil","handle":"nil","plugins":null}`),
		"absent": json.RawMessage(`{"id":"absent","handle":"absent"}`),
		"echo":   json.RawMessage(`{"id":"echo","handle":"echo","plugins":{"existing":false}}`),
	} {
		if err = s.Put("bots", id, bot); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"worker", "empty", "nil", "absent"} {
		var bot domain.Bot
		if err = s.Get("bots", id, &bot); err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{"existing": true, "disabled": true, "pending": true, "legacy": true}
		if id == "worker" {
			want["existing"] = false
			want["unknown"] = true
		}
		if !reflect.DeepEqual(bot.Plugins, want) {
			t.Fatalf("%s selections = %v; want %v", id, bot.Plugins, want)
		}
	}
	var migrated map[string]json.RawMessage
	if err = s.Get("bots", "worker", &migrated); err != nil {
		t.Fatal(err)
	}
	var original map[string]json.RawMessage
	json.Unmarshal(legacy, &original)
	delete(migrated, "plugins")
	delete(original, "plugins")
	if !reflect.DeepEqual(migrated, original) {
		t.Fatal("migration changed unrelated bot fields", migrated)
	}
	var echo domain.Bot
	if err = s.Get("bots", "echo", &echo); err != nil || !reflect.DeepEqual(echo.Plugins, map[string]bool{"existing": false}) {
		t.Fatal("Echo selections changed", echo, err)
	}
	if err = s.Put("plugins", "new-after-upgrade", domain.PluginManifest{ID: "new-after-upgrade", Scope: "bot", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var worker domain.Bot
	if err = s.Get("bots", "worker", &worker); err != nil {
		t.Fatal(err)
	}
	if _, exists := worker.Plugins["new-after-upgrade"]; exists {
		t.Fatal("restart reapplied plugin defaults")
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatal("migration version", version, err)
	}
}

func TestBotPluginMigrationRollsBackOnFailure(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Put("plugins", "existing", domain.PluginManifest{ID: "existing", Scope: "bot"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"worker", "blocked"} {
		if err = s.Put("bots", id, domain.Bot{ID: id, Handle: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`PRAGMA user_version=4;
CREATE TRIGGER reject_bot_migration BEFORE UPDATE ON records
WHEN NEW.namespace='bots' AND NEW.id='blocked'
BEGIN SELECT RAISE(ABORT,'test migration failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(); err == nil {
		t.Fatal("expected migration failure")
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("failed migration advanced version", version, err)
	}
	for _, id := range []string{"worker", "blocked"} {
		var bot domain.Bot
		if err = s.Get("bots", id, &bot); err != nil || bot.Plugins != nil {
			t.Fatal("failed migration committed selections", id, bot, err)
		}
	}
}
