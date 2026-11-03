package store

import (
	"database/sql"
	"encoding/json"
)

// Snapshot legacy implicit selections before plugins.New registers this build's
// builtins. Only the catalog already saved in the profile may grant access.
func migrateBotPlugins(tx *sql.Tx) error {
	rows, err := tx.Query("SELECT id,data FROM records WHERE namespace='plugins'")
	if err != nil {
		return err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return err
		}
		var plugin struct {
			Scope string `json:"scope"`
		}
		if err = json.Unmarshal(data, &plugin); err != nil {
			return err
		}
		if plugin.Scope == "bot" || plugin.Scope == "" {
			ids = append(ids, id)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	rows, err = tx.Query("SELECT id,data FROM records WHERE namespace='bots' AND id<>'echo'")
	if err != nil {
		return err
	}
	defer rows.Close()
	updates := map[string][]byte{}
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return err
		}
		var bot map[string]json.RawMessage
		if err = json.Unmarshal(data, &bot); err != nil {
			return err
		}
		var selections map[string]bool
		if raw := bot["plugins"]; raw != nil {
			if err = json.Unmarshal(raw, &selections); err != nil {
				return err
			}
		}
		if selections == nil {
			selections = map[string]bool{}
		}
		changed := false
		for _, pluginID := range ids {
			if _, exists := selections[pluginID]; !exists {
				selections[pluginID] = true
				changed = true
			}
		}
		if changed {
			bot["plugins"], err = json.Marshal(selections)
			if err != nil {
				return err
			}
			updates[id], err = json.Marshal(bot)
			if err != nil {
				return err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for id, data := range updates {
		if _, err = tx.Exec("UPDATE records SET data=? WHERE namespace='bots' AND id=?", data, id); err != nil {
			return err
		}
	}
	_, err = tx.Exec("PRAGMA user_version=5")
	return err
}
