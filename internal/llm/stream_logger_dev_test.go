//go:build dev

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDevStreamLoggerPrintsRawMessagesAndEventsInOrder(t *testing.T) {
	var output bytes.Buffer
	streamLogState.Lock()
	originalWriter := streamLogState.writer
	originalStreamID := streamLogState.nextStreamID
	originalSequence := streamLogState.nextSequence
	streamLogState.writer = &output
	streamLogState.nextStreamID = 0
	streamLogState.nextSequence = 0
	streamLogState.Unlock()
	t.Cleanup(func() {
		streamLogState.Lock()
		streamLogState.writer = originalWriter
		streamLogState.nextStreamID = originalStreamID
		streamLogState.nextSequence = originalSequence
		streamLogState.Unlock()
	})

	input := strings.Join([]string{
		`data: {"choices":[],"usage":{"completion_tokens":0}}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		``,
		`data: {"choices":`,
		``,
	}, "\n")
	events := make(chan StreamEvent, 4)
	parseStreamLogged(
		context.Background(),
		strings.NewReader(input),
		events,
		newStreamLogger("stream-1", nil),
		nil,
	)

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 log lines, got %d:\n%s", len(lines), output.String())
	}

	entries := make([]streamLogEntry, len(lines))
	for i, line := range lines {
		const prefix = "[llm-stream] "
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("line %d missing prefix: %q", i, line)
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &entries[i]); err != nil {
			t.Fatalf("decode line %d: %v", i, err)
		}
		if entries[i].Sequence != uint64(i+1) {
			t.Fatalf("line %d has sequence %d", i, entries[i].Sequence)
		}
		if entries[i].Stream != 1 || entries[i].ClientStreamID != "stream-1" {
			t.Fatalf("line %d has unexpected stream identity: %#v", i, entries[i])
		}
	}

	if entries[0].Kind != "raw" || !strings.Contains(entries[0].Data, `"usage"`) {
		t.Fatalf("expected metadata-only raw message first, got %#v", entries[0])
	}
	if entries[1].Kind != "event" || !bytes.Contains(entries[1].Event, []byte(`"type":"usage"`)) {
		t.Fatalf("expected usage event second, got %#v", entries[1])
	}
	if entries[2].Kind != "raw" || !strings.Contains(entries[2].Data, `"hello"`) {
		t.Fatalf("expected content raw message third, got %#v", entries[2])
	}
	if entries[3].Kind != "event" || !bytes.Contains(entries[3].Event, []byte(`"type":"token"`)) {
		t.Fatalf("expected token event fourth, got %#v", entries[3])
	}
	if entries[4].Kind != "raw" || entries[4].Data != `{"choices":` {
		t.Fatalf("expected malformed raw message fifth, got %#v", entries[4])
	}
	if entries[5].Kind != "event" || !bytes.Contains(entries[5].Event, []byte(`"type":"error"`)) {
		t.Fatalf("expected error event sixth, got %#v", entries[5])
	}
}
