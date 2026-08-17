// Package settings persists Echo application settings to a JSON file in the
// user's platform-appropriate application config directory (echo/echo.json on
// Windows, Linux, and macOS).
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/brent/echo/internal/llm"
)

// DefaultStorePath returns the platform-appropriate path to the Echo settings
// file. It lives under the user's application config directory (os.UserConfigDir),
// in an "Echo" subdirectory, as "echo.json".
func DefaultStorePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config dir: %w", err)
	}
	return filepath.Join(configDir, "Echo", "echo.json"), nil
}

// Store loads and saves Echo settings to a JSON file on disk.
type Store struct {
	path string
}

// NewStore creates a Store that reads and writes the settings file at path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the settings file path this store uses.
func (s *Store) Path() string {
	return s.path
}

// Load reads the settings file. If the file does not exist it returns the
// default settings without error. Settings are normalized with endpoint
// profiles treated as the source of truth.
func (s *Store) Load() (llm.Settings, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return llm.DefaultSettings(), nil
		}
		return llm.Settings{}, fmt.Errorf("read settings: %w", err)
	}
	var settings llm.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return llm.Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	return settings.NormalizedEndpointProfiles(), nil
}

// Save writes the settings to disk, creating the parent directory as needed.
// It writes to a temp file and renames it into place so a crash mid-write
// cannot corrupt an existing settings file.
func (s *Store) Save(settings llm.Settings) error {
	settings = settings.NormalizedEndpointProfiles()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename settings: %w", err)
	}
	return nil
}
