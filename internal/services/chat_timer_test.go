package services

import (
	"net/http"
	"testing"
	"time"
)

func TestSystemServiceChatRecordsMessageElapsedTime(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w, `{"choices":[{"index":0,"delta":{"content":"Hi"}}]}`)
		time.Sleep(30 * time.Millisecond)
		writeSSE(t, w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	}))

	session, err := service.SendChatMessage(workspaceID, "Count time")
	if err != nil {
		t.Fatalf("send chat: %v", err)
	}
	assistant := session.Messages[len(session.Messages)-1]
	if assistant.Role != "assistant" {
		t.Fatalf("expected assistant message, got %#v", assistant)
	}
	if assistant.StartedAtMs <= 0 {
		t.Fatalf("expected StartedAtMs > 0 while streaming, got %d", assistant.StartedAtMs)
	}
	if assistant.DurationMs != 0 {
		t.Fatalf("expected DurationMs to be unset while streaming, got %d", assistant.DurationMs)
	}

	session = waitForChatIdle(t, service, workspaceID)
	assistant = session.Messages[len(session.Messages)-1]
	if assistant.Status != "complete" {
		t.Fatalf("expected complete assistant message, got %#v", assistant)
	}
	if assistant.DurationMs <= 0 {
		t.Fatalf("expected DurationMs > 0 after completion, got %d", assistant.DurationMs)
	}
	if cutoff := time.Now().Add(-24 * time.Hour).UnixMilli(); assistant.StartedAtMs < cutoff {
		t.Fatalf("startedAtMs implausibly old: %d", assistant.StartedAtMs)
	}
	if cutoff := int64(10 * time.Second / time.Millisecond); assistant.DurationMs > cutoff {
		t.Fatalf("duration implausibly large: %d", assistant.DurationMs)
	}
}

func TestSystemServiceChatRecordsDurationOnError(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		time.Sleep(15 * time.Millisecond)
		// A stop without any visible content settles the message as an error.
		writeSSE(t, w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	}))

	session, err := service.SendChatMessage(workspaceID, "Produce nothing")
	if err != nil {
		t.Fatalf("send chat: %v", err)
	}
	assistant := session.Messages[len(session.Messages)-1]
	if assistant.StartedAtMs <= 0 {
		t.Fatalf("expected StartedAtMs > 0, got %d", assistant.StartedAtMs)
	}

	session = waitForChatIdle(t, service, workspaceID)
	assistant = session.Messages[len(session.Messages)-1]
	if assistant.Status != "error" {
		t.Fatalf("expected error status, got %#v", assistant)
	}
	if assistant.DurationMs <= 0 {
		t.Fatalf("expected DurationMs > 0 after error settle, got %d", assistant.DurationMs)
	}
}
