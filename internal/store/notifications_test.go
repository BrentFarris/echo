package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func TestNotificationClaimsRecoveryAndResolution(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Chat{ID: "chat", Title: "Work"}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	d := NotificationDevice{ID: "device", Session: "session"}
	if err = s.SaveNotificationDevice(d); err != nil {
		t.Fatal(err)
	}
	m := domain.Message{ID: "final", ChatID: c.ID, Role: "assistant", Status: "running"}
	if err = s.AddMessage(m); err != nil {
		t.Fatal(err)
	}
	a := domain.ChatAlert{ID: "home:completion:final", HomeNodeID: "home", ChatID: c.ID, MessageID: m.ID, Kind: "completion", Active: true, OccurredAt: time.Now()}
	m.Status = "completed"
	if err = s.FinishMessage(m, a); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishMessage(m, a); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimAlert(d.ID, a.ID, "push", true); err != nil || ok {
		t.Fatal("push bypassed grace period", ok, err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.ClaimAlert(d.ID, a.ID, "live", false)
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("got %d live claims", claims.Load())
	}
	if ok, _ := s.ClaimAlert(d.ID, a.ID, "push", false); ok {
		t.Fatal("push duplicated live alert")
	}
	second := NotificationDevice{ID: "phone", Session: "phone-session"}
	if err = s.SaveNotificationDevice(second); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PendingAlerts(second.ID); len(pending) != 0 {
		t.Fatal("new device replayed old completions", pending)
	}
	q := domain.Message{ID: "question", ChatID: c.ID, Role: "tool", Status: "running"}
	_ = s.AddMessage(q)
	qa := domain.ChatAlert{ID: "home:question:question", HomeNodeID: "home", ChatID: c.ID, MessageID: q.ID, Kind: "question", Active: true, OccurredAt: time.Now()}
	if err = s.AddQuestionAlert(qa); err != nil {
		t.Fatal(err)
	}
	third := NotificationDevice{ID: "tablet", Session: "third"}
	_ = s.SaveNotificationDevice(third)
	if pending, _ := s.PendingAlerts(third.ID); len(pending) != 1 {
		t.Fatal("pending question was not restored", pending)
	}
	feed, _ := s.AlertFeed(0, "home")
	cursor := feed[len(feed)-1].Sequence
	q.Status = "completed"
	if err = s.FinishMessage(q); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PendingAlerts(third.ID); len(pending) != 0 {
		t.Fatal("answered question still pending", pending)
	}
	feed, _ = s.AlertFeed(cursor, "home")
	if len(feed) != 1 || feed[0].Active {
		t.Fatal("resolution was not journaled", feed)
	}
}

func TestNotificationProjectionDeletionAndExpiry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.CreateChat(domain.Chat{ID: "projection", Projection: true})
	_ = s.CreateChat(domain.Chat{ID: "home"})
	d := NotificationDevice{ID: "d", Session: "s"}
	_ = s.SaveNotificationDevice(d)
	for _, chat := range []string{"projection", "home"} {
		m := domain.Message{ID: chat, ChatID: chat, Role: "assistant", Status: "completed"}
		_ = s.AddMessage(m)
		a := domain.ChatAlert{ID: chat, ChatID: chat, MessageID: m.ID, Kind: "completion", HomeNodeID: "node", OccurredAt: time.Now(), Active: true}
		if err = s.FinishMessage(m, a); err != nil {
			t.Fatal(err)
		}
	}
	pending, _ := s.PendingAlerts(d.ID)
	if len(pending) != 1 || pending[0].ChatID != "home" {
		t.Fatal(pending)
	}
	if err = s.DeleteChat("home"); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingAlerts(d.ID)
	if len(pending) != 0 {
		t.Fatal("deleted chat still delivers", pending)
	}
	_ = s.PutAlert(domain.ChatAlert{ID: "expired", HomeNodeID: "remote", Kind: "completion", OccurredAt: time.Now().Add(-25 * time.Hour), Active: true})
	if ok, _ := s.ClaimAlert(d.ID, "expired", "live", false); ok {
		t.Fatal("expired alert claimed")
	}
	_ = s.DeleteNotificationDevice(d.ID, "wrong")
	if _, err = s.NotificationDevice(d.ID, d.Session); err != nil {
		t.Fatal("cross-session delete", err)
	}
	_ = s.DeleteNotificationDevice(d.ID, d.Session)
	if _, err = s.NotificationDevice(d.ID, d.Session); err == nil {
		t.Fatal("device retained")
	}
}

func TestNotificationPushCommitRejectsLateLiveAndRetries(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.SaveNotificationDevice(NotificationDevice{ID: "d", Session: "s"})
	a := domain.ChatAlert{ID: "a", Kind: "completion", HomeNodeID: "n", Active: true, OccurredAt: time.Now()}
	_ = s.PutAlert(a)
	_, _ = s.db.Exec("UPDATE notification_deliveries SET next=0")
	if ok, err := s.ClaimAlert("d", "a", "push", true); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := s.ClaimAlert("d", "a", "live", false); ok {
		t.Fatal("late live claim accepted")
	}
	_ = s.FinishPush("d", "a", time.Now().Add(-time.Second))
	retries, err := s.RetryPush("d")
	if err != nil || len(retries) != 1 || retries[0].Attempts != 1 {
		t.Fatal(retries, err)
	}
	_ = s.BeginPushRetry("d", "a")
	_ = s.FinishPush("d", "a", time.Time{})
	retries, _ = s.RetryPush("d")
	if len(retries) != 0 {
		t.Fatal(retries)
	}
}

func TestNotificationReplicaDoesNotReplayExistingHistory(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.CreateChat(domain.Chat{ID: "home"})
	m := domain.Message{ID: "old", ChatID: "home", Role: "assistant", Status: "completed", CreatedAt: time.Now().Add(-time.Hour)}
	_ = s.AddMessage(m)
	a := domain.ChatAlert{ID: "old-alert", ChatID: m.ChatID, MessageID: m.ID, HomeNodeID: "node", Kind: "completion", Active: true, OccurredAt: time.Now()}
	if err = s.MergeReplica("home", "worker", "epoch", 1, []domain.Message{m}, nil, a); err != nil {
		t.Fatal(err)
	}
	feed, err := s.AlertFeed(0, "")
	if err != nil || len(feed) != 0 {
		t.Fatal("existing completed history replayed", feed, err)
	}
	// A new remote reply can have an earlier timestamp due to clock skew. Its
	// persisted message transition, rather than wall-clock comparison, is decisive.
	m.ID = "new"
	a.ID = "new-alert"
	a.MessageID = m.ID
	if err = s.MergeReplica("home", "worker", "epoch", 2, []domain.Message{m}, nil, a); err != nil {
		t.Fatal(err)
	}
	feed, err = s.AlertFeed(0, "")
	if err != nil || len(feed) != 1 {
		t.Fatal(feed, err)
	}
}
