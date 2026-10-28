package store

import (
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestForkCommitRollbackRestartAndIndependentBytes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	c := domain.Chat{ID: "source"}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	if err = s.AddMessage(domain.Message{ID: "source-message", ChatID: c.ID, Role: "tool"}); err != nil {
		t.Fatal(err)
	}
	a := domain.Attachment{ID: "source-image", ChatID: c.ID, Name: "image.png", Size: 5, MediaType: "image/png"}
	if err = s.AddGeneratedAttachment(a, "source-message", []byte("image")); err != nil {
		t.Fatal(err)
	}
	copy := HistoryCopy{Chat: domain.Chat{ID: "fork", ForkedFrom: &domain.ForkOrigin{ChatID: c.ID, MessageID: "source-message"}}, Messages: []domain.Message{{ID: "new-message", ChatID: "fork", Role: "tool"}}}
	a.ID, a.ChatID = "new-image", "fork"
	path := filepath.Join(t.TempDir(), "staged")
	copy.Attachments = []HistoryAttachment{{Metadata: a, MessageID: "new-message", Path: path}}
	if _, err = s.CommitFork(c.ID, "source-message", "request", copy); err == nil {
		t.Fatal("missing staging file accepted")
	}
	if _, err = s.Chat("fork"); err == nil {
		t.Fatal("partial fork survived rollback")
	}
	if id, err := s.ForkResult(c.ID, "source-message", "request"); id != "" || err != nil {
		t.Fatal(id, err)
	}
	if err = s.StageAttachment(c.ID, "source-image", path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CommitFork(c.ID, "source-message", "request", copy); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.ForkResult(c.ID, "source-message", "request"); err != nil || id != "fork" {
		t.Fatal(id, err)
	}
	if err = s.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, data, err := s.Attachment("fork", a.ID, false); err != nil || string(data) != "image" {
		t.Fatal(err, string(data))
	}
	ms, err := s.Messages("fork")
	if err != nil || len(ms) != 1 || len(ms[0].Attachments) != 1 {
		t.Fatal(ms, err)
	}
}

func TestForkProjectionInitializationIsAtomicAndIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := domain.Chat{ID: "projection", Projection: true, ForkedFrom: &domain.ForkOrigin{ChatID: "source"}}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	copy := HistoryCopy{Chat: c, Messages: []domain.Message{{ID: "first", ChatID: c.ID}, {ID: "first", ChatID: c.ID}}}
	if err = s.SeedForkProjection(copy); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
	ms, _ := s.Messages(c.ID)
	if len(ms) != 0 {
		t.Fatal("partial history survived", ms)
	}
	if seeded, err := s.ForkProjectionSeeded(c.ID); err != nil || seeded {
		t.Fatal(seeded, err)
	}
	copy.Messages = copy.Messages[:1]
	if err = s.SeedForkProjection(copy); err != nil {
		t.Fatal(err)
	}
	if err = s.SeedForkProjection(copy); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.Messages(c.ID)
	if len(ms) != 1 {
		t.Fatal(ms)
	}
}
