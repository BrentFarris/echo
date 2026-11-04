package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNativeCommandProgress(t *testing.T) {
	messages := make(chan string, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	update, stop := nativeCommandProgress(ctx, "Git status", time.Millisecond, func(message string) { messages <- message })
	defer stop()
	waitFor := func(phase string) {
		deadline := time.After(time.Second)
		for {
			select {
			case message := <-messages:
				if strings.Contains(message, "[Echo] Git status: "+phase) {
					return
				}
			case <-deadline:
				t.Fatalf("missing progress phase %q", phase)
			}
		}
	}
	for _, phase := range []string{"waiting for repository", "preparing command access", "running command", "checking changed files"} {
		update(phase)
		waitFor(phase)
	}
	cancel()
	stop() // Join before the tool result is returned; no late progress is allowed.
	for len(messages) > 0 {
		<-messages
	}
	select {
	case message := <-messages:
		t.Fatalf("progress continued after completion: %s", message)
	case <-time.After(10 * time.Millisecond):
	}
}
