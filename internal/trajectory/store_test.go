package trajectory

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

func TestStoreAppendBatchPreservesIndividualOrderedRecords(t *testing.T) {
	store, err := New(t.TempDir(), "chat-batch", "chat")
	if err != nil {
		t.Fatal(err)
	}
	step := 2
	firstAt := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.FixedZone("test", -5*60*60))
	secondAt := firstAt.Add(250 * time.Millisecond)
	events, err := store.AppendBatch([]AppendEntry{
		{Timestamp: firstAt, Type: "assistant/chunk", TurnID: "turn-1", Step: &step, Data: map[string]any{"part": 1}},
		{Timestamp: secondAt, Type: "assistant/chunk", TurnID: "turn-1", Step: &step, Data: map[string]any{"part": 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("unexpected batch sequences: %#v", events)
	}
	if !events[0].Timestamp.Equal(firstAt) || !events[1].Timestamp.Equal(secondAt) {
		t.Fatalf("capture timestamps were not preserved: %#v", events)
	}
	if events[0].Step == nil || *events[0].Step != step || events[1].Step == nil || *events[1].Step != step {
		t.Fatalf("assistant step was not preserved: %#v", events)
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(data, []byte{'\n'}); lines != 3 {
		t.Fatalf("expected one header and two event lines, got %d: %s", lines, data)
	}
	page, err := store.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || !bytes.Contains(page.Events[0].Data, []byte(`"part":1`)) || !bytes.Contains(page.Events[1].Data, []byte(`"part":2`)) {
		t.Fatalf("batch records did not round-trip independently: %#v", page.Events)
	}
}

func TestStoreAppendBatchValidatesBeforeWriting(t *testing.T) {
	store, err := New(t.TempDir(), "chat-batch-invalid", "chat")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendBatch([]AppendEntry{
		{Type: "turn/start", Data: map[string]any{"ok": true}},
		{Type: "assistant/chunk", Data: make(chan int)},
	})
	if err == nil {
		t.Fatal("expected an unsupported payload to reject the batch")
	}
	if _, statErr := os.Stat(store.Path()); !os.IsNotExist(statErr) {
		t.Fatalf("invalid batch wrote a partial file: %v", statErr)
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

func TestConcurrentResearchAppendsKeepContiguousSequenceOrder(t *testing.T) {
	store, err := New(t.TempDir(), "chat-research", "chat")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	const eventsPerWorker = 20
	var wg sync.WaitGroup
	errors := make(chan error, workers)
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range eventsPerWorker {
				if _, appendErr := store.Append("research/status", "turn-1", nil, map[string]any{
					"agentId": worker, "index": index,
				}); appendErr != nil {
					errors <- appendErr
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)
	for appendErr := range errors {
		t.Fatal(appendErr)
	}

	page, err := store.Page(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != workers*eventsPerWorker {
		t.Fatalf("got %d events, want %d", len(page.Events), workers*eventsPerWorker)
	}
	for index, event := range page.Events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event %d has sequence %d", index, event.Sequence)
		}
	}
}
