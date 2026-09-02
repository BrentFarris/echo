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
	"runtime"
	"strings"
	"sync"
)

// Workspace describes Echo's local registration and absolute locator mirror.
// The workspace-owned .echo/workspace.json remains authoritative for its name,
// folders, and settings. IconExt is the detected extension of the workspace
// icon stored at .echo/icon.<ext> (empty when no icon was set).
type Workspace struct {
	ID                          string   `json:"id"`
	Name                        string   `json:"name"`
	MainPath                    string   `json:"mainPath"`
	IconExt                     string   `json:"iconExt,omitempty"`
	Folders                     []string `json:"folders,omitempty"`
	SearchParentGitRepositories bool     `json:"searchParentGitRepositories,omitempty"`
}

// SavedCommand is a user-defined terminal command stored for a workspace.
// Order is retained so the terminal menu remains stable across restarts.
type SavedCommand struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Order   int    `json:"order"`
}

// File is the top-level structure of echo.json. Settings is kept as raw JSON
// so this package stays decoupled from the settings schema; the settings store
// owns parsing it. ActiveWorkspaceID records the last workspace the user
// opened so Echo can restore it as the current workspace on startup.
type File struct {
	Version           int                        `json:"version,omitempty"`
	Settings          json.RawMessage            `json:"settings"`
	LanguageServers   json.RawMessage            `json:"languageServerProfiles,omitempty"`
	DebugAdapters     json.RawMessage            `json:"debugAdapterProfiles,omitempty"`
	DebugState        map[string]json.RawMessage `json:"debugState,omitempty"`
	Auth              json.RawMessage            `json:"auth,omitempty"`
	Workspaces        []Workspace                `json:"workspaces"`
	ActiveWorkspaceID string                     `json:"activeWorkspaceId,omitempty"`
	SavedCommands     map[string][]SavedCommand  `json:"savedCommands,omitempty"`
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

var stores sync.Map

// NewStore creates a Store that reads and writes the app data file at path.
func NewStore(path string) *Store {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(parent, filepath.Base(path))
	}
	key := path
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	if existing, ok := stores.Load(key); ok {
		return existing.(*Store)
	}
	created := &Store{path: path}
	actual, _ := stores.LoadOrStore(key, created)
	return actual.(*Store)
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
	return s.loadLocked()
}

func (s *Store) loadLocked() (File, error) {
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
	return s.saveLocked(f)
}

// Update performs an atomic read-modify-write transaction while holding the
// store lock. Every subsystem that shares echo.json must use Update for
// mutations so concurrent settings, workspace, and authentication writes do
// not clobber one another.
func (s *Store) Update(update func(*File) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	if err := update(&f); err != nil {
		return err
	}
	return s.saveLocked(f)
}

func (s *Store) saveLocked(f File) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create app data dir: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal app data: %w", err)
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create app data temp file: %w", err)
	}
	tmp := tmpFile.Name()
	defer os.Remove(tmp)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod app data temp file: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write app data: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync app data: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close app data: %w", err)
	}
	if err := replaceFile(tmp, s.path); err != nil {
		return fmt.Errorf("rename app data: %w", err)
	}
	if directory, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
