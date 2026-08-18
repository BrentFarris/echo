// Package settings persists Echo application settings. Settings live inside the
// shared Echo app data file (echo.json) alongside the workspace list, so the
// settings store and the workspace store never clobber each other. See
// internal/appdata for the shared file layout.
package settings

import (
	"encoding/json"
	"fmt"

	"github.com/brent/echo/internal/appdata"
	"github.com/brent/echo/internal/llm"
)

// DefaultStorePath returns the platform-appropriate path to the Echo settings
// file (the shared Echo app data file under the user's config directory).
func DefaultStorePath() (string, error) {
	return appdata.DefaultStorePath()
}

// Store loads and saves Echo settings to the shared app data file on disk.
type Store struct {
	data *appdata.Store
}

// NewStore creates a Store that reads and writes settings within the app data
// file at path.
func NewStore(path string) *Store {
	return &Store{data: appdata.NewStore(path)}
}

// Path returns the app data file path this store uses.
func (s *Store) Path() string {
	return s.data.Path()
}

// Load reads the settings from the app data file. If the file does not exist
// it returns the default settings without error. Settings are normalized with
// endpoint profiles treated as the source of truth.
func (s *Store) Load() (llm.Settings, error) {
	f, err := s.data.Load()
	if err != nil {
		return llm.Settings{}, err
	}
	if len(f.Settings) == 0 {
		return llm.DefaultSettings(), nil
	}
	var settings llm.Settings
	if err := json.Unmarshal(f.Settings, &settings); err != nil {
		return llm.Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	return settings.NormalizedEndpointProfiles(), nil
}

// Save writes the settings into the shared app data file, preserving any
// existing workspace list.
func (s *Store) Save(settings llm.Settings) error {
	settings = settings.NormalizedEndpointProfiles()
	f, err := s.data.Load()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	f.Settings = raw
	return s.data.Save(f)
}
