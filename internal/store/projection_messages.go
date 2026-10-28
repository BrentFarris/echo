package store

import (
	"encoding/json"
	"errors"

	"github.com/brent/echo/internal/domain"
)

// ReconcileProjection applies home-side edits/deletions to messages this worker
// already knows. It never imports other bots' work or home-side path grants.
func (s *Store) ReconcileProjection(chatID string, contents map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	var chat domain.Chat
	if err = tx.QueryRow("SELECT data FROM chats WHERE id=?", chatID).Scan(&data); err != nil {
		return err
	}
	if err = json.Unmarshal(data, &chat); err != nil {
		return err
	}
	if !chat.Projection {
		return errors.New("only worker projections can be reconciled")
	}
	rows, err := tx.Query("SELECT data FROM messages WHERE chat_id=? ORDER BY seq", chatID)
	if err != nil {
		return err
	}
	messages := []domain.Message{}
	for rows.Next() {
		var m domain.Message
		if err = rows.Scan(&data); err == nil {
			err = unpack(data, &m)
		}
		if err != nil {
			rows.Close()
			return err
		}
		messages = append(messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, m := range messages {
		content, exists := contents[m.ID]
		if !exists {
			if _, err = tx.Exec("DELETE FROM messages WHERE chat_id=? AND id=?", chatID, m.ID); err != nil {
				return err
			}
		} else if m.Role == "assistant" && m.Content != content {
			m.Content = content
			if data, err = pack(m); err != nil {
				return err
			}
			if _, err = tx.Exec("UPDATE messages SET data=? WHERE chat_id=? AND id=?", data, chatID, m.ID); err != nil {
				return err
			}
			if _, err = tx.Exec("DELETE FROM chunks WHERE message_id=?", m.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
