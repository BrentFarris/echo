package workspacefs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrackingWatcherSurvivesRefreshWithoutStartingIndex(t *testing.T) {
	service, workspace, path, root := newTestService(t)
	t.Cleanup(service.Close)
	if err := os.WriteFile(filepath.Join(path, ".gitignore"), []byte("ignored/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "ignored"), 0755); err != nil {
		t.Fatal(err)
	}
	events := make(chan WatchEvent, 32)
	watcher := NewWatchManager(service, func(event WatchEvent) {
		select {
		case events <- event:
		default:
		}
	})
	t.Cleanup(watcher.Close)
	if err := watcher.SubscribeTracking(workspace); err != nil {
		t.Fatal(err)
	}
	watcher.Refresh(workspace)
	service.index.mu.RLock()
	index := service.index.states[workspace]
	service.index.mu.RUnlock()
	if index != nil {
		t.Fatal("tracking started a recursive file index")
	}
	file := filepath.Join(path, "ignored", "external.txt")
	if err := os.WriteFile(file, []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case event := <-events:
			for _, change := range event.Changes {
				if change.Ref.RootID == root.ID && change.Ref.Path == "ignored/external.txt" {
					service.index.mu.RLock()
					afterEvent := service.index.states[workspace]
					service.index.mu.RUnlock()
					if afterEvent != nil {
						t.Fatal("an ordinary tracking event started a recursive index")
					}
					watcher.UnsubscribeTracking(workspace)
					watcher.mu.Lock()
					remaining := watcher.workspaces[workspace]
					watcher.mu.Unlock()
					if remaining != nil {
						t.Fatal("tracking watcher was not released")
					}
					return
				}
			}
		case <-timeout.C:
			t.Fatal("tracking did not watch the generically ignored directory")
		}
	}
}
