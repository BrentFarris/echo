package flowlog

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestControllerWritesOrderedJSONLAndTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "echo", "echo.log")
	controller := NewController()
	if err := controller.Enable(path); err != nil {
		t.Fatal(err)
	}
	if got := controller.Path(); got != filepath.Clean(path) {
		t.Fatalf("expected controller path %q, got %q", filepath.Clean(path), got)
	}
	trace := controller.StartRequest("model-one", []byte(`{"model":"model-one","messages":[]}`))
	trace.Log(slog.LevelDebug, "llm_stream_chunk", slog.String("payload", `{"choices":[]}`))

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			controller.Log(slog.LevelInfo, "concurrent_event", slog.Int("index", index))
		}(i)
	}
	wg.Wait()
	if err := controller.Disable(); err != nil {
		t.Fatal(err)
	}

	entries := readFlowLogEntries(t, path)
	if len(entries) != 28 {
		t.Fatalf("expected 28 entries, got %d", len(entries))
	}
	for i, entry := range entries {
		if got := int(entry["sequence"].(float64)); got != i+1 {
			t.Fatalf("entry %d has sequence %d", i, got)
		}
	}

	if err := controller.Enable(path); err != nil {
		t.Fatal(err)
	}
	trace.Log(slog.LevelInfo, "stale_trace_must_not_write")
	controller.Log(slog.LevelInfo, "new_capture_marker")
	if err := controller.Disable(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "model-one") || strings.Contains(text, "stale_trace_must_not_write") {
		t.Fatalf("new capture retained old data: %s", text)
	}
	if !strings.Contains(text, "new_capture_marker") {
		t.Fatalf("new capture marker missing: %s", text)
	}
}

func TestControllerEnableFailureLeavesDisabled(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := NewController()
	if err := controller.Enable(filepath.Join(parent, "echo.log")); err == nil {
		t.Fatal("expected enable failure")
	}
	if controller.Enabled() {
		t.Fatal("controller should remain disabled")
	}
}

func readFlowLogEntries(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var entries []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode log entry: %v\n%s", err, scanner.Text())
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}
