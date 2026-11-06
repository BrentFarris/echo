package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/brent/echo/internal/domain"
)

const ChatRetention = 30 * 24 * time.Hour

var ErrChatDeleted = errors.New("this chat is deleted; restore it before making changes")
var ErrChatExpired = errors.New("this deleted chat has expired")

func migrateChatRetention(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS chat_retired_replicas(chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,source TEXT NOT NULL,PRIMARY KEY(chat_id,source));
CREATE INDEX IF NOT EXISTS chat_expiry ON chats(json_extract(data,'$.deletedAt'));
PRAGMA user_version=8;`); err != nil {
		return err
	}
	// Enforce the boundary in SQLite too: uploads, streams and replica writes
	// must not slip through a check performed before the deletion transaction.
	for _, table := range []string{"messages", "attachments", "chunks"} {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			row := "NEW"
			if operation == "DELETE" {
				row = "OLD"
			}
			chat := row + ".chat_id"
			if table == "chunks" {
				chat = "(SELECT chat_id FROM messages WHERE id=" + row + ".message_id)"
			}
			query := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS retained_%s_%s BEFORE %s ON %s
WHEN EXISTS(SELECT 1 FROM chats WHERE id=%s AND json_extract(data,'$.deletedAt') IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'this chat is deleted; restore it before making changes'); END;`, table, operation, operation, table, chat)
			if _, err := tx.Exec(query); err != nil {
				return err
			}
		}
	}
	for _, operation := range []string{"INSERT", "UPDATE"} {
		query := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS retained_records_%s BEFORE %s ON records
WHEN NEW.namespace IN ('runs','external-sessions') AND EXISTS(SELECT 1 FROM chats WHERE id=json_extract(NEW.data,'$.chatId') AND json_extract(data,'$.deletedAt') IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'this chat is deleted; restore it before making changes'); END;`, operation, operation)
		if _, err := tx.Exec(query); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE TRIGGER IF NOT EXISTS retired_dispatch BEFORE UPDATE ON records
WHEN OLD.namespace='swarm-dispatches' AND json_extract(OLD.data,'$.status') IN ('trashed','deleted') AND json_extract(NEW.data,'$.status') NOT IN ('trashed','deleted')
BEGIN SELECT RAISE(ABORT,'dispatch was retired'); END;`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TRIGGER IF NOT EXISTS retired_worker BEFORE UPDATE ON records
WHEN OLD.namespace='swarm-workers' AND json_extract(OLD.data,'$.status')='deleted' AND json_extract(NEW.data,'$.status')!='deleted'
BEGIN SELECT RAISE(ABORT,'worker was retired'); END;`); err != nil {
		return err
	}
	return nil
}

func retainedChat(data []byte, now time.Time) (domain.Chat, error) {
	var c domain.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	c.ExpiresAt = nil
	if c.DeletedAt != nil {
		expires := c.DeletedAt.Add(ChatRetention)
		c.ExpiresAt = &expires
		if !now.Before(expires) {
			return c, ErrChatExpired
		}
	}
	return c, nil
}

// RetainedChat is the explicit read-only path for a deleted conversation.
func (s *Store) RetainedChat(id string) (domain.Chat, error) {
	var data []byte
	if err := s.db.QueryRow("SELECT data FROM chats WHERE id=?", id).Scan(&data); err != nil {
		return domain.Chat{}, err
	}
	return retainedChat(data, domain.Now())
}

func (s *Store) RetainedChats() ([]domain.Chat, error) { return s.listChats(true) }

func (s *Store) listChats(includeDeleted bool) ([]domain.Chat, error) {
	rows, err := s.db.Query("SELECT data FROM chats ORDER BY json_extract(data,'$.updatedAt') DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Chat{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		c, err := retainedChat(data, domain.Now())
		if errors.Is(err, ErrChatExpired) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if includeDeleted || c.DeletedAt == nil {
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// TrashChat retains chats with history and permanently removes blank chats.
// A nil result means the chat was permanently removed.
func (s *Store) TrashChat(id string, now time.Time) (*domain.Chat, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRow("SELECT data FROM chats WHERE id=?", id).Scan(&data); err != nil {
		return nil, err
	}
	c, err := retainedChat(data, now)
	if err != nil {
		return nil, err
	}
	// Check inside the deletion transaction so an accepted first message
	// cannot race with permanent removal of an otherwise blank chat.
	var hasHistory bool
	if err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM messages WHERE chat_id=?)", id).Scan(&hasHistory); err != nil {
		return nil, err
	}
	if !hasHistory {
		if err = purgeChatTx(tx, id); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, s.removeChatFiles(id)
	}
	if c.DeletedAt != nil {
		return &c, nil
	}
	// A stopped remote worker can report late. Retire every old source even
	// across restoration; new dispatches get distinct source identities.
	if _, err = tx.Exec("INSERT OR IGNORE INTO chat_retired_replicas SELECT chat_id,source FROM swarm_checkpoints WHERE chat_id=?", id); err != nil {
		return &c, err
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO chat_retired_replicas SELECT ?,host.key || ':' || r.id FROM records r,json_each(r.data,'$.hosts') host WHERE r.namespace='swarm-dispatches' AND json_extract(r.data,'$.chatId')=?", id, id); err != nil {
		return &c, err
	}
	if _, err = tx.Exec("UPDATE records SET data=json_set(data,'$.status','trashed','$.reconciled',json('{}')) WHERE namespace='swarm-dispatches' AND json_extract(data,'$.chatId')=?", id); err != nil {
		return &c, err
	}
	if _, err = tx.Exec("UPDATE records SET data=json_set(data,'$.status','cancelled') WHERE namespace='runs' AND json_extract(data,'$.chatId')=? AND json_extract(data,'$.status') IN ('running','waiting','queued')", id); err != nil {
		return &c, err
	}
	rows, err := tx.Query("SELECT data FROM messages WHERE chat_id=?", id)
	if err != nil {
		return &c, err
	}
	var unfinished []domain.Message
	for rows.Next() {
		var m domain.Message
		if err = rows.Scan(&data); err == nil {
			err = unpack(data, &m)
		}
		if err != nil {
			rows.Close()
			return &c, err
		}
		if m.Status == "running" || m.Status == "waiting" || m.Status == "queued" {
			m.Status = "cancelled"
			unfinished = append(unfinished, m)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return &c, err
	}
	for _, m := range unfinished {
		data, err = pack(m)
		if err != nil {
			return &c, err
		}
		if _, err = tx.Exec("UPDATE messages SET data=? WHERE id=?", data, m.ID); err != nil {
			return &c, err
		}
	}
	if _, err = tx.Exec("DELETE FROM records WHERE namespace='external-sessions' AND json_extract(data,'$.chatId')=?", id); err != nil {
		return &c, err
	}
	if _, err = tx.Exec("DELETE FROM attachments WHERE chat_id=? AND message_id IS NULL", id); err != nil {
		return &c, err
	}
	if err = cancelChatAlerts(tx, id); err != nil {
		return &c, err
	}
	c.DeletedAt = &now
	expires := now.Add(ChatRetention)
	c.ExpiresAt = &expires
	data, err = json.Marshal(c)
	if err != nil {
		return &c, err
	}
	if _, err = tx.Exec("UPDATE chats SET data=? WHERE id=?", data, id); err != nil {
		return &c, err
	}
	return &c, tx.Commit()
}

func (s *Store) RestoreChat(id string, now time.Time) (domain.Chat, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return domain.Chat{}, err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRow("SELECT data FROM chats WHERE id=?", id).Scan(&data); err != nil {
		return domain.Chat{}, err
	}
	c, err := retainedChat(data, now)
	if err != nil {
		return c, err
	}
	if c.DeletedAt == nil {
		return c, nil
	}
	c.DeletedAt, c.ExpiresAt = nil, nil
	c.ExecutionEpoch = domain.ID()
	c.LastReadAt = &now
	c.MarkedUnread = false
	data, err = json.Marshal(c)
	if err != nil {
		return c, err
	}
	if _, err = tx.Exec("UPDATE chats SET data=? WHERE id=?", data, id); err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *Store) PurgeChat(id string) error { return s.deleteChat(id, true, nil) }

// Keep only credentials/IDs needed to deliver durable remote cancellation.
func purgeChatRecords(tx *sql.Tx, chatID string) error {
	rows, err := tx.Query(`SELECT namespace,id,data FROM records r WHERE namespace IN ('swarm-dispatches','swarm-workers') AND
(json_extract(data,'$.chatId')=? OR 'swarm:' || json_extract(data,'$.home') || ':' || json_extract(data,'$.chat.id')=? OR
(json_extract(data,'$.chat.id')=? AND json_extract(data,'$.home')=(SELECT json_extract(data,'$.node.nodeId') FROM records WHERE namespace='swarm' AND id='identity')))`, chatID, chatID, chatID)
	if err != nil {
		return err
	}
	type record struct {
		namespace, id string
		data          map[string]any
	}
	var records []record
	for rows.Next() {
		var r record
		var raw []byte
		if err = rows.Scan(&r.namespace, &r.id, &raw); err == nil {
			err = json.Unmarshal(raw, &r.data)
		}
		if err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	control := func(value map[string]any) map[string]any {
		return map[string]any{"home": value["home"], "id": value["id"], "token": value["token"]}
	}
	for _, r := range records {
		var minimal map[string]any
		if r.namespace == "swarm-dispatches" {
			hosts := map[string]any{}
			if original, ok := r.data["hosts"].(map[string]any); ok {
				for node, value := range original {
					if host, ok := value.(map[string]any); ok {
						hosts[node] = control(host)
					}
				}
			}
			minimal = map[string]any{"id": r.id, "chatId": chatID, "status": "deleted", "reconciled": map[string]bool{}, "hosts": hosts}
		} else {
			minimal = control(r.data)
			minimal["status"] = "deleted"
			if chat, ok := r.data["chat"].(map[string]any); ok {
				minimal["chat"] = map[string]any{"id": chat["id"]}
			}
		}
		raw, err := json.Marshal(minimal)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE records SET data=? WHERE namespace=? AND id=?", raw, r.namespace, r.id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM records WHERE namespace IN ('runs','external-sessions','pending-groups') AND json_extract(data,'$.chatId')=?", chatID); err != nil {
		return err
	}
	// Retain the inactive notification revision so connected machines learn
	// the cancellation, but discard its conversation-derived title and body.
	_, err = tx.Exec("UPDATE notification_alerts SET data=json_set(data,'$.title','','$.preview','') WHERE chat=?", chatID)
	return err
}

func (s *Store) PurgeExpiredChats(now time.Time) (int, error) {
	// Also clean up blank chats retained by versions predating immediate removal.
	rows, err := s.db.Query("SELECT id,data,EXISTS(SELECT 1 FROM messages WHERE chat_id=chats.id) FROM chats WHERE json_extract(data,'$.deletedAt') IS NOT NULL")
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		var data []byte
		var hasHistory bool
		if err = rows.Scan(&id, &data, &hasHistory); err != nil {
			rows.Close()
			return 0, err
		}
		if _, err = retainedChat(data, now); errors.Is(err, ErrChatExpired) || err == nil && !hasHistory {
			ids = append(ids, id)
		} else if err != nil {
			rows.Close()
			return 0, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		if err = s.deleteChat(id, true, &now); errors.Is(err, ErrChatDeleted) {
			continue
		} else if err != nil {
			return count, err
		}
		count++
	}
	// File removal can fail temporarily on Windows. Keep a durable retry even
	// after the chat row and its content have already been removed.
	pending, err := s.Entries("chat-file-cleanups")
	if err != nil {
		return count, err
	}
	for id := range pending {
		if err := s.removeChatFiles(id); err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Store) removeChatFiles(id string) error {
	if err := os.RemoveAll(s.externalAttachmentDirectory(id)); err != nil {
		return err
	}
	return s.Delete("chat-file-cleanups", id)
}
