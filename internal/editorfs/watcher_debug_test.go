package editorfs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherDirectoryBurstThenRootRefresh(t *testing.T) {
	fs, id, root, _ := newTestService(t)
	watcher := NewWatchManager(fs, nil)
	if err := watcher.Subscribe(id); err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	for i := 0; i < 150; i++ {
		dir := filepath.Join(root, fmt.Sprintf("generated-%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { watcher.Refresh(id); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("root refresh blocked while adding directory watches")
	}
}
