package gitservice

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestGitCommandCancellationAndSharedLock(t *testing.T) {
	s := New(nil, nil)
	common := filepath.Join(t.TempDir(), ".git")
	ctx, cancel := s.CommandContext(context.Background(), "bot")
	defer cancel()
	other, cancelOther := s.CommandContext(context.Background(), "other")
	defer cancelOther()
	// Acquire the exact mutex used by editor repository states.
	s.mu.Lock()
	lock := s.commandLock(common)
	s.mu.Unlock()
	lock.Lock()
	called := make(chan struct{}, 1)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- s.RunCommand(ctx, "bot", common, func(context.Context) error {
			called <- struct{}{}
			return nil
		})
	}()
	<-started
	select {
	case <-done:
		t.Fatal("bot command bypassed the editor mutation lock")
	case <-time.After(20 * time.Millisecond):
	}
	s.StopWorkspaceProcesses("bot")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel the command context")
	}
	if ctx.Err() == nil || other.Err() != nil {
		t.Fatal("workspace revocation did not isolate the affected bot")
	}
	lock.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) || len(called) != 0 {
		t.Fatalf("revoked queued command ran: called=%v, error=%v", len(called), err)
	}
	fresh, cancelFresh := s.CommandContext(context.Background(), "bot")
	defer cancelFresh()
	if fresh.Err() != nil {
		t.Fatal("new invocation inherited a revoked context")
	}
}
