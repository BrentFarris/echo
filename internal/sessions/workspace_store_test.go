package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestWorkspaceStoreStartsEmptyAndDoesNotReadLegacySession(t *testing.T) {
	root := t.TempDir()
	legacy := NewStore(root)
	if err := legacy.Save(Transcript{Version: Version, WorkspaceID: "ws-1", Turns: []Turn{{ID: "legacy"}}, Messages: []llm.Message{}}); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaceStore(root)
	workspace, err := store.Load("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Tabs) != 0 || workspace.ActiveChatID != "" {
		t.Fatalf("legacy session was unexpectedly migrated: %#v", workspace)
	}
	if _, err := os.Stat(legacy.Path()); err != nil {
		t.Fatalf("legacy session was changed: %v", err)
	}
	autosavePath := filepath.Join(root, ".echo", "autosave.json")
	const autosave = `{"legacy":true}`
	if err := os.WriteFile(autosavePath, []byte(autosave), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err = store.Load("ws-1")
	if err != nil || len(workspace.Tabs) != 0 {
		t.Fatalf("OLD autosave was unexpectedly migrated: %#v (%v)", workspace, err)
	}
	data, err := os.ReadFile(autosavePath)
	if err != nil || string(data) != autosave {
		t.Fatalf("OLD autosave was changed: %q (%v)", data, err)
	}
}

func TestWorkspaceStoreRoundTripPreservesOrderAndActiveTab(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir())
	want := ChatWorkspace{
		Version: WorkspaceVersion, WorkspaceID: "ws-1", Revision: 7, ActiveChatID: "chat-b",
		Tabs: []TabTranscript{
			{ChatID: "chat-a", Preview: "first prompt", Turns: []Turn{}, Messages: []llm.Message{}},
			{ChatID: "chat-b", Preview: "latest prompt", Revision: 3, Turns: []Turn{}, Messages: []llm.Message{{Role: llm.RoleUser, Content: "latest prompt"}}},
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveChatID != "chat-b" || len(got.Tabs) != 2 || got.Tabs[0].ChatID != "chat-a" || got.Tabs[1].Preview != "latest prompt" {
		t.Fatalf("unexpected workspace round trip: %#v", got)
	}
}

func TestWorkspaceStoreSerializesConcurrentUpdates(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir())
	initial := ChatWorkspace{
		Version: WorkspaceVersion, WorkspaceID: "ws-1", ActiveChatID: "chat-a",
		Tabs: []TabTranscript{{ChatID: "chat-a"}, {ChatID: "chat-b"}},
	}
	if err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, chatID := range []string{"chat-a", "chat-b"} {
		chatID := chatID
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Update("ws-1", func(workspace *ChatWorkspace) error {
				for index := range workspace.Tabs {
					if workspace.Tabs[index].ChatID == chatID {
						workspace.Tabs[index].Preview = "updated " + chatID
					}
				}
				return nil
			})
			if err != nil {
				t.Errorf("update %s: %v", chatID, err)
			}
		}()
	}
	wg.Wait()
	got, err := store.Load("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tabs[0].Preview != "updated chat-a" || got.Tabs[1].Preview != "updated chat-b" {
		t.Fatalf("concurrent update was lost: %#v", got.Tabs)
	}
}

func TestWorkspaceStoreMalformedFileIsPreserved(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaceStore(root)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	const malformed = "{not-json"
	if err := os.WriteFile(store.Path(), []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("ws-1"); err == nil || !strings.Contains(err.Error(), "parse chat workspace") {
		t.Fatalf("expected parse error, got %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != malformed {
		t.Fatalf("malformed workspace was changed: %q", data)
	}
}
