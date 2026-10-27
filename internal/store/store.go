// Package store owns the transactional boundary. Only transcript payloads are
// compressed: indexed metadata stays cheap to query without reading histories.
package store

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/brent/echo/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	lock *os.File
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := lockDirectory(filepath.Join(dir, "server.lock"))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "echo2.sqlite"))
	if err != nil {
		lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, lock: lock}
	_, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA auto_vacuum=INCREMENTAL; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS records(namespace TEXT NOT NULL,id TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(namespace,id));
CREATE UNIQUE INDEX IF NOT EXISTS bot_handles ON records(lower(json_extract(data,'$.handle'))) WHERE namespace='bots';
CREATE TABLE IF NOT EXISTS chats(id TEXT PRIMARY KEY,data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS messages(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,data BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS message_chat ON messages(chat_id,seq);
CREATE TABLE IF NOT EXISTS attachments(id TEXT PRIMARY KEY,chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,message_id TEXT REFERENCES messages(id) ON DELETE CASCADE,metadata BLOB NOT NULL,data BLOB NOT NULL,created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS attachment_chat ON attachments(chat_id);
CREATE TABLE IF NOT EXISTS chunks(id INTEGER PRIMARY KEY AUTOINCREMENT,message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,content BLOB NOT NULL,reasoning BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS dispatch_requests(chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,request_id TEXT NOT NULL,dispatch_id TEXT NOT NULL,PRIMARY KEY(chat_id,request_id));
PRAGMA user_version=1;`)
	if err != nil {
		db.Close()
		lock.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	defer s.lock.Close()
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

func (s *Store) Accepted(chatID, requestID string) string {
	if requestID == "" {
		return ""
	}
	var id string
	s.db.QueryRow("SELECT dispatch_id FROM dispatch_requests WHERE chat_id=? AND request_id=?", chatID, requestID).Scan(&id)
	return id
}

// Accept commits the user message, chat metadata, run boundaries and retry key
// together. A caller never observes an accepted message with no durable run.
func (s *Store) Accept(c domain.Chat, m domain.Message, runs []domain.Run, requestID, dispatchID string) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	chat, e := json.Marshal(c)
	if e != nil {
		return e
	}
	payload, e := pack(m)
	if e != nil {
		return e
	}
	result, e := tx.Exec("UPDATE chats SET data=? WHERE id=?", chat, c.ID)
	if e != nil {
		return e
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("chat was deleted")
	}
	if _, e = tx.Exec("INSERT INTO messages(id,chat_id,data) VALUES(?,?,?)", m.ID, c.ID, payload); e != nil {
		return e
	}
	for _, a := range m.Attachments {
		result, e := tx.Exec("UPDATE attachments SET message_id=? WHERE id=? AND chat_id=? AND message_id IS NULL", m.ID, a.ID, c.ID)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return errors.New("attachment was removed or already sent; attach it again")
		}
	}
	for _, run := range runs {
		data, e := json.Marshal(run)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO records VALUES('runs',?,?)", run.ID, data); e != nil {
			return e
		}
	}
	if requestID != "" {
		if _, e = tx.Exec("INSERT INTO dispatch_requests VALUES(?,?,?)", c.ID, requestID, dispatchID); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (s *Store) ClearNamespace(ns string) error {
	_, e := s.db.Exec("DELETE FROM records WHERE namespace=?", ns)
	return e
}

func (s *Store) PutBatch(ns string, values map[string]any) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for id, v := range values {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO records VALUES(?,?,?) ON CONFLICT(namespace,id) DO UPDATE SET data=excluded.data", ns, id, b); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func pack(v any) ([]byte, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var out bytes.Buffer
	w, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	_, e = w.Write(b)
	if e != nil {
		return nil, e
	}
	if e = w.Close(); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
func unpack(b []byte, v any) error {
	r, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer r.Close()
	return json.NewDecoder(io.LimitReader(r, 32<<20)).Decode(v)
}
func (s *Store) Put(ns, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO records VALUES(?,?,?) ON CONFLICT(namespace,id) DO UPDATE SET data=excluded.data", ns, id, b)
	return e
}
func (s *Store) Get(ns, id string, v any) error {
	var b []byte
	if e := s.db.QueryRow("SELECT data FROM records WHERE namespace=? AND id=?", ns, id).Scan(&b); e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (s *Store) Delete(ns, id string) error {
	_, e := s.db.Exec("DELETE FROM records WHERE namespace=? AND id=?", ns, id)
	return e
}
func (s *Store) List(ns string) ([]json.RawMessage, error) {
	rows, e := s.db.Query("SELECT data FROM records WHERE namespace=? ORDER BY id", ns)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Entries includes record keys for namespaces whose values predate embedded IDs.
func (s *Store) Entries(ns string) (map[string]json.RawMessage, error) {
	rows, err := s.db.Query("SELECT id,data FROM records WHERE namespace=?", ns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		out[id] = data
	}
	return out, rows.Err()
}

// Update serializes read/modify/write, including a missing record. Callbacks must
// not call Store methods (the database intentionally has one write connection).
func (s *Store) Update(ns, id string, fn func(json.RawMessage) (any, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var b []byte
	e = tx.QueryRow("SELECT data FROM records WHERE namespace=? AND id=?", ns, id).Scan(&b)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	v, e := fn(b)
	if e != nil {
		return e
	}
	b, e = json.Marshal(v)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO records VALUES(?,?,?) ON CONFLICT(namespace,id) DO UPDATE SET data=excluded.data", ns, id, b); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) CreateChat(c domain.Chat) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO chats VALUES(?,?)", c.ID, b)
	return e
}
func (s *Store) SaveChat(c domain.Chat) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	r, e := s.db.Exec("UPDATE chats SET data=? WHERE id=?", b, c.ID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) Chat(id string) (domain.Chat, error) {
	var c domain.Chat
	var b []byte
	e := s.db.QueryRow("SELECT data FROM chats WHERE id=?", id).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &c)
	}
	return c, e
}
func (s *Store) SetChatGeneration(id string, generation domain.Generation) error {
	data, e := json.Marshal(generation)
	if e != nil {
		return e
	}
	r, e := s.db.Exec("UPDATE chats SET data=json_set(data,'$.generation',json(?)) WHERE id=?", string(data), id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) Chats() ([]domain.Chat, error) {
	rows, e := s.db.Query("SELECT data FROM chats ORDER BY json_extract(data,'$.updatedAt') DESC")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Chat{}
	for rows.Next() {
		var b []byte
		var c domain.Chat
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &c); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) DeleteChat(id string) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM records WHERE namespace='runs' AND json_extract(data,'$.chatId')=?", id); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM chats WHERE id=?", id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) AddMessage(m domain.Message) error {
	b, e := pack(m)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO messages(id,chat_id,data) VALUES(?,?,?)", m.ID, m.ChatID, b)
	return e
}
func (s *Store) FinishMessage(m domain.Message) error {
	b, e := pack(m)
	if e != nil {
		return e
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE messages SET data=? WHERE id=?", b, m.ID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	if _, e = tx.Exec("DELETE FROM chunks WHERE message_id=?", m.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) AppendChunk(id, content, reasoning string) error {
	c, e := pack(content)
	if e != nil {
		return e
	}
	r, e := pack(reasoning)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("INSERT INTO chunks(message_id,content,reasoning) VALUES(?,?,?)", id, c, r)
	return e
}
func (s *Store) Messages(chat string) ([]domain.Message, error) {
	rows, e := s.db.Query("SELECT data FROM messages WHERE chat_id=? ORDER BY seq", chat)
	if e != nil {
		return nil, e
	}
	out := []domain.Message{}
	for rows.Next() {
		var b []byte
		var m domain.Message
		if e = rows.Scan(&b); e == nil {
			e = unpack(b, &m)
		}
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		rs, e := s.db.Query("SELECT content,reasoning FROM chunks WHERE message_id=? ORDER BY id", out[i].ID)
		if e != nil {
			return nil, e
		}
		for rs.Next() {
			var cb, rb []byte
			var c, r string
			if e = rs.Scan(&cb, &rb); e == nil {
				e = unpack(cb, &c)
			}
			if e == nil {
				e = unpack(rb, &r)
			}
			if e != nil {
				rs.Close()
				return nil, e
			}
			out[i].Content += c
			out[i].Reasoning += r
		}
		e = rs.Err()
		rs.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) Recover() error {
	raw, e := s.List("runs")
	if e != nil {
		return e
	}
	for _, b := range raw {
		var r domain.Run
		if e = json.Unmarshal(b, &r); e != nil {
			return e
		}
		if r.Status == "running" || r.Status == "waiting" || r.Status == "queued" {
			r.Status = "interrupted"
			r.Error = "Echo stopped during this run. Inspect its tool results before starting new work."
			if e = s.Put("runs", r.ID, r); e != nil {
				return e
			}
		}
	}
	chats, e := s.Chats()
	if e != nil {
		return e
	}
	for _, c := range chats {
		ms, e := s.Messages(c.ID)
		if e != nil {
			return e
		}
		for _, m := range ms {
			if m.Status == "running" {
				m.Status = "interrupted"
				if e = s.FinishMessage(m); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func (s *Store) Reclaim() error { _, e := s.db.Exec("PRAGMA incremental_vacuum(128)"); return e }
func Decode[T any](raw []json.RawMessage) ([]T, error) {
	out := make([]T, 0, len(raw))
	for _, b := range raw {
		var t T
		if e := json.Unmarshal(b, &t); e != nil {
			return nil, fmt.Errorf("decode record: %w", e)
		}
		out = append(out, t)
	}
	return out, nil
}
