package debugconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/appdata"
)

var ErrProfileInUse = errors.New("debug adapter profile is in use")

type ProfileStore struct{ data *appdata.Store }

func NewProfileStore(data *appdata.Store) *ProfileStore { return &ProfileStore{data: data} }

func (s *ProfileStore) Profiles() ([]AdapterProfile, error) {
	file, err := s.data.Load()
	if err != nil {
		return nil, err
	}
	if len(file.DebugAdapters) == 0 {
		return nil, nil
	}
	var profiles []AdapterProfile
	if err := json.Unmarshal(file.DebugAdapters, &profiles); err != nil {
		return nil, fmt.Errorf("parse debug adapter profiles: %w", err)
	}
	return NormalizeProfiles(profiles)
}

func (s *ProfileStore) Save(profiles []AdapterProfile) error {
	profiles, err := NormalizeProfiles(profiles)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(profiles)
	if err != nil {
		return err
	}
	return s.data.Update(func(file *appdata.File) error { file.DebugAdapters = raw; return nil })
}

func (s *ProfileStore) Add(profile AdapterProfile) (AdapterProfile, error) {
	profile = profile.Normalized()
	if err := profile.Validate(); err != nil {
		return AdapterProfile{}, err
	}
	profiles, err := s.Profiles()
	if err != nil {
		return AdapterProfile{}, err
	}
	for _, existing := range profiles {
		if existing.ID == profile.ID {
			return AdapterProfile{}, fmt.Errorf("debug adapter profile %q already exists", profile.ID)
		}
	}
	profiles = append(profiles, profile)
	return profile, s.Save(profiles)
}

func (s *ProfileStore) AddTemplate(id string) (AdapterProfile, error) {
	profile, ok := TemplateByID(id)
	if !ok {
		return AdapterProfile{}, fmt.Errorf("debug adapter template %q was not found", id)
	}
	return s.Add(profile)
}

func (s *ProfileStore) Update(id string, profile AdapterProfile) (AdapterProfile, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	profile = profile.Normalized()
	if profile.ID != id {
		return AdapterProfile{}, fmt.Errorf("debug adapter profile id cannot be changed")
	}
	if err := profile.Validate(); err != nil {
		return AdapterProfile{}, err
	}
	profiles, err := s.Profiles()
	if err != nil {
		return AdapterProfile{}, err
	}
	for index := range profiles {
		if profiles[index].ID == id {
			profiles[index] = profile
			return profile, s.Save(profiles)
		}
	}
	return AdapterProfile{}, fmt.Errorf("debug adapter profile %q was not found", id)
}

func (s *ProfileStore) Delete(id string, inUse func(string) bool) error {
	id = strings.ToLower(strings.TrimSpace(id))
	profiles, err := s.Profiles()
	if err != nil {
		return err
	}
	if inUse != nil && inUse(id) {
		return fmt.Errorf("%w: %s", ErrProfileInUse, id)
	}
	for index, profile := range profiles {
		if profile.ID == id {
			return s.Save(append(profiles[:index], profiles[index+1:]...))
		}
	}
	return fmt.Errorf("debug adapter profile %q was not found", id)
}
