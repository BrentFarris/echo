package store

import (
	"database/sql"
	"errors"

	"github.com/brent/echo/internal/domain"
)

func messageInChat(tx *sql.Tx, chatID, id string) (domain.Message, int64, error) {
	var m domain.Message
	var seq int64
	var data []byte
	err := tx.QueryRow("SELECT seq,data FROM messages WHERE chat_id=? AND id=?", chatID, id).Scan(&seq, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return m, 0, errors.New("message was not found")
	}
	if err == nil {
		err = unpack(data, &m)
	}
	return m, seq, err
}

// DeleteMessage removes only this half of the conversation. A tool result is
// dependent on its call, not on every preceding assistant message in a run.
func (s *Store) DeleteMessage(chatID, id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, seq, err := messageInChat(tx, chatID, id)
	if err != nil {
		return err
	}
	ids := []string{id}
	if m.Role == "assistant" && len(m.Calls) > 0 {
		calls := map[string]bool{}
		for _, call := range m.Calls {
			calls[call.ID] = true
		}
		rows, err := tx.Query("SELECT data FROM messages WHERE chat_id=? AND seq>? ORDER BY seq", chatID, seq)
		if err != nil {
			return err
		}
		for rows.Next() {
			var data []byte
			var candidate domain.Message
			if err = rows.Scan(&data); err == nil {
				err = unpack(data, &candidate)
			}
			if err != nil {
				rows.Close()
				return err
			}
			if candidate.Role == "tool" && candidate.RunID == m.RunID && calls[candidate.CallID] {
				ids = append(ids, candidate.ID)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	for _, target := range ids {
		// Foreign keys cascade to streamed chunks and attachments.
		if _, err = tx.Exec("DELETE FROM messages WHERE chat_id=? AND id=?", chatID, target); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateMessageContent(chatID, id, content string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	m, _, err := messageInChat(tx, chatID, id)
	if err != nil {
		return err
	}
	m.Content = content
	data, err := pack(m)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE messages SET data=? WHERE chat_id=? AND id=?", data, chatID, id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM chunks WHERE message_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// Called inside acceptance, after all send validation. Rollback restores the
// original transcript if the replacement or its durable retry key cannot save.
func truncateForResubmit(tx *sql.Tx, chatID, id string, attachments []domain.Attachment) error {
	m, seq, err := messageInChat(tx, chatID, id)
	if err != nil {
		return err
	}
	if m.Role != "user" || m.RunID != "" {
		return errors.New("only user messages can be resubmitted")
	}
	for _, a := range attachments {
		if _, err = tx.Exec("UPDATE attachments SET message_id=NULL WHERE chat_id=? AND message_id=? AND id=?", chatID, id, a.ID); err != nil {
			return err
		}
	}
	_, err = tx.Exec("DELETE FROM messages WHERE chat_id=? AND seq>=?", chatID, seq)
	return err
}
