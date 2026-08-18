// Package appdata owns the Echo application data file (echo.json) in the
// user's platform-appropriate config directory. The file is a single JSON
// document that holds both the application settings and the list of registered
// workspaces, so the settings and workspace stores can share one file without
// clobbering each other's data.
package appdata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Workspace describes a workspace registered with Echo. MainPath is the
// primary folder (the one that owns the .echo directory); Folders lists every
// folder the workspace operates on. IconExt is the detected extension of the
// workspace icon stored at .echo/icon.<ext> (empty when no icon was set).
type Workspace struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	MainPath string   `json:"mainPath"`
	IconExt  string   `json:"iconExt,omitempty"`
	Folders  []string `json:"folders,omitempty"`
}

// File is the top-level structure of echo.json. Settings is kept as raw JSON
// so this package stays decoupled from the settings schema; the settings store
// owns parsing it. ActiveWorkspaceID records the last workspace the user
// opened so Echo can restore it as the current workspace on startup.
type File struct {
	Settings           json.RawMessage `json:"settings"`
	Workspaces         []Workspace     `json:"workspaces"`
	ActiveWorkspaceID  string          `json:"activeWorkspaceId,omitempty"`
}

// DefaultStorePath returns the platform-appropriate path to the Echo app data
// file. It lives under the user's application config directory (os.UserConfigDir),
// in an "Echo" subdirectory, as "echo.json".
func DefaultStorePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config dir: %w", err)
	}
	return filepath.Join(configDir, "Echo", "echo.json"), nil
}

// Store reads and writes the Echo app data file. It is safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore creates a Store that reads and writes the app data file at path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the app data file path this store uses.
func (s *Store) Path() string {
	return s.path
}

// Load reads the app data file. If the file does not exist it returns an empty
// File without error. A legacy file written as a bare settings object (without
// a "settings" key) is migrated by treating the whole document as settings.
func (s *Store) Load() (File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("read app data: %w", err)
	}

	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("parse app data: %w", err)
	}

	// Detect the legacy bare-settings format and treat the whole document as
	// the settings payload.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err == nil {
		if _, ok := probe["settings"]; !ok {
			f.Settings = data
		}
	}

	return f, nil
}

// Save writes the app data file to disk, creating the parent directory as
// needed. It writes to a temp file and renames it into place so a crash
// mid-write cannot corrupt an existing file.
func (s *Store) Save(f File) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create app data dir: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal app data: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write app data: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename app data: %w", err)
	}
	return nil
}
