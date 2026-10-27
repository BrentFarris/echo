package store

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/brent/echo/internal/domain"
)

const MaxAttachmentBytes = 32 << 20
const MaxAttachmentBatchBytes = 64 << 20
const MaxAttachments = 8

func (s *Store) AddAttachment(a domain.Attachment, data []byte) error {
	if int64(len(data)) != a.Size || a.Size > MaxAttachmentBytes {
		return errors.New("attachment exceeds 32 MiB")
	}
	metadata, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var compressed bytes.Buffer
	w, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if _, err = w.Write(data); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Abandoned uploads are reclaimed without touching accepted transcript data.
	if _, err = tx.Exec("DELETE FROM attachments WHERE message_id IS NULL AND created_at<?", time.Now().Add(-24*time.Hour).Unix()); err != nil {
		return err
	}
	var count, size int64
	if err = tx.QueryRow("SELECT count(*),coalesce(sum(json_extract(metadata,'$.size')),0) FROM attachments WHERE chat_id=? AND message_id IS NULL", a.ChatID).Scan(&count, &size); err != nil {
		return err
	}
	if count >= MaxAttachments || size+a.Size > MaxAttachmentBatchBytes {
		return errors.New("a message can have up to 8 attachments totaling 64 MiB; remove unused attachments first")
	}
	if _, err = tx.Exec("INSERT INTO attachments VALUES(?,?,NULL,?,?,?)", a.ID, a.ChatID, metadata, compressed.Bytes(), time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Attachment(chatID, id string, pendingOnly bool) (domain.Attachment, []byte, error) {
	var a domain.Attachment
	var metadata, compressed []byte
	query := "SELECT metadata,data FROM attachments WHERE chat_id=? AND id=?"
	if pendingOnly {
		query += " AND message_id IS NULL"
	}
	if err := s.db.QueryRow(query, chatID, id).Scan(&metadata, &compressed); err != nil {
		return a, nil, err
	}
	if err := json.Unmarshal(metadata, &a); err != nil {
		return a, nil, err
	}
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return a, nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, MaxAttachmentBytes+1))
	if err == nil && int64(len(data)) != a.Size {
		err = errors.New("invalid attachment size")
	}
	return a, data, err
}

func (s *Store) RemoveAttachment(chatID, id string) error {
	_, err := s.db.Exec("DELETE FROM attachments WHERE chat_id=? AND id=? AND message_id IS NULL", chatID, id)
	return err
}
