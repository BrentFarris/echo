package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/brent/echo/internal/domain"
)

type NotificationDevice struct {
	ID           string                         `json:"id"`
	Session      string                         `json:"-"`
	Subscription json.RawMessage                `json:"subscription,omitempty"`
	Preferences  domain.NotificationPreferences `json:"preferences"`
}

func migrateNotifications(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS notification_alerts (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, home TEXT NOT NULL,
 chat TEXT NOT NULL, message TEXT NOT NULL, kind TEXT NOT NULL, active INTEGER NOT NULL,
 expires INTEGER NOT NULL, data BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS notification_devices (id TEXT PRIMARY KEY, session TEXT NOT NULL, data BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS notification_deliveries (
 device TEXT NOT NULL REFERENCES notification_devices(id) ON DELETE CASCADE,
 alert TEXT NOT NULL REFERENCES notification_alerts(id) ON DELETE CASCADE,
 status TEXT NOT NULL DEFAULT 'pending', next INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(device,alert));
 CREATE INDEX IF NOT EXISTS notification_due ON notification_deliveries(status,next);
 CREATE INDEX IF NOT EXISTS notification_expiry ON notification_alerts(expires);
 CREATE INDEX IF NOT EXISTS notification_home ON notification_alerts(home,seq);
 PRAGMA user_version=4;`)
	return err
}

func putAlert(tx *sql.Tx, a domain.ChatAlert) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO notification_alerts(id,home,chat,message,kind,active,expires,data) VALUES(?,?,?,?,?,?,?,?)`, a.ID, a.HomeNodeID, a.ChatID, a.MessageID, a.Kind, a.Active, a.OccurredAt.Add(24*time.Hour).UnixMilli(), data)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		// Resolution advances the feed cursor, so other entry servers cancel too.
		if !a.Active {
			_, err = tx.Exec(`UPDATE notification_alerts SET active=0,data=?,seq=(SELECT COALESCE(MAX(seq),0)+1 FROM notification_alerts) WHERE id=? AND active=1`, data, a.ID)
		}
		return err
	}
	if a.Active {
		_, err = tx.Exec(`INSERT OR IGNORE INTO notification_deliveries(device,alert,next) SELECT id,?,? FROM notification_devices`, a.ID, time.Now().Add(2*time.Second).UnixMilli())
	}
	return err
}

func (s *Store) PutAlert(a domain.ChatAlert) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = putAlert(tx, a); err != nil {
		return err
	}
	return tx.Commit()
}

// Local alerts require a user-visible chat and are committed with its message.
func localAlert(tx *sql.Tx, a domain.ChatAlert) error {
	var data []byte
	if err := tx.QueryRow("SELECT data FROM chats WHERE id=?", a.ChatID).Scan(&data); err != nil {
		return err
	}
	var chat domain.Chat
	if err := json.Unmarshal(data, &chat); err != nil {
		return err
	}
	if chat.Projection {
		return nil
	}
	if chat.Title != "" {
		title := []rune(chat.Title)
		if len(title) > 80 {
			title = title[:80]
		}
		a.Preview = string(title) + " — " + a.Preview
	}
	return putAlert(tx, a)
}

func (s *Store) AddQuestionAlert(a domain.ChatAlert) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRow("SELECT data FROM messages WHERE id=? AND chat_id=?", a.MessageID, a.ChatID).Scan(&data); err != nil {
		return err
	}
	var m domain.Message
	if err = unpack(data, &m); err != nil {
		return err
	}
	if m.Status != "running" {
		return nil
	}
	if err = localAlert(tx, a); err != nil {
		return err
	}
	return tx.Commit()
}

func resolveAlerts(tx *sql.Tx, chat, message string) error {
	rows, err := tx.Query(`SELECT data FROM notification_alerts WHERE chat=? AND message=? AND kind='question' AND active=1`, chat, message)
	if err != nil {
		return err
	}
	var alerts []domain.ChatAlert
	for rows.Next() {
		var data []byte
		var a domain.ChatAlert
		if err = rows.Scan(&data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &a); err != nil {
			break
		}
		a.Active = false
		alerts = append(alerts, a)
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range alerts {
		if err = putAlert(tx, a); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AlertFeed(after int64, home string) ([]domain.ChatAlert, error) {
	rows, err := s.db.Query(`SELECT seq,data,active FROM notification_alerts WHERE seq>? AND (?='' OR home=?) AND expires>? ORDER BY seq LIMIT 250`, after, home, home, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ChatAlert{}
	for rows.Next() {
		var a domain.ChatAlert
		var data []byte
		var seq int64
		var active bool
		if err = rows.Scan(&seq, &data, &active); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &a); err != nil {
			return nil, err
		}
		a.Sequence = seq
		a.Active = active
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SaveNotificationDevice(d NotificationDevice) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// One browser push endpoint represents one installation even after its local
	// storage is cleared or its owner session is renewed.
	var subscription struct {
		Endpoint string `json:"endpoint"`
	}
	if len(d.Subscription) > 0 && json.Unmarshal(d.Subscription, &subscription) == nil && subscription.Endpoint != "" {
		if _, err = tx.Exec(`DELETE FROM notification_devices WHERE id<>? AND json_extract(data,'$.subscription.endpoint')=?`, d.ID, subscription.Endpoint); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO notification_devices(id,session,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data WHERE session=excluded.session`, d.ID, d.Session, data)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO notification_deliveries(device,alert,next) SELECT ?,id,? FROM notification_alerts WHERE kind='question' AND active=1 AND expires>?`, d.ID, time.Now().Add(2*time.Second).UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) NotificationDevice(id, session string) (NotificationDevice, error) {
	var d NotificationDevice
	var data []byte
	err := s.db.QueryRow("SELECT data,session FROM notification_devices WHERE id=? AND session=?", id, session).Scan(&data, &d.Session)
	if err == nil {
		err = json.Unmarshal(data, &d)
	}
	return d, err
}
func (s *Store) NotificationDevices() ([]NotificationDevice, error) {
	rows, err := s.db.Query("SELECT data,session FROM notification_devices")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDevice{}
	for rows.Next() {
		var d NotificationDevice
		var data []byte
		if err = rows.Scan(&data, &d.Session); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) DeleteNotificationDevice(id, session string) error {
	_, err := s.db.Exec("DELETE FROM notification_devices WHERE id=? AND session=?", id, session)
	return err
}

func (s *Store) ClearNotificationSubscription(d NotificationDevice) error {
	var subscription struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(d.Subscription, &subscription); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE notification_devices SET data=json_remove(data,'$.subscription') WHERE id=? AND session=? AND json_extract(data,'$.subscription.endpoint')=?`, d.ID, d.Session, subscription.Endpoint)
	return err
}
func (s *Store) PendingAlerts(device string, pushOnly ...bool) ([]domain.ChatAlert, error) {
	push := len(pushOnly) > 0 && pushOnly[0]
	rows, err := s.db.Query(`SELECT a.data FROM notification_alerts a JOIN notification_deliveries d ON d.alert=a.id JOIN notification_devices dev ON dev.id=d.device WHERE d.device=? AND d.status='pending' AND a.active=1 AND a.expires>? AND (?=0 OR CASE WHEN a.kind='question' THEN json_extract(dev.data,'$.preferences.questions') ELSE json_extract(dev.data,'$.preferences.completion') END=1) ORDER BY a.seq LIMIT 100`, device, time.Now().UnixMilli(), push)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ChatAlert{}
	for rows.Next() {
		var a domain.ChatAlert
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) ClaimAlert(device, alert, status string, due bool) (bool, error) {
	limit := int64(9223372036854775807)
	if due {
		limit = time.Now().UnixMilli()
	}
	r, err := s.db.Exec(`UPDATE notification_deliveries SET status=?,attempts=attempts+1,next=? WHERE device=? AND alert=? AND status='pending' AND next<=? AND EXISTS(SELECT 1 FROM notification_alerts WHERE id=alert AND active=1 AND expires>?)`, status, time.Now().Add(time.Minute).UnixMilli(), device, alert, limit, time.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func (s *Store) FinishPush(device, alert string, retry time.Time) error {
	status := "sent"
	if !retry.IsZero() {
		status = "retry"
	}
	_, err := s.db.Exec(`UPDATE notification_deliveries SET status=?,next=? WHERE device=? AND alert=?`, status, retry.UnixMilli(), device, alert)
	return err
}

type PushDelivery struct {
	Alert    domain.ChatAlert
	Attempts int
}

func (s *Store) RetryPush(device string) ([]PushDelivery, error) {
	rows, err := s.db.Query(`SELECT a.data,d.attempts FROM notification_alerts a JOIN notification_deliveries d ON d.alert=a.id WHERE d.device=? AND d.status IN ('retry','push') AND d.next<=? AND d.attempts<8 AND a.active=1 AND a.expires>? LIMIT 100`, device, time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushDelivery{}
	for rows.Next() {
		var d PushDelivery
		var data []byte
		if err = rows.Scan(&data, &d.Attempts); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &d.Alert); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) BeginPushRetry(device, alert string) error {
	_, err := s.db.Exec(`UPDATE notification_deliveries SET status='push',attempts=attempts+1,next=? WHERE device=? AND alert=?`, time.Now().Add(time.Minute).UnixMilli(), device, alert)
	return err
}
func (s *Store) PruneNotifications() error {
	_, err := s.db.Exec(`DELETE FROM notification_alerts WHERE expires<?`, time.Now().Add(-6*24*time.Hour).UnixMilli())
	return err
}

func (s *Store) NotificationAlert(id string) (domain.ChatAlert, error) {
	var a domain.ChatAlert
	var data []byte
	err := s.db.QueryRow("SELECT data FROM notification_alerts WHERE id=?", id).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &a)
	}
	return a, err
}

func (s *Store) ResolveQuestionAlert(chat, message string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = resolveAlerts(tx, chat, message); err != nil {
		return err
	}
	return tx.Commit()
}

func cancelChatAlerts(tx *sql.Tx, chat string) error {
	rows, err := tx.Query("SELECT data FROM notification_alerts WHERE chat=? AND active=1", chat)
	if err != nil {
		return err
	}
	var alerts []domain.ChatAlert
	for rows.Next() {
		var data []byte
		var a domain.ChatAlert
		if err = rows.Scan(&data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &a); err != nil {
			break
		}
		a.Active = false
		alerts = append(alerts, a)
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range alerts {
		if err = putAlert(tx, a); err != nil {
			return err
		}
	}
	return nil
}
