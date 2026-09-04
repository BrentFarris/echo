package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
			{
				ChatID: "chat-b", Preview: "latest prompt", Revision: 3,
				Turns: []Turn{{ID: "turn-b", UserContent: "latest prompt", UserMessageIndex: 2}},
				Messages: []llm.Message{
					{Role: llm.RoleUser, Content: "opening"},
					{Role: llm.RoleAssistant, Content: "older answer"},
					{Role: llm.RoleUser, Content: "latest prompt"},
				},
				ContextCheckpoint: &ContextCheckpoint{
					Summary: "## Goal\nContinue", ProtectedHeadIndex: 0, CompactedThrough: 2,
					Endpoint: "http://localhost/v1", Model: "model", BeforeTokens: 4000, AfterTokens: 1200,
				},
			},
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
	if got.Tabs[1].ContextCheckpoint == nil || got.Tabs[1].ContextCheckpoint.Summary != "## Goal\nContinue" || got.Tabs[1].Turns[0].UserMessageIndex != 2 || len(got.Tabs[1].Messages) != 3 {
		t.Fatalf("raw history or context checkpoint changed during round trip: %#v", got.Tabs[1])
	}
}

func TestWorkspaceStoreRebindsMachineLocalWorkspaceID(t *testing.T) {
	store := NewWorkspaceStore(t.TempDir())
	want := ChatWorkspace{
		Version: WorkspaceVersion, WorkspaceID: "ws-old", Revision: 7, ActiveChatID: "chat-a",
		Tabs: []TabTranscript{{ChatID: "chat-a", Preview: "preserved", Turns: []Turn{}, Messages: []llm.Message{}}},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("ws-new")
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "ws-new" || got.Revision != want.Revision || len(got.Tabs) != 1 || got.Tabs[0].Preview != "preserved" {
		t.Fatalf("chat state was not preserved during rebind: %#v", got)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"workspaceId": "ws-old"`) || !strings.Contains(string(data), `"workspaceId": "ws-new"`) {
		t.Fatalf("rebound workspace id was not persisted: %s", data)
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

func TestWorkspaceStoreMigratesVersionOneAndPreservesNormalTabs(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaceStore(root)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "version": 1,
  "workspaceId": "ws-1",
  "revision": 4,
  "activeChatId": "chat-a",
  "tabs": [{"chatId":"chat-a","preview":"preserved","revision":2,"turns":[],"messages":[]}]
}`
	if err := os.WriteFile(store.Path(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace, err := store.Load("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Version != WorkspaceVersion || workspace.ActiveChatID != "chat-a" || len(workspace.Tabs) != 1 || workspace.Tabs[0].Preview != "preserved" || workspace.CodeChat != nil {
		t.Fatalf("unexpected migrated workspace: %#v", workspace)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 4`) {
		t.Fatalf("migration was not persisted: %s", data)
	}
}

func TestWorkspaceStoreRoundTripsGoalHistoryAndPausesActiveGoalOnLoad(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaceStore(root)
	now := time.Now().UTC().Add(-2 * time.Second)
	want := ChatWorkspace{
		Version: WorkspaceVersion, WorkspaceID: "ws-1", ActiveChatID: "chat-a",
		Tabs: []TabTranscript{{
			ChatID: "chat-a", Turns: []Turn{}, Messages: []llm.Message{}, CurrentGoalID: "goal-2",
			Goals: []GoalState{
				{ID: "goal-1", Objective: "Finished", Status: GoalStatusCompleted, CreatedAt: now, UpdatedAt: now},
				{
					ID: "goal-2", Objective: "Keep going", Status: GoalStatusActive, Model: "model-a",
					CreatedAt: now, UpdatedAt: now, ActiveSince: &now,
					PendingStatus: GoalStatusCompleted, PendingOutcome: "Awaiting the final summary.",
					PendingSteering: []GoalSteering{{ID: "steer-1", Content: "Use the new API", CreatedAt: now}},
				},
			},
		}},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	inspected, err := store.Load("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Tabs[0].Goals[1].Status != GoalStatusActive {
		t.Fatalf("ordinary reads must not interrupt a live goal: %#v", inspected.Tabs[0].Goals[1])
	}
	if _, err := store.Update("ws-1", func(workspace *ChatWorkspace) error {
		workspace.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadRecoveringGoals("ws-1")
	if err != nil {
		t.Fatal(err)
	}
	goal := got.Tabs[0].Goals[1]
	if goal.Status != GoalStatusPaused || goal.ActiveSince != nil || goal.ActiveSeconds < 1 || len(goal.PendingSteering) != 1 || goal.PendingStatus != "" || goal.PendingOutcome != "" {
		t.Fatalf("active goal was not restored paused with its queue: %#v", goal)
	}
	if !strings.Contains(goal.LastError, "restarted") || got.Tabs[0].Goals[0].Status != GoalStatusCompleted {
		t.Fatalf("goal history was not preserved: %#v", got.Tabs[0].Goals)
	}
}
