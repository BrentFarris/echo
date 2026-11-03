//go:build linux || darwin

package credentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnixVaultPersistencePermissionsAndTampering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vault")
	ctx := context.Background()
	first := NewFileStore(dir)
	if err := first.Set(ctx, "peer/token", "durable secret"); err != nil {
		t.Fatal(err)
	}
	second := NewFileStore(dir)
	if value, err := second.Get(ctx, "peer/token"); err != nil || value != "durable secret" {
		t.Fatalf("restart %q %v", value, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, _ := entry.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe permissions: %v", info.Mode())
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "durable secret") {
			t.Fatal("plaintext secret")
		}
		if strings.HasSuffix(path, ".secret") {
			data[len(data)-1] ^= 1
			os.WriteFile(path, data, 0600)
			if _, err := second.Get(ctx, "peer/token"); err == nil {
				t.Fatal("tampered credential accepted")
			}
		}
	}
	if err := second.Set(ctx, "peer/token", "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := second.Delete(ctx, "peer/token"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Get(ctx, "peer/token"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "master.key"), 0644); err != nil {
		t.Fatal(err)
	}
	if second.Available(ctx) {
		t.Fatal("public master key accepted")
	}
}
func TestUnixVaultRejectsLinkedKey(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "key")
	os.WriteFile(outside, make([]byte, 32), 0600)
	if err := os.Symlink(outside, filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	if NewFileStore(dir).Available(context.Background()) {
		t.Fatal("linked key accepted")
	}
}
