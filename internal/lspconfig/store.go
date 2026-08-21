package lspconfig

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/brent/echo/internal/appdata"
)

type Store struct {
	data *appdata.Store
}

func NewStore(data *appdata.Store) *Store {
	return &Store{data: data}
}

func (s *Store) Load() ([]Profile, error) {
	file, err := s.data.Load()
	if err != nil {
		return nil, err
	}
	if len(file.LanguageServers) == 0 || bytes.Equal(bytes.TrimSpace(file.LanguageServers), []byte("null")) {
		return []Profile{}, nil
	}
	var profiles []Profile
	if err := json.Unmarshal(file.LanguageServers, &profiles); err != nil {
		return nil, fmt.Errorf("parse language server profiles: %w", err)
	}
	return NormalizeProfiles(profiles)
}

func (s *Store) Save(profiles []Profile) ([]Profile, error) {
	profiles, err := NormalizeProfiles(profiles)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(profiles)
	if err != nil {
		return nil, fmt.Errorf("marshal language server profiles: %w", err)
	}
	if err := s.data.Update(func(file *appdata.File) error {
		file.LanguageServers = raw
		return nil
	}); err != nil {
		return nil, err
	}
	return profiles, nil
}

func (s *Store) Add(profile Profile) (Profile, error) {
	profile = profile.Normalized()
	profiles, err := s.mutate(func(profiles []Profile) ([]Profile, error) {
		for _, existing := range profiles {
			if existing.ID == profile.ID {
				return nil, fmt.Errorf("language server profile %q already exists", profile.ID)
			}
		}
		return append(profiles, profile), nil
	})
	if err != nil {
		return Profile{}, err
	}
	for _, saved := range profiles {
		if saved.ID == profile.ID {
			return saved, nil
		}
	}
	return Profile{}, fmt.Errorf("language server profile %q was not saved", profile.ID)
}

func (s *Store) Update(id string, profile Profile) (Profile, error) {
	return s.UpdateChecked(id, profile, nil)
}

// UpdateChecked validates the complete prospective profile list before it is
// committed. The callback runs inside the app-data transaction.
func (s *Store) UpdateChecked(id string, profile Profile, validate func([]Profile) error) (Profile, error) {
	id = profileID(id)
	profile.ID = id
	profiles, err := s.mutate(func(profiles []Profile) ([]Profile, error) {
		for index := range profiles {
			if profiles[index].ID == id {
				profiles[index] = profile
				prospective, err := NormalizeProfiles(profiles)
				if err != nil {
					return nil, err
				}
				if validate != nil {
					if err := validate(prospective); err != nil {
						return nil, err
					}
				}
				return prospective, nil
			}
		}
		return nil, fmt.Errorf("language server profile %q was not found", id)
	})
	if err != nil {
		return Profile{}, err
	}
	for _, saved := range profiles {
		if saved.ID == id {
			return saved, nil
		}
	}
	return Profile{}, fmt.Errorf("language server profile %q was not found", id)
}

func (s *Store) Delete(id string) error {
	id = profileID(id)
	_, err := s.mutate(func(profiles []Profile) ([]Profile, error) {
		result := profiles[:0]
		found := false
		for _, profile := range profiles {
			if profile.ID == id {
				found = true
				continue
			}
			result = append(result, profile)
		}
		if !found {
			return nil, fmt.Errorf("language server profile %q was not found", id)
		}
		return result, nil
	})
	return err
}

func (s *Store) mutate(update func([]Profile) ([]Profile, error)) ([]Profile, error) {
	var saved []Profile
	err := s.data.Update(func(file *appdata.File) error {
		var profiles []Profile
		if len(file.LanguageServers) > 0 && !bytes.Equal(bytes.TrimSpace(file.LanguageServers), []byte("null")) {
			if err := json.Unmarshal(file.LanguageServers, &profiles); err != nil {
				return fmt.Errorf("parse language server profiles: %w", err)
			}
		}
		var err error
		profiles, err = NormalizeProfiles(profiles)
		if err != nil {
			return err
		}
		profiles, err = update(profiles)
		if err != nil {
			return err
		}
		profiles, err = NormalizeProfiles(profiles)
		if err != nil {
			return err
		}
		file.LanguageServers, err = json.Marshal(profiles)
		if err != nil {
			return fmt.Errorf("marshal language server profiles: %w", err)
		}
		saved = make([]Profile, len(profiles))
		for index, profile := range profiles {
			saved[index] = profile.Clone()
		}
		return nil
	})
	return saved, err
}

func profileID(value string) string {
	return Profile{ID: value}.Normalized().ID
}
