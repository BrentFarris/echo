package scmexec

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoveryBoundsAndScopes(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"repo/.git", "repo/nested/.git", "node_modules/ignored/.git", "cache/ignored/.git", "private/ignored/.git"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("cache/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := func(path string) bool { _, err := os.Stat(filepath.Join(path, ".git")); return err == nil }
	allowed := func(path string) bool { return path != filepath.Join(root, "private") }
	found, err := Discover(context.Background(), root, marker, allowed)
	expected := []string{filepath.Join(root, "repo"), filepath.Join(root, "repo", "nested")}
	if err != nil || !reflect.DeepEqual(found, expected) {
		t.Fatalf("discover: %v %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, root, marker); err != context.Canceled {
		t.Fatal("cancelled scan was not reported", err)
	}
}
