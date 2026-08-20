package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

func TestStoreMissingIsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	got, err := store.Load("ws-1")
	if err != nil {
		t.Fatalf("load missing store: %v", err)
	}
	if got.Version != Version || got.WorkspaceID != "ws-1" || len(got.Turns) != 0 {
		t.Fatalf("unexpected empty transcript: %#v", got)
	}
}

func TestStoreRoundTripFullTextHistory(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	completed := time.Now().UTC()
	want := Transcript{
		Version: Version, WorkspaceID: "ws-1", Revision: 9,
		Turns: []Turn{{
			ID: "turn-1", RequestID: "request-1", UserContent: "inspect it", Model: "model-a",
			Status: "done", StartedAt: completed.Add(-time.Second), CompletedAt: &completed,
				AssistantTurns: []AssistantTurn{{
					Number: 0, Content: "finished", Reasoning: "checking", HasToolCalls: true,
					Tools: []ToolActivity{{CallID: "call-1", Name: "filesystem_list", Arguments: `{"path":"."}`, Status: "complete", Success: true, Result: `{"success":true}`}},
					Images: []MediaAttachment{{ID: "gen-img-1", Name: "ComfyUI_x.png", MediaType: "image/png", Bytes: 42, DataURL: "data:image/png;base64,AAA="}},
					Videos: []MediaAttachment{{ID: "gen-vid-1", Name: "ComfyUI_y.mp4", MediaType: "video/mp4", Bytes: 96, DataURL: "data:video/mp4;base64,BBB="}},
				}},
		}},
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "inspect it"}, {Role: llm.RoleAssistant, Content: "finished"}},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load("ws-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Revision != want.Revision || len(got.Turns) != 1 || got.Turns[0].AssistantTurns[0].Reasoning != "checking" {
		t.Fatalf("round trip lost transcript data: %#v", got)
	}
	tool := got.Turns[0].AssistantTurns[0].Tools[0]
	if tool.Arguments != `{"path":"."}` || tool.Result != `{"success":true}` {
		t.Fatalf("round trip lost tool detail: %#v", tool)
	}
	if len(got.Turns[0].AssistantTurns[0].Images) != 1 || got.Turns[0].AssistantTurns[0].Images[0].DataURL != "data:image/png;base64,AAA=" {
		t.Fatalf("round trip lost assistant image media: %#v", got.Turns[0].AssistantTurns[0].Images)
	}
	if len(got.Turns[0].AssistantTurns[0].Videos) != 1 || got.Turns[0].AssistantTurns[0].Videos[0].Name != "ComfyUI_y.mp4" {
		t.Fatalf("round trip lost assistant video media: %#v", got.Turns[0].AssistantTurns[0].Videos)
	}
	if _, err := os.Stat(store.Path() + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file remains after atomic save: %v", err)
	}
}

func TestStoreMalformedFileIsPreserved(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	const malformed = "{not-json"
	if err := os.WriteFile(store.Path(), []byte(malformed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("ws-1"); err == nil || !strings.Contains(err.Error(), "parse chat session") {
		t.Fatalf("expected parse error, got %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != malformed {
		t.Fatalf("malformed session was changed: %q", data)
	}
}
