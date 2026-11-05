package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func retainedFixture(t *testing.T, s *Store, id string) domain.Chat {
	t.Helper()
	c := domain.Chat{ID: id, Title: "Retained conversation", Bots: map[string]string{"echo": "Echo"}, CreatedAt: domain.Now(), UpdatedAt: domain.Now()}
	if err := s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	m := domain.Message{ID: id + "-message", ChatID: id, Role: "assistant", Status: "completed", Content: "saved answer"}
	if err := s.AddMessage(m); err != nil {
		t.Fatal(err)
	}
	a := domain.Attachment{ID: id + "-media", ChatID: id, Name: "answer.txt", Size: 5, MediaType: "text/plain"}
	if err := s.AddGeneratedAttachment(a, m.ID, []byte("saved")); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBlankChatDeletionPurgesPendingUploads(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := domain.Chat{ID: "blank", Title: "New chat", Bots: map[string]string{"echo": "Echo"}}
	if err = s.CreateChat(c); err != nil {
		t.Fatal(err)
	}
	if err = s.AddAttachment(domain.Attachment{ID: "draft", ChatID: c.ID, Size: 1}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.TrashChat(c.ID, domain.Now())
	if err != nil || deleted != nil {
		t.Fatal(deleted, err)
	}
	if !s.ChatDeleted(c.ID) {
		t.Fatal("blank chat has no permanent tombstone")
	}
	if chats, err := s.RetainedChats(); err != nil || len(chats) != 0 {
		t.Fatal(chats, err)
	}
	if _, err = s.RestoreChat(c.ID, domain.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("blank chat remained restorable", err)
	}
	var uploads int
	if err = s.db.QueryRow("SELECT count(*) FROM attachments WHERE chat_id=?", c.ID).Scan(&uploads); err != nil || uploads != 0 {
		t.Fatal(uploads, err)
	}
}

func TestBlankChatDeletionSerializesWithFirstMessage(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 12; i++ {
		c := domain.Chat{ID: domain.ID()}
		if err = s.CreateChat(c); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var messageErr, deleteErr error
		var retained *domain.Chat
		wg.Add(2)
		go func() {
			defer wg.Done()
			messageErr = s.AddMessage(domain.Message{ID: domain.ID(), ChatID: c.ID, Role: "user", Content: "first message"})
		}()
		go func() { defer wg.Done(); retained, deleteErr = s.TrashChat(c.ID, domain.Now()) }()
		wg.Wait()
		if deleteErr != nil {
			t.Fatal(deleteErr)
		}
		if messageErr == nil {
			if retained == nil {
				t.Fatal("accepted first message was permanently deleted")
			}
			messages, err := s.Messages(c.ID)
			if err != nil || len(messages) != 1 || messages[0].Content != "first message" {
				t.Fatal(messages, err)
			}
		} else if retained != nil || !s.ChatDeleted(c.ID) {
			t.Fatal("blank chat was retained", retained)
		}
	}
}

func TestBlankRetainedChatsAreCleanedUpBeforeExpiry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := domain.Now()
	for _, c := range []domain.Chat{{ID: "legacy-blank", DeletedAt: &now}, {ID: "active-blank"}, {ID: "restored-blank", DeletedAt: &now}} {
		if err = s.CreateChat(c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.RestoreChat("restored-blank", now); err != nil {
		t.Fatal(err)
	}
	if count, err := s.PurgeExpiredChats(now); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if !s.ChatDeleted("legacy-blank") {
		t.Fatal("previously retained blank chat survived cleanup")
	}
	for _, id := range []string{"active-blank", "restored-blank"} {
		if _, err = s.Chat(id); err != nil {
			t.Fatal("cleanup removed active chat", id, err)
		}
	}
}

func TestChatRetentionRestoreAndPermanentRemoval(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := retainedFixture(t, s, "chat")
	now := domain.Now()
	_ = s.Put("external-sessions", "session", map[string]string{"chatId": c.ID})
	deleted, err := s.TrashChat(c.ID, now)
	if err != nil || deleted.DeletedAt == nil || !deleted.ExpiresAt.Equal(now.Add(ChatRetention)) {
		t.Fatal(deleted, err)
	}
	again, err := s.TrashChat(c.ID, now.Add(time.Hour))
	if err != nil || !again.DeletedAt.Equal(now) {
		t.Fatal("deletion extended retention", again, err)
	}
	if _, err = s.Chat(c.ID); !errors.Is(err, ErrChatDeleted) {
		t.Fatal(err)
	}
	if active, _ := s.Chats(); len(active) != 0 {
		t.Fatal(active)
	}
	if retained, _ := s.RetainedChats(); len(retained) != 1 {
		t.Fatal(retained)
	}
	if err = s.Get("external-sessions", "session", &map[string]string{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func() error{
		"metadata":       func() error { return s.SaveChat(c) },
		"generation":     func() error { return s.SetChatGeneration(c.ID, domain.Generation{}) },
		"message":        func() error { return s.AddMessage(domain.Message{ID: "late", ChatID: c.ID}) },
		"finish":         func() error { return s.FinishMessage(domain.Message{ID: c.ID + "-message", ChatID: c.ID}) },
		"chunk":          func() error { return s.AppendChunk(c.ID+"-message", "late", "") },
		"edit":           func() error { return s.UpdateMessageContent(c.ID, c.ID+"-message", "late") },
		"delete message": func() error { return s.DeleteMessage(c.ID, c.ID+"-message") },
		"attachment": func() error {
			return s.AddAttachment(domain.Attachment{ID: "late", ChatID: c.ID, Size: 1}, []byte("x"))
		},
		"run":     func() error { return s.Put("runs", "late", domain.Run{ID: "late", ChatID: c.ID}) },
		"replica": func() error { return s.MergeReplica(c.ID, "late-source", "epoch", 1, nil, nil) },
	} {
		if err := mutate(); err == nil {
			t.Errorf("%s changed deleted history", name)
		}
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if messages, err := s.Messages(c.ID); err != nil || len(messages) != 1 || messages[0].Content != "saved answer" {
		t.Fatal(messages, err)
	}
	if _, data, err := s.Attachment(c.ID, c.ID+"-media", false); err != nil || string(data) != "saved" {
		t.Fatal(string(data), err)
	}
	restored, err := s.RestoreChat(c.ID, now.Add(time.Hour))
	if err != nil || restored.ID != c.ID || restored.DeletedAt != nil || restored.Title != c.Title {
		t.Fatal(restored, err)
	}
	if err = s.PurgeChat(c.ID); !errors.Is(err, ErrChatDeleted) {
		t.Fatal("purged active chat", err)
	}
	second, err := s.TrashChat(c.ID, now.Add(2*time.Hour))
	if err != nil || !second.DeletedAt.Equal(now.Add(2*time.Hour)) {
		t.Fatal(second, err)
	}
	if err = s.PurgeChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetainedChat(c.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, _, err = s.Attachment(c.ID, c.ID+"-media", false); err == nil {
		t.Fatal("media survived purge")
	}
	if err = s.CreateChat(c); err == nil {
		t.Fatal("purged chat resurrected")
	}
}

func TestChatRetentionMigrationKeepsExistingChatsActive(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := retainedFixture(t, s, "legacy")
	if _, err = s.db.Exec("DROP TABLE chat_retired_replicas; PRAGMA user_version=7"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 8 {
		t.Fatal(version, err)
	}
	active, err := s.Chat(c.ID)
	if err != nil || active.DeletedAt != nil || active.ExpiresAt != nil || active.Title != c.Title {
		t.Fatal(active, err)
	}
	if _, err = s.TrashChat(c.ID, domain.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestChatRetentionExpiryAndRetiredReplicas(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := retainedFixture(t, s, "chat")
	now := domain.Now()
	_ = s.Put("swarm-dispatches", "old", map[string]any{"chatId": c.ID, "status": "completed", "hosts": map[string]any{"remote": map[string]any{"home": "home", "id": "old", "token": "grant", "message": map[string]string{"content": "private payload"}}}})
	_ = s.Put("swarm", "identity", map[string]any{"node": map[string]string{"nodeId": "home"}})
	_ = s.Put("swarm-workers", "home:old", map[string]any{"home": "home", "id": "old", "token": "grant", "chat": c, "message": map[string]string{"content": "private local worker payload"}})
	if _, err = s.TrashChat(c.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreChat(c.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.MergeReplica(c.ID, "remote:old", "epoch", 100, nil, nil); !errors.Is(err, ErrStaleReplica) {
		t.Fatal("retired dispatch accepted", err)
	}
	if err = s.MergeReplica(c.ID, "remote:new", "epoch", 1, nil, nil); err != nil {
		t.Fatal("new dispatch rejected", err)
	}
	if _, err = s.TrashChat(c.ID, now); err != nil {
		t.Fatal(err)
	}
	if count, err := s.PurgeExpiredChats(now.Add(ChatRetention - time.Nanosecond)); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err = s.RestoreChat(c.ID, now.Add(ChatRetention)); !errors.Is(err, ErrChatExpired) {
		t.Fatal("restored at expiry", err)
	}
	if count, err := s.PurgeExpiredChats(now.Add(ChatRetention)); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	var receipt struct {
		Hosts map[string]map[string]json.RawMessage `json:"hosts"`
	}
	if err = s.Get("swarm-dispatches", "old", &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Hosts["remote"]["message"]) != 0 || len(receipt.Hosts["remote"]["token"]) == 0 {
		t.Fatal("purge retained payload or lost cancellation grant", receipt)
	}
	var worker map[string]json.RawMessage
	if err = s.Get("swarm-workers", "home:old", &worker); err != nil || len(worker["message"]) != 0 {
		t.Fatal("local worker payload survived purge", worker, err)
	}
	if err = s.Put("swarm-workers", "home:old", map[string]any{"status": "cancelled", "message": "late private payload"}); err == nil {
		t.Fatal("late cancellation resurrected a purged worker payload")
	}
}

func TestChatRetentionRestorePurgeRace(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 12; i++ {
		c := retainedFixture(t, s, domain.ID())
		if _, err = s.TrashChat(c.ID, domain.Now()); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = s.RestoreChat(c.ID, domain.Now()) }()
		go func() { defer wg.Done(); _ = s.PurgeChat(c.ID) }()
		wg.Wait()
		chat, err := s.RetainedChat(c.ID)
		if err == nil && chat.DeletedAt != nil || err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("invalid final state", chat, err)
		}
	}
}

func TestChatRetentionExpiredReadsAndProjectionSessions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := retainedFixture(t, s, "expired")
	if _, err = s.TrashChat(c.ID, domain.Now().Add(-ChatRetention)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetainedChat(c.ID); !errors.Is(err, ErrChatExpired) {
		t.Fatal(err)
	}
	if _, err = s.Messages(c.ID); !errors.Is(err, ErrChatExpired) {
		t.Fatal(err)
	}
	if _, _, err = s.Attachment(c.ID, c.ID+"-media", false); !errors.Is(err, ErrChatExpired) {
		t.Fatal(err)
	}
	if chats, err := s.RetainedChats(); err != nil || len(chats) != 0 {
		t.Fatal(chats, err)
	}
	projection := domain.Chat{ID: "projection", Projection: true}
	if err = s.CreateChat(projection); err != nil {
		t.Fatal(err)
	}
	_ = s.Put("external-sessions", "native", map[string]string{"chatId": projection.ID, "sessionId": "old-native-session"})
	projection.ExecutionEpoch = "restored"
	if err = s.Accept(projection, domain.Message{ID: "new-input", ChatID: projection.ID}, nil, "new-request", "new-dispatch"); err != nil {
		t.Fatal(err)
	}
	if err = s.Get("external-sessions", "native", &map[string]string{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("projection reused pre-deletion native session", err)
	}
}
