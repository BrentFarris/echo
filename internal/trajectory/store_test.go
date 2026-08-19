package trajectory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreAppendPageSearchAndDelete(t *testing.T) {
	store, err := New(t.TempDir(), "chat-1", "chat")
	if err != nil {
		t.Fatal(err)
	}
	step := 0
	first, err := store.Append("turn/start", "turn-1", nil, map[string]any{"origin": "send"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append("request/start", "turn-1", &step, map[string]any{"request": map[string]any{"model": "test-model"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("unexpected sequences: %d, %d", first.Sequence, second.Sequence)
	}

	page, err := store.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if page.Header.FormatVersion != FormatVersion || page.Header.ChatID != "chat-1" || len(page.Events) != 2 {
		t.Fatalf("unexpected page: %#v", page)
	}
	result, err := store.Search("test-model", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Sequence != 2 {
		t.Fatalf("unexpected search result: %#v", result)
	}

	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatalf("trajectory was not deleted: %v", err)
	}
}

func TestStoreIgnoresAndRepairsTornFinalRecord(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, "chat-2", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("turn/start", "turn-1", nil, map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(store.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"record":"event","sequence":2`); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	reloaded, err := New(root, "chat-2", "chat")
	if err != nil {
		t.Fatal(err)
	}
	page, err := reloaded.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Incomplete || len(page.Events) != 1 {
		t.Fatalf("expected one event and an incomplete marker: %#v", page)
	}
	if event, err := reloaded.Append("turn/end", "turn-1", nil, map[string]any{"status": "done"}); err != nil {
		t.Fatal(err)
	} else if event.Sequence != 2 {
		t.Fatalf("expected repaired sequence 2, got %d", event.Sequence)
	}
	finalPage, err := reloaded.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if finalPage.Incomplete || len(finalPage.Events) != 2 {
		t.Fatalf("trajectory was not repaired: %#v", finalPage)
	}
	if filepath.Ext(reloaded.Path()) != ".jsonl" {
		t.Fatalf("unexpected trajectory extension: %s", reloaded.Path())
	}
}

func TestPageUsesTurnAlignedLimit(t *testing.T) {
	store, err := New(t.TempDir(), "chat-3", "chat")
	if err != nil {
		t.Fatal(err)
	}
	for _, turnID := range []string{"turn-1", "turn-2", "turn-3"} {
		if _, err := store.Append("turn/start", turnID, nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append("turn/end", turnID, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Page(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || len(page.Events) != 4 || page.Events[0].TurnID != "turn-2" {
		t.Fatalf("page was not turn aligned: %#v", page)
	}
}
