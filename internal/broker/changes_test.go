package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestFileSnapshotsDetectChangesWithoutIndexingPrivateOrMetadataFiles(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	for _, directory := range []string{private, filepath.Join(root, ".git"), filepath.Join(root, "node_modules")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("edited.txt", "before")
	write("deleted.txt", "old")
	write("unchanged.txt", "same")
	f := Files{Private: private}
	bot := domain.Bot{Roots: []string{root}}
	before := f.SnapshotFiles(context.Background(), bot, []string{root})
	write("edited.txt", "after")
	write("created, 日本語.txt", "new")
	write("unchanged.txt", "same")
	write("private/secret.txt", "secret")
	write(".git/config", "metadata")
	write("node_modules/cache.txt", "dependency")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	after := f.SnapshotFiles(context.Background(), bot, []string{root})
	changes := DiffFiles(bot, before, after)
	if !before.Complete || !after.Complete || len(changes) != 3 {
		t.Fatalf("unexpected snapshots: %+v %+v %+v", before, after, changes)
	}
	for _, change := range changes {
		switch filepath.Base(change.Path) {
		case "edited.txt":
			if change.Operation != "edited" {
				t.Fatal(change)
			}
		case "deleted.txt":
			if change.Operation != "deleted" {
				t.Fatal(change)
			}
		case "created, 日本語.txt":
			if change.Operation != "created" {
				t.Fatal(change)
			}
		default:
			t.Fatalf("unexpected tracked file: %+v", change)
		}
	}
}

func TestIncompleteFileScansDoNotInventCreationsOrDeletions(t *testing.T) {
	bot := domain.Bot{}
	before := FileSnapshot{Files: map[string]string{"a": "old", "b": "old"}, Complete: true}
	after := FileSnapshot{Files: map[string]string{"a": "new"}, Complete: false}
	changes := DiffFiles(bot, before, after)
	if len(changes) != 1 || changes[0].Path != "a" || changes[0].Operation != "edited" {
		t.Fatal(changes)
	}
	changes = DiffFiles(bot, after, before)
	if len(changes) != 1 {
		t.Fatal(changes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := Files{}
	if f.SnapshotFiles(ctx, bot, []string{t.TempDir()}).Complete {
		t.Fatal("cancelled scan reported complete")
	}
}

func TestRevealChecksScopeAndFindsContainingFolder(t *testing.T) {
	root := t.TempDir()
	private := t.TempDir()
	path := filepath.Join(root, "file, 日本語.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	f := Files{Private: private}
	bot := domain.Bot{Roots: []string{root}}
	target, err := f.RevealTarget(bot, path)
	if err != nil || !target.Select || target.Path != path {
		t.Fatalf("%+v %v", target, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target, err = f.RevealTarget(bot, filepath.Join(root, "removed", "nested", "file.txt"))
	if err != nil || target.Select || target.Path != root {
		t.Fatalf("%+v %v", target, err)
	}
	for _, invalid := range []string{private, filepath.Join(filepath.Dir(root), "sibling.txt"), "relative.txt"} {
		if _, err := f.RevealTarget(bot, invalid); err == nil {
			t.Fatalf("revealed denied path %s", invalid)
		}
	}
	if _, err := f.RevealTarget(domain.Bot{}, private); err == nil {
		t.Fatal("revealed private state")
	}
	t.Run("symlink escape", func(t *testing.T) {
		link := filepath.Join(root, "outside")
		if err := os.Symlink(private, link); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		if _, err := f.RevealTarget(bot, link); err == nil {
			t.Fatal("revealed a symlink escape")
		}
		if _, err := f.RevealTarget(bot, filepath.Join(link, "deleted.txt")); err == nil {
			t.Fatal("revealed a deleted path through an escape")
		}
	})
}
