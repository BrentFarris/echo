//go:build dev

package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/brent/echo/internal/flowlog"
)

type streamLogger struct {
	id             uint64
	clientStreamID string
	runtime        *runtimeStreamLogger
}

type streamLogEntry struct {
	Sequence       uint64          `json:"sequence"`
	Stream         uint64          `json:"stream"`
	ClientStreamID string          `json:"clientStreamId"`
	Kind           string          `json:"kind"`
	Data           string          `json:"data,omitempty"`
	Event          json.RawMessage `json:"event,omitempty"`
}

var streamLogState = struct {
	sync.Mutex
	writer       io.Writer
	nextStreamID uint64
	nextSequence uint64
}{
	writer: os.Stdout,
}

func newStreamLogger(clientStreamID string, trace *flowlog.RequestTrace) *streamLogger {
	streamLogState.Lock()
	defer streamLogState.Unlock()
	streamLogState.nextStreamID++
	return &streamLogger{
		id:             streamLogState.nextStreamID,
		clientStreamID: clientStreamID,
		runtime:        newRuntimeStreamLogger(trace),
	}
}

func (l *streamLogger) raw(data string) {
	if l.runtime != nil {
		l.runtime.raw(data)
	}
	l.write(streamLogEntry{
		Kind: "raw",
		Data: data,
	})
}

func (l *streamLogger) event(event StreamEvent) {
	if l.runtime != nil {
		l.runtime.event(event)
	}
	data, err := json.Marshal(event)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"type":"log_error","error":%q}`, err.Error()))
	}
	l.write(streamLogEntry{
		Kind:  "event",
		Event: data,
	})
}

func (l *streamLogger) finish() {
	if l.runtime != nil {
		l.runtime.finishResponse()
	}
}

func (l *streamLogger) write(entry streamLogEntry) {
	streamLogState.Lock()
	defer streamLogState.Unlock()

	streamLogState.nextSequence++
	entry.Sequence = streamLogState.nextSequence
	entry.Stream = l.id
	entry.ClientStreamID = l.clientStreamID
	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(streamLogState.writer, "[llm-stream] unable to encode log entry: %v\n", err)
		return
	}
	fmt.Fprintf(streamLogState.writer, "[llm-stream] %s\n", data)
}
