package store

import "github.com/brent/echo/internal/domain"

// SetModelContent preserves the completed tool record and attachment bytes.
func (s *Store) SetModelContent(chat, id, content string) (domain.Message, error) {
	var m domain.Message
	tx, err := s.db.Begin()
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRow("SELECT data FROM messages WHERE chat_id=? AND id=?", chat, id).Scan(&data); err != nil {
		return m, err
	}
	if err = unpack(data, &m); err != nil {
		return m, err
	}
	m.ModelContent = &content
	data, err = pack(m)
	if err != nil {
		return m, err
	}
	if _, err = tx.Exec("UPDATE messages SET data=? WHERE chat_id=? AND id=?", data, chat, id); err != nil {
		return m, err
	}
	return m, tx.Commit()
}
