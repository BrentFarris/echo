package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/brent/echo/internal/domain"
)

// HistoryCopy contains materialized messages and compressed attachment staging
// files. It has no execution state: importing history cannot start a run.
type HistoryCopy struct {
	Chat        domain.Chat
	Messages    []domain.Message
	Runs        []domain.Run
	Attachments []HistoryAttachment
}
type HistoryAttachment struct {
	Metadata  domain.Attachment
	MessageID string
	Path      string
}
type forkReceipt struct {
	MessageID string `json:"messageId"`
	ChatID    string `json:"chatId"`
}

func (s *Store) ForkResult(source, target, request string) (string, error) {
	var receipt forkReceipt
	err := s.Get("chat-forks:"+source, request, &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if receipt.MessageID != target {
		return "", errors.New("fork request id was used for another message")
	}
	if _, err = s.Chat(receipt.ChatID); errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("the fork created by this request was deleted")
	}
	return receipt.ChatID, err
}

// StageAttachment copies compressed bytes without inflating every attachment
// into memory while constructing a large fork.
func (s *Store) StageAttachment(chatID, id, path string) error {
	var data []byte
	if err := s.db.QueryRow("SELECT data FROM attachments WHERE chat_id=? AND id=?", chatID, id).Scan(&data); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func insertHistory(tx *sql.Tx, copy HistoryCopy) error {
	for _, m := range copy.Messages {
		if m.ChatID != copy.Chat.ID {
			return errors.New("copied message belongs to another chat")
		}
		data, err := pack(m)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO messages(id,chat_id,data) VALUES(?,?,?)", m.ID, m.ChatID, data); err != nil {
			return err
		}
	}
	for _, run := range copy.Runs {
		if run.ChatID != copy.Chat.ID || run.Status == "running" || run.Status == "waiting" || run.Status == "queued" {
			return errors.New("copied runs must be inactive and belong to the destination")
		}
		data, err := json.Marshal(run)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO records VALUES('runs',?,?)", run.ID, data); err != nil {
			return err
		}
	}
	for _, a := range copy.Attachments {
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return err
		}
		metadata, err := json.Marshal(a.Metadata)
		if err != nil {
			return err
		}
		if a.Metadata.ChatID != copy.Chat.ID {
			return errors.New("copied attachment belongs to another chat")
		}
		result, err := tx.Exec("INSERT INTO attachments SELECT ?,chat_id,id,?,?,? FROM messages WHERE id=? AND chat_id=?", a.Metadata.ID, metadata, data, time.Now().Unix(), a.MessageID, copy.Chat.ID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return errors.New("copied attachment has no message")
		}
	}
	return nil
}

// Caller holds server admission lock and has revalidated the source snapshot.
func (s *Store) CommitFork(source, target, request string, copy HistoryCopy) (string, error) {
	if id, err := s.ForkResult(source, target, request); id != "" || err != nil {
		return id, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	data, err := json.Marshal(copy.Chat)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec("INSERT INTO chats VALUES(?,?)", copy.Chat.ID, data); err != nil {
		return "", err
	}
	if err = insertHistory(tx, copy); err != nil {
		return "", err
	}
	data, err = json.Marshal(forkReceipt{MessageID: target, ChatID: copy.Chat.ID})
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec("INSERT INTO records VALUES(?,?,?)", "chat-forks:"+source, request, data); err != nil {
		return "", err
	}
	return copy.Chat.ID, tx.Commit()
}

func (s *Store) ForkProjectionSeeded(chatID string) (bool, error) {
	var seeded bool
	err := s.Get("fork-projections", chatID, &seeded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return seeded, err
}

func (s *Store) SeedForkProjection(copy HistoryCopy) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRow("SELECT data FROM records WHERE namespace='fork-projections' AND id=?", copy.Chat.ID).Scan(&data); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = tx.QueryRow("SELECT data FROM chats WHERE id=?", copy.Chat.ID).Scan(&data); err != nil {
		return err
	}
	var chat domain.Chat
	if err = json.Unmarshal(data, &chat); err != nil {
		return err
	}
	if !chat.Projection || chat.ForkedFrom == nil {
		return errors.New("history initialization requires a fork projection")
	}
	if err = insertHistory(tx, copy); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO records VALUES('fork-projections',?,'true')", copy.Chat.ID); err != nil {
		return err
	}
	return tx.Commit()
}
