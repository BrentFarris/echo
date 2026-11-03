package store

import (
	"encoding/json"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestEndpointTimeoutDefaultsMigration(t *testing.T) {
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
	for id, raw := range map[string]string{
		"defaults": `{"id":"defaults","requestTimeoutSeconds":600,"streamIdleTimeoutSeconds":120,"credential":"keep","futureField":{"value":42}}`,
		"custom":   `{"id":"custom","requestTimeoutSeconds":3600,"streamIdleTimeoutSeconds":900}`,
		"disabled": `{"id":"disabled","streamIdleTimeoutSeconds":-1}`,
	} {
		if err = s.Put("endpoints", id, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(); err != nil {
		t.Fatal(err)
	}
	var defaults map[string]json.RawMessage
	if err = s.Get("endpoints", "defaults", &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults["requestTimeoutSeconds"] != nil || defaults["streamIdleTimeoutSeconds"] != nil || string(defaults["credential"]) != `"keep"` || string(defaults["futureField"]) != `{"value":42}` {
		t.Fatal("migration lost fields or retained defaults", defaults)
	}
	for id, want := range map[string][2]int{"custom": {3600, 900}, "disabled": {0, -1}} {
		var endpoint domain.Endpoint
		if err = s.Get("endpoints", id, &endpoint); err != nil || endpoint.RequestTimeoutSeconds != want[0] || endpoint.StreamIdleTimeoutSeconds != want[1] {
			t.Fatal("custom timeout changed", endpoint, err)
		}
	}
	// Once upgraded, deliberately saving the old values must survive a restart.
	if err = s.Put("endpoints", "defaults", domain.Endpoint{ID: "defaults", RequestTimeoutSeconds: 600, StreamIdleTimeoutSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var endpoint domain.Endpoint
	if err = s.Get("endpoints", "defaults", &endpoint); err != nil || endpoint.RequestTimeoutSeconds != 600 || endpoint.StreamIdleTimeoutSeconds != 120 {
		t.Fatal("migration reapplied on restart", endpoint, err)
	}
}

func TestEndpointTimeoutMigrationRollsBackOnFailure(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Put("endpoints", "legacy", domain.Endpoint{ID: "legacy", RequestTimeoutSeconds: 600, StreamIdleTimeoutSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA user_version=5;
CREATE TRIGGER reject_timeout_migration BEFORE UPDATE ON records
WHEN NEW.namespace='endpoints' AND json_extract(OLD.data,'$.streamIdleTimeoutSeconds')=120
 AND json_extract(NEW.data,'$.streamIdleTimeoutSeconds') IS NULL
BEGIN SELECT RAISE(ABORT,'test migration failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(); err == nil {
		t.Fatal("expected migration failure")
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatal("failed migration advanced version", version, err)
	}
	var endpoint domain.Endpoint
	if err = s.Get("endpoints", "legacy", &endpoint); err != nil || endpoint.RequestTimeoutSeconds != 600 || endpoint.StreamIdleTimeoutSeconds != 120 {
		t.Fatal("failed migration changed endpoint", endpoint, err)
	}
}
