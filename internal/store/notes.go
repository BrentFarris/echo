package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/brent/echo/internal/domain"
)

var ErrNoteConflict = errors.New("note changed on another device; load latest or save your draft as a new note")

// MutateNote checks the revision and writes in one transaction. Updates never
// upsert: a late autosave cannot recreate a deleted note.
func (s *Store) MutateNote(note domain.Note, create, remove bool) (domain.Note, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return note, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if create {
		note.ID, note.Revision, note.CreatedAt = domain.ID(), 1, now
	} else {
		var raw []byte
		if err = tx.QueryRow("SELECT data FROM records WHERE namespace='notes' AND id=?", note.ID).Scan(&raw); err != nil {
			return note, err
		}
		var previous domain.Note
		if err = json.Unmarshal(raw, &previous); err != nil {
			return note, err
		}
		if note.Revision != previous.Revision {
			return note, ErrNoteConflict
		}
		note.CreatedAt, note.Revision = previous.CreatedAt, previous.Revision+1
	}
	note.UpdatedAt = now
	if remove {
		_, err = tx.Exec("DELETE FROM records WHERE namespace='notes' AND id=?", note.ID)
	} else {
		var raw []byte
		raw, err = json.Marshal(note)
		if err == nil {
			if create {
				_, err = tx.Exec("INSERT INTO records(namespace,id,data) VALUES('notes',?,?)", note.ID, raw)
			} else {
				_, err = tx.Exec("UPDATE records SET data=? WHERE namespace='notes' AND id=?", raw, note.ID)
			}
		}
	}
	if err != nil {
		return note, err
	}
	return note, tx.Commit()
}

func (s *Store) Note(id string) (domain.Note, error) {
	var note domain.Note
	err := s.Get("notes", id, &note)
	return note, err
}

// Keep the missing-note sentinel available to callers without exposing SQL.
var ErrNoteMissing = sql.ErrNoRows
