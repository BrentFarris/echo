package appdata

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func TestSharedStoreUpdatesDoNotClobber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "echo.json")
	first := NewStore(path)
	second := NewStore(path)
	if first != second {
		t.Fatal("stores for the same path must share a transaction boundary")
	}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		if err := first.Update(func(file *File) error {
			file.Settings = json.RawMessage(`{"value":1}`)
			return nil
		}); err != nil {
			t.Errorf("settings update: %v", err)
		}
	}()
	go func() {
		defer wait.Done()
		if err := second.Update(func(file *File) error {
			file.Workspaces = append(file.Workspaces, Workspace{ID: "one", Name: "One"})
			return nil
		}); err != nil {
			t.Errorf("workspace update: %v", err)
		}
	}()
	wait.Wait()
	file, err := first.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Settings) == 0 || len(file.Workspaces) != 1 {
		t.Fatalf("concurrent update was lost: %+v", file)
	}
}
