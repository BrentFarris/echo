package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const debugPersistentStateVersion = 1

type debugPersistentState struct {
	Version    int                                           `json:"version"`
	Workspaces map[string]map[string][]DebugSourceBreakpoint `json:"workspaces"`
}

func debugPersistentStatePath(storePath string) string {
	return filepath.Join(filepath.Dir(storePath), "debug.json")
}

func loadDebugPersistentState(path string) debugPersistentState {
	state := debugPersistentState{
		Version:    debugPersistentStateVersion,
		Workspaces: make(map[string]map[string][]DebugSourceBreakpoint),
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1024*1024 {
		return state
	}
	var decoded debugPersistentState
	if json.Unmarshal(data, &decoded) != nil || decoded.Version != debugPersistentStateVersion {
		return state
	}
	if decoded.Workspaces != nil {
		state.Workspaces = decoded.Workspaces
	}
	return state
}

func writeDebugPersistentState(path string, state debugPersistentState) error {
	state.Version = debugPersistentStateVersion
	if state.Workspaces == nil {
		state.Workspaces = make(map[string]map[string][]DebugSourceBreakpoint)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode debug state: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create debug state directory: %w", err)
	}
	temp, err := os.CreateTemp(directory, ".debug-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary debug state: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure debug state: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary debug state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary debug state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary debug state: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace debug state: %w", err)
	}
	return nil
}

func cloneDebugPersistentState(state debugPersistentState) debugPersistentState {
	clone := debugPersistentState{
		Version:    debugPersistentStateVersion,
		Workspaces: make(map[string]map[string][]DebugSourceBreakpoint, len(state.Workspaces)),
	}
	for workspaceID, sources := range state.Workspaces {
		clonedSources := make(map[string][]DebugSourceBreakpoint, len(sources))
		for path, breakpoints := range sources {
			clonedSources[path] = append([]DebugSourceBreakpoint(nil), breakpoints...)
		}
		clone.Workspaces[workspaceID] = clonedSources
	}
	return clone
}
