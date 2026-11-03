package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestFossilRepositoryAccessScope(t *testing.T) {
	s := testServer(t)
	parent := t.TempDir()
	checkout := filepath.Join(parent, "checkout")
	if err := os.Mkdir(checkout, 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(checkout, "one", "two")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(parent, "different name.fossil")
	if err := os.WriteFile(database, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(checkout, "_FOSSIL_"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE vvar(name TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	otherWorkspace := t.TempDir()
	bot, err := s.saveBot(domain.Bot{Handle: "fossil-access", Roots: []string{checkout, otherWorkspace}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, ref := range []string{database, "../different name.fossil"} {
		if _, err := db.Exec("INSERT OR REPLACE INTO vvar VALUES ('repository', ?)", ref); err != nil {
			t.Fatal(err)
		}
		path, scope, err := s.fossilRepositoryAccess(ctx, bot, checkout)
		if err != nil || !sameFossilPath(path, database) || !sameFossilPath(scope, parent) {
			t.Fatalf("linked database %q: %q %q %v", ref, path, scope, err)
		}
		for _, directory := range []string{"", checkout, nested} {
			roots, err := s.fossilProcessRoots(ctx, bot, directory)
			if err != nil || !reflect.DeepEqual(roots, []string{parent, otherWorkspace}) {
				t.Fatalf("Fossil process from %q: %v %v", directory, roots, err)
			}
		}
		if s.Files.Allowed(bot, database) {
			t.Fatal("metadata grant leaked into bot file tools")
		}
		if _, err := s.validateFossilRepository(ctx, bot.ID, checkout, database); err != nil {
			t.Fatal(err)
		}
		if _, err := s.validateFossilRepository(ctx, bot.ID, checkout, filepath.Join(parent, "other.fossil")); err == nil {
			t.Fatal("accepted a database other than the checkout reference")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.fossil")
	if _, err := db.Exec("UPDATE vvar SET value = ? WHERE name = 'repository'", outside); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.fossilRepositoryAccess(ctx, bot, checkout); err == nil {
		t.Fatal("checkout reference expanded access beyond its immediate parent")
	}
	if _, err := s.fossilProcessRoots(ctx, bot, nested); err == nil {
		t.Fatal("Fossil process accepted a repository beyond the checkout's parent")
	}
	if _, err := db.Exec("UPDATE vvar SET value = ? WHERE name = 'repository'", database); err != nil {
		t.Fatal(err)
	}
	// Even the metadata exception cannot grant a folder containing private state.
	privateServer := &Server{Dir: checkout, Files: s.Files}
	if _, _, err := privateServer.fossilRepositoryAccess(ctx, bot, checkout); err == nil {
		t.Fatal("Fossil parent grant included Echo private state")
	}
	bot.Roots = []string{t.TempDir()}
	if _, _, err := s.fossilRepositoryAccess(ctx, bot, checkout); err == nil {
		t.Fatal("revoked checkout retained metadata access")
	}
	if roots, err := s.fossilProcessRoots(ctx, domain.Bot{ID: "echo"}, checkout); err != nil || len(roots) != 0 {
		t.Fatalf("unbound Fossil process acquired workspace restrictions: %v %v", roots, err)
	}
}
