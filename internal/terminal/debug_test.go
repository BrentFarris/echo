package terminal

import (
	"context"
	"testing"
)

func TestDebugTerminalOwnershipAndReplay(t *testing.T) {
	s, bot, _ := newTestService(t)
	defer shutdownTestService(t, s)
	normal, debug, other := newFakeBackend(), newFakeBackend(), newFakeBackend()
	backends := []*fakeBackend{normal, debug, other}
	s.SetBackendFactory(func() (Backend, error) { b := backends[0]; backends = backends[1:]; return b, nil })
	shell, err := s.Start(bot.ID, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"args": []any{"dlv", "dap"}, "cwd": bot.Roots[0], "env": map[string]any{"DEBUG_TEST": "yes"}}
	first, err := s.StartDebugDAP(context.Background(), bot.ID, "owner-a", args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.StartDebugDAP(context.Background(), bot.ID, "owner-b", args)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := s.List(bot.ID)
	if err != nil || sessions[0].ID != shell.ID {
		t.Fatal("normal terminal must remain first", sessions, err)
	}
	s.StopOwner(bot.ID, "owner-a")
	ended, err := s.Sync(bot.ID, first["sessionId"].(string), 0)
	if err != nil || ended.Status != "exited" || ended.OwnerSessionID != "owner-a" {
		t.Fatal(ended, err)
	}
	for _, id := range []string{shell.ID, second["sessionId"].(string)} {
		snapshot, err := s.Sync(bot.ID, id, 0)
		if err != nil || snapshot.Status != "running" {
			t.Fatal(snapshot, err)
		}
	}
	if _, err = s.StartDebugDAP(context.Background(), bot.ID, "bad", map[string]any{"kind": "external"}); err == nil {
		t.Fatal("external terminal accepted")
	}
}
