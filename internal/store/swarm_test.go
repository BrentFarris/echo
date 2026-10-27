package store

import (
	"errors"
	"github.com/brent/echo/internal/domain"
	"testing"
)

func TestSwarmMigrationCheckpointAndDeletion(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Chat{ID: "legacy-local-id"}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("ALTER TABLE swarm_checkpoints DROP COLUMN retired; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := domain.Message{ID: "replica", ChatID: c.ID, Content: "newest", Status: "completed"}
	if err = s.MergeReplica(c.ID, "linux:dispatch", "epoch-1", 9, []domain.Message{m}, nil); err != nil {
		t.Fatal(err)
	}
	m.Content = "stale"
	if err = s.MergeReplica(c.ID, "linux:dispatch", "epoch-1", 8, []domain.Message{m}, nil); !errors.Is(err, ErrStaleReplica) {
		t.Fatal(err)
	}
	ms, _ := s.Messages(c.ID)
	if len(ms) != 1 || ms[0].Content != "newest" {
		t.Fatal(ms)
	}
	m.Content = "restart interrupted"
	if err = s.MergeReplica(c.ID, "linux:dispatch", "epoch-2", 1, []domain.Message{m}, nil); err != nil {
		t.Fatal(err)
	}
	m.Content = "late pre-restart snapshot"
	if err = s.MergeReplica(c.ID, "linux:dispatch", "epoch-1", 100, []domain.Message{m}, nil); !errors.Is(err, ErrStaleReplica) {
		t.Fatal("retired epoch accepted", err)
	}
	ms, _ = s.Messages(c.ID)
	if len(ms) != 1 || ms[0].Content != "restart interrupted" {
		t.Fatal("late epoch replaced recovery", ms)
	}
	if err = s.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.MergeReplica(c.ID, "linux:dispatch", "epoch-2", 2, []domain.Message{m}, nil); err == nil {
		t.Fatal("late delivery recreated chat")
	}
	if err = s.CreateChat(c); err == nil {
		t.Fatal("tombstone lost")
	}
}
