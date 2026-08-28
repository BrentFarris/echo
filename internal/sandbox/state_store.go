package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var safeIDPattern = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

type StateStore struct {
	root string
	mu   sync.Mutex
}

func NewStateStore(root string) *StateStore { return &StateStore{root: filepath.Clean(root)} }

func (s *StateStore) Root() string { return s.root }

func (s *StateStore) workspaceDir(workspaceID string) (string, error) {
	clean := strings.Trim(safeIDPattern.ReplaceAllString(strings.TrimSpace(workspaceID), "-"), "-.")
	if clean == "" {
		return "", fmt.Errorf("workspace id is invalid")
	}
	return filepath.Join(s.root, clean), nil
}

func (s *StateStore) Load(workspaceID string) (MachineState, bool, error) {
	directory, err := s.workspaceDir(workspaceID)
	if err != nil {
		return MachineState{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MachineState{}, false, nil
		}
		return MachineState{}, false, fmt.Errorf("read sandbox state: %w", err)
	}
	var state MachineState
	if err := json.Unmarshal(data, &state); err != nil {
		return MachineState{}, true, fmt.Errorf("parse sandbox state: %w", err)
	}
	if state.WorkspaceID != workspaceID {
		return MachineState{}, true, fmt.Errorf("sandbox state belongs to a different workspace")
	}
	return state, true, nil
}

func (s *StateStore) Save(state MachineState) error {
	directory, err := s.workspaceDir(state.WorkspaceID)
	if err != nil {
		return err
	}
	state.Version = 1
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sandbox state: %w", err)
	}
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create sandbox state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create sandbox state file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write sandbox state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync sandbox state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close sandbox state: %w", err)
	}
	if err := replaceFile(temporaryPath, filepath.Join(directory, "state.json")); err != nil {
		return fmt.Errorf("replace sandbox state: %w", err)
	}
	return nil
}

func (s *StateStore) Delete(workspaceID string) error {
	directory, err := s.workspaceDir(workspaceID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rootAbsolute, err := filepath.Abs(s.root)
	if err != nil {
		return err
	}
	directoryAbsolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbsolute, directoryAbsolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to delete sandbox state outside its root")
	}
	if err := os.RemoveAll(directoryAbsolute); err != nil {
		return fmt.Errorf("delete sandbox state: %w", err)
	}
	return nil
}
