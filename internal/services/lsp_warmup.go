package services

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	lspWarmupDelay             = 750 * time.Millisecond
	lspWarmupMaxScannedEntries = 20000
)

type lspWarmupTarget struct {
	folder     WorkspaceFolder
	languageID string
}

func (s *SystemService) warmActiveWorkspaceLSPClients(state AppState) {
	if s.ctx == nil || state.ActiveWorkspaceID == "" {
		return
	}
	for _, workspace := range state.Workspaces {
		if workspace.ID == state.ActiveWorkspaceID {
			s.warmWorkspaceLSPClientsAsync(workspace.ID)
			return
		}
	}
}

func (s *SystemService) warmWorkspaceLSPClientsAsync(workspaceID string) {
	if s.ctx == nil || workspaceID == "" {
		return
	}
	s.lspMu.Lock()
	if _, running := s.lspWarmups[workspaceID]; running {
		s.lspMu.Unlock()
		return
	}
	s.lspWarmups[workspaceID] = struct{}{}
	s.lspMu.Unlock()

	ctx := s.ctx
	go func() {
		defer s.finishLSPWarmup(workspaceID)

		timer := time.NewTimer(lspWarmupDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}

		workspace, ok := s.workspaceForLSPWarmup(workspaceID)
		if !ok {
			return
		}
		targets := workspaceLSPWarmupTargets(workspace)
		for _, target := range targets {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, _ = s.workspaceLSPClient(workspace, target.folder, target.languageID)
		}
	}()
}

func (s *SystemService) finishLSPWarmup(workspaceID string) {
	s.lspMu.Lock()
	delete(s.lspWarmups, workspaceID)
	s.lspMu.Unlock()
}

func (s *SystemService) workspaceForLSPWarmup(workspaceID string) (Workspace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, workspace := range s.state.Workspaces {
		if workspace.ID != workspaceID || workspace.Missing {
			continue
		}
		workspace.Folders = append([]WorkspaceFolder(nil), workspace.Folders...)
		return workspace, true
	}
	return Workspace{}, false
}

func workspaceLSPWarmupTargets(workspace Workspace) []lspWarmupTarget {
	targets := []lspWarmupTarget{}
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		root, err := workspaceFolderAbsolutePath(folder)
		if err != nil {
			continue
		}
		languageIDs := detectWorkspaceFolderLSPLanguages(root)
		for _, languageID := range languageIDs {
			targets = append(targets, lspWarmupTarget{
				folder:     folder,
				languageID: languageID,
			})
		}
	}
	return targets
}

func detectWorkspaceFolderLSPLanguages(root string) []string {
	definitions := registeredLSPLanguageDefinitions()
	if len(definitions) == 0 {
		return nil
	}
	found := map[string]bool{}
	ignoreMatcher := newWorkspaceIgnoreMatcher(root)
	for _, definition := range definitions {
		for _, marker := range definition.WorkspaceMarkers {
			if workspaceMarkerExists(root, marker) {
				found[definition.ID] = true
				break
			}
		}
	}
	if len(found) == len(definitions) {
		return sortedLanguageIDs(found)
	}

	scanned := 0
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || path == root {
			return nil
		}
		scanned++
		if scanned > lspWarmupMaxScannedEntries {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			relative, err := filepath.Rel(root, path)
			if err == nil && ignoreMatcher.ignores(filepath.ToSlash(relative), true) {
				return filepath.SkipDir
			}
			return nil
		}
		if languageID, ok := lspLanguageIDForPath(entry.Name()); ok {
			found[languageID] = true
			if len(found) == len(definitions) {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return sortedLanguageIDs(found)
}

func workspaceMarkerExists(root string, marker string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(marker)))
	return err == nil && !info.IsDir()
}

func registeredLSPLanguageDefinitions() []lspLanguageDefinition {
	definitions := make([]lspLanguageDefinition, 0, len(lspLanguagesByID))
	for _, definition := range lspLanguagesByID {
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].ID < definitions[j].ID
	})
	return definitions
}

func sortedLanguageIDs(found map[string]bool) []string {
	languageIDs := make([]string, 0, len(found))
	for languageID := range found {
		languageIDs = append(languageIDs, languageID)
	}
	sort.Strings(languageIDs)
	return languageIDs
}
