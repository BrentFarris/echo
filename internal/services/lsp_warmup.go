package services

import (
	"context"
	"errors"
	"go/build"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	lspWarmupDelay             = 750 * time.Millisecond
	lspWarmupMaxScannedEntries = 20000
	lspWarmupMaxGoBuilds       = 8
	lspWarmupConcurrency       = 2
)

type lspWarmupRun struct {
	cancel context.CancelFunc
}

type lspWarmupTarget struct {
	folder      WorkspaceFolder
	languageID  string
	sourcePaths []string
}

type lspWarmupScan struct {
	found        map[string]bool
	sourcePaths  map[string][]string
	goBuildRoots []string
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
	ctx, cancel := context.WithCancel(s.ctx)
	run := &lspWarmupRun{cancel: cancel}
	s.lspMu.Lock()
	if _, running := s.lspWarmups[workspaceID]; running {
		s.lspMu.Unlock()
		cancel()
		return
	}
	s.lspWarmups[workspaceID] = run
	s.lspMu.Unlock()

	go func() {
		defer cancel()
		defer s.finishLSPWarmup(workspaceID, run)

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
		s.warmWorkspaceLSPClients(ctx, workspace)
	}()
}

func (s *SystemService) finishLSPWarmup(workspaceID string, run *lspWarmupRun) {
	s.lspMu.Lock()
	if s.lspWarmups[workspaceID] == run {
		delete(s.lspWarmups, workspaceID)
	}
	s.lspMu.Unlock()
}

func (s *SystemService) warmWorkspaceLSPClients(ctx context.Context, workspace Workspace) {
	targets := workspaceLSPWarmupTargets(workspace)
	semaphore := make(chan struct{}, lspWarmupConcurrency)
	var wait sync.WaitGroup
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		target := target
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			s.warmWorkspaceLSPTarget(ctx, workspace, target)
		}()
	}
	wait.Wait()
}

func (s *SystemService) warmWorkspaceLSPTarget(ctx context.Context, workspace Workspace, target lspWarmupTarget) {
	started := time.Now()
	client, err := s.workspaceLSPClient(workspace, target.folder, target.languageID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Debug("language server warmup could not start", "workspace", workspace.DisplayName, "language", target.languageID, "error", err)
		}
		return
	}
	for _, sourcePath := range target.sourcePaths {
		if ctx.Err() != nil {
			return
		}
		file, readErr := readWorkspaceTextFile(workspace, sourcePath)
		if readErr != nil {
			slog.Debug("language server warmup could not read source", "workspace", workspace.DisplayName, "language", target.languageID, "path", workspaceRelativePath(workspace, sourcePath), "error", readErr)
			continue
		}
		primed, primeErr := client.primeDocument(ctx, sourcePath, file.Content)
		if primeErr != nil {
			if !errors.Is(primeErr, context.Canceled) {
				slog.Warn("language server warmup failed", "workspace", workspace.DisplayName, "language", target.languageID, "path", file.Path, "duration", time.Since(started), "error", primeErr)
			}
			return
		}
		if primed {
			slog.Debug("language server document primed", "workspace", workspace.DisplayName, "language", target.languageID, "path", file.Path, "duration", time.Since(started))
		}
	}
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
		scan := scanWorkspaceFolderLSPWarmup(root)
		languageIDs := sortedLanguageIDs(scan.found)
		for _, languageID := range languageIDs {
			sourcePaths := append([]string(nil), scan.sourcePaths[languageID]...)
			if languageID == "go" {
				sourcePaths = goWarmupSourcePaths(scan.sourcePaths[languageID], scan.goBuildRoots)
			} else if len(sourcePaths) > 1 {
				sourcePaths = sourcePaths[:1]
			}
			targets = append(targets, lspWarmupTarget{
				folder:      folder,
				languageID:  languageID,
				sourcePaths: sourcePaths,
			})
		}
	}
	return targets
}

func detectWorkspaceFolderLSPLanguages(root string) []string {
	return sortedLanguageIDs(scanWorkspaceFolderLSPWarmup(root).found)
}

func scanWorkspaceFolderLSPWarmup(root string) lspWarmupScan {
	definitions := registeredLSPLanguageDefinitions()
	scan := lspWarmupScan{
		found:       map[string]bool{},
		sourcePaths: map[string][]string{},
	}
	if len(definitions) == 0 {
		return scan
	}
	ignoreMatcher := newWorkspaceIgnoreMatcher(root)
	goBuildRoots := map[string]bool{}
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
		relative, err := filepath.Rel(root, path)
		if err == nil {
			relative = filepath.ToSlash(relative)
			for _, definition := range definitions {
				for _, marker := range definition.WorkspaceMarkers {
					if lspWorkspaceMarkerMatches(relative, marker) {
						scan.found[definition.ID] = true
						if definition.ID == "go" && (strings.EqualFold(entry.Name(), "go.mod") || strings.EqualFold(entry.Name(), "go.work")) {
							goBuildRoots[filepath.Clean(filepath.Dir(path))] = true
						}
						break
					}
				}
			}
		}
		if languageID, ok := lspLanguageIDForPath(entry.Name()); ok {
			scan.found[languageID] = true
			scan.sourcePaths[languageID] = append(scan.sourcePaths[languageID], filepath.Clean(path))
		}
		return nil
	})
	for buildRoot := range goBuildRoots {
		scan.goBuildRoots = append(scan.goBuildRoots, buildRoot)
	}
	sort.Strings(scan.goBuildRoots)
	for languageID := range scan.sourcePaths {
		sort.Strings(scan.sourcePaths[languageID])
	}
	return scan
}

func lspWorkspaceMarkerMatches(relative string, marker string) bool {
	relative = strings.Trim(strings.ReplaceAll(relative, "\\", "/"), "/")
	marker = strings.Trim(strings.ReplaceAll(marker, "\\", "/"), "/")
	return strings.EqualFold(relative, marker) || strings.HasSuffix(strings.ToLower(relative), "/"+strings.ToLower(marker))
}

func goWarmupSourcePaths(sourcePaths []string, buildRoots []string) []string {
	if len(sourcePaths) == 0 {
		return nil
	}
	sources := append([]string(nil), sourcePaths...)
	roots := append([]string(nil), buildRoots...)
	sort.Strings(sources)
	sort.Strings(roots)
	preferredByRoot := map[string]string{}
	fallbackByRoot := map[string]string{}
	for _, source := range sources {
		nearest := ""
		for _, root := range roots {
			if pathWithinRoot(root, source) && len(root) > len(nearest) {
				nearest = root
			}
		}
		if nearest == "" {
			continue
		}
		if fallbackByRoot[nearest] == "" {
			fallbackByRoot[nearest] = source
		}
		if preferredByRoot[nearest] == "" && goWarmupSourceMatchesCurrentBuild(source) {
			preferredByRoot[nearest] = source
		}
	}
	selected := make([]string, 0, min(len(roots), lspWarmupMaxGoBuilds))
	seen := map[string]bool{}
	for _, root := range roots {
		source := preferredByRoot[root]
		if source == "" {
			source = fallbackByRoot[root]
		}
		if source != "" && !seen[source] {
			selected = append(selected, source)
			seen[source] = true
			if len(selected) == lspWarmupMaxGoBuilds {
				break
			}
		}
	}
	if len(selected) == 0 {
		for _, source := range sources {
			if goWarmupSourceMatchesCurrentBuild(source) {
				selected = append(selected, source)
				break
			}
		}
		if len(selected) == 0 {
			selected = append(selected, sources[0])
		}
	}
	return selected
}

func goWarmupSourceMatchesCurrentBuild(path string) bool {
	if strings.HasSuffix(strings.ToLower(filepath.Base(path)), "_test.go") {
		return false
	}
	matches, err := build.Default.MatchFile(filepath.Dir(path), filepath.Base(path))
	return err == nil && matches
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
