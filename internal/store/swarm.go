package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/brent/echo/internal/domain"
)

var ErrStaleReplica = errors.New("replica snapshot already delivered")

// Swarm migrations are additive. Legacy local IDs, transcripts and retry records are
// unchanged. A tombstone survives deletion of its chat and foreign keys.
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 4 {
		return errors.New("this profile was created by a newer Echo; update Echo before opening it")
	}
	if version >= 4 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version < 2 {
		if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS chat_tombstones(id TEXT PRIMARY KEY,deleted_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS swarm_checkpoints(chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,source TEXT NOT NULL,epoch TEXT NOT NULL,sequence INTEGER NOT NULL,PRIMARY KEY(chat_id,source));
 PRAGMA user_version=2;`); err != nil {
			return err
		}
	}
	// Remember superseded process epochs so a delayed response from an old
	// connection cannot overwrite results already reconciled after a restart.
	if version < 3 {
		if _, err = tx.Exec(`ALTER TABLE swarm_checkpoints ADD COLUMN retired TEXT NOT NULL DEFAULT '[]'; PRAGMA user_version=3;`); err != nil {
			return err
		}
	}
	if err = migrateNotifications(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// MergeReplica commits all deliveries and their source watermark together. A
// stale source snapshot cannot replace newer output or recreate a deleted chat.
func (s *Store) MergeReplica(chat, source, epoch string, sequence int64, messages []domain.Message, runs []domain.Run, alerts ...domain.ChatAlert) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists string
	if err = tx.QueryRow("SELECT id FROM chats WHERE id=?", chat).Scan(&exists); err != nil {
		return err
	}
	var previousEpoch string
	var retiredJSON string
	var previous int64
	err = tx.QueryRow("SELECT epoch,sequence,retired FROM swarm_checkpoints WHERE chat_id=? AND source=?", chat, source).Scan(&previousEpoch, &previous, &retiredJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && previousEpoch == epoch && previous >= sequence {
		return ErrStaleReplica
	}
	retired := []string{}
	if retiredJSON != "" {
		if err := json.Unmarshal([]byte(retiredJSON), &retired); err != nil {
			return err
		}
	}
	if slices.Contains(retired, epoch) {
		return ErrStaleReplica
	}
	if previousEpoch != "" && previousEpoch != epoch {
		retired = append(retired, previousEpoch)
	}
	newCompletions := map[string]bool{}
	for _, m := range messages {
		if m.ChatID != chat {
			return errors.New("replica chat mismatch")
		}
		var owner string
		var priorData []byte
		err = tx.QueryRow("SELECT chat_id,data FROM messages WHERE id=?", m.ID).Scan(&owner, &priorData)
		if err == nil && owner != chat {
			return errors.New("replica message collision")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var prior domain.Message
		if len(priorData) > 0 {
			if err = unpack(priorData, &prior); err != nil {
				return err
			}
		}
		newCompletions[m.ID] = prior.Status != "completed"
		data, err := pack(m)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO messages(id,chat_id,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data WHERE messages.chat_id=excluded.chat_id", m.ID, chat, data); err != nil {
			return err
		}
		if m.Status != "running" {
			if err = resolveAlerts(tx, m.ChatID, m.ID); err != nil {
				return err
			}
		}
	}
	for _, a := range alerts {
		if a.Kind == "completion" && !newCompletions[a.MessageID] {
			continue
		}
		if err = localAlert(tx, a); err != nil {
			return err
		}
	}
	for _, run := range runs {
		if run.ChatID != chat {
			return errors.New("replica run mismatch")
		}
		data, err := json.Marshal(run)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO records VALUES('runs',?,?) ON CONFLICT(namespace,id) DO UPDATE SET data=excluded.data", run.ID, data); err != nil {
			return err
		}
	}
	retiredData, _ := json.Marshal(retired)
	if _, err = tx.Exec("INSERT INTO swarm_checkpoints(chat_id,source,epoch,sequence,retired) VALUES(?,?,?,?,?) ON CONFLICT(chat_id,source) DO UPDATE SET epoch=excluded.epoch,sequence=excluded.sequence,retired=excluded.retired", chat, source, epoch, sequence, string(retiredData)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ChatDeleted(id string) bool {
	var found string
	return s.db.QueryRow("SELECT id FROM chat_tombstones WHERE id=?", id).Scan(&found) == nil
}
