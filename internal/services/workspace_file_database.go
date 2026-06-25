package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	workspaceFileDatabaseVersion = 1
	workspaceFileDatabaseName    = "files-v1.json"
	workspaceFileDatabaseMaxAge  = 30 * time.Second
)

type workspaceFileDatabase struct {
	Version     int                          `json:"version"`
	FolderID    string                       `json:"folderId"`
	FolderLabel string                       `json:"folderLabel"`
	RootPath    string                       `json:"rootPath"`
	GeneratedAt string                       `json:"generatedAt"`
	Entries     []workspaceFileDatabaseEntry `json:"entries"`
}

type workspaceFileDatabaseEntry struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	RootRelative string `json:"rootRelative"`
	Kind         string `json:"kind"`
	Bytes        int64  `json:"bytes,omitempty"`
	ModifiedAt   string `json:"modifiedAt"`
	Ignored      bool   `json:"ignored,omitempty"`
}

func searchWorkspaceFilesWithDatabase(workspace Workspace, query string, includeIgnored bool, limit int) ([]WorkspaceFileEntry, bool, error) {
	output := make([]WorkspaceFileEntry, 0)
	truncated := false
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		database, err := loadOrBuildWorkspaceFileDatabase(folder)
		if err != nil {
			return nil, false, err
		}
		for _, entry := range database.Entries {
			if !includeIgnored && entry.Ignored {
				continue
			}
			_, relative, name, ok := workspaceFileDatabaseEntryTarget(database, entry)
			if !ok || !workspaceSearchMatches(query, name, relative) {
				continue
			}
			current, ok := currentWorkspaceFileEntry(database, entry)
			if !ok {
				continue
			}
			output = append(output, current)
		}
	}
	sortWorkspaceFileEntries(output)
	if len(output) > limit {
		output = output[:limit]
		truncated = true
	}
	return output, truncated, nil
}

func loadOrBuildWorkspaceFileDatabase(folder WorkspaceFolder) (workspaceFileDatabase, error) {
	database, ok, err := loadFreshWorkspaceFileDatabase(folder)
	if err != nil {
		return workspaceFileDatabase{}, err
	}
	if ok {
		return database, nil
	}
	return rebuildWorkspaceFileDatabase(folder)
}

func loadFreshWorkspaceFileDatabase(folder WorkspaceFolder) (workspaceFileDatabase, bool, error) {
	path, err := workspaceFileDatabasePath(folder)
	if err != nil {
		return workspaceFileDatabase{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return workspaceFileDatabase{}, false, nil
		}
		return workspaceFileDatabase{}, false, fmt.Errorf("read workspace file database: %w", err)
	}
	var database workspaceFileDatabase
	if err := json.Unmarshal(data, &database); err != nil {
		return workspaceFileDatabase{}, false, nil
	}
	if !workspaceFileDatabaseMatchesFolder(database, folder) {
		return workspaceFileDatabase{}, false, nil
	}
	generatedAt, err := time.Parse(time.RFC3339Nano, database.GeneratedAt)
	if err != nil {
		return workspaceFileDatabase{}, false, nil
	}
	if time.Since(generatedAt) > workspaceFileDatabaseMaxAge {
		return workspaceFileDatabase{}, false, nil
	}
	return database, true, nil
}

func rebuildWorkspaceFileDatabase(folder WorkspaceFolder) (workspaceFileDatabase, error) {
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return workspaceFileDatabase{}, err
	}
	database := workspaceFileDatabase{
		Version:     workspaceFileDatabaseVersion,
		FolderID:    folder.ID,
		FolderLabel: folder.Label,
		RootPath:    root,
		GeneratedAt: formatWorkspaceModifiedAt(time.Now()),
		Entries:     []workspaceFileDatabaseEntry{},
	}
	ignoredDirectories := map[string]bool{}
	err = filepath.WalkDir(root, func(absolute string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || absolute == root {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() && strings.EqualFold(name, workspaceCacheDirName) {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil {
			return nil
		}
		rootRelative := filepath.ToSlash(relative)
		parentIgnored := ignoredDirectories[filepath.Dir(absolute)]
		ignored := parentIgnored || isIgnoredWorkspaceDirectory(name)
		if entry.IsDir() && ignored {
			ignoredDirectories[absolute] = true
		}
		database.Entries = append(database.Entries, workspaceFileDatabaseEntry{
			Name:         name,
			Path:         folder.Label + "/" + rootRelative,
			RootRelative: rootRelative,
			Kind:         workspaceFileKind(info),
			Bytes:        info.Size(),
			ModifiedAt:   formatWorkspaceModifiedAt(info.ModTime()),
			Ignored:      ignored,
		})
		return nil
	})
	if err != nil {
		return workspaceFileDatabase{}, fmt.Errorf("build workspace file database: %w", err)
	}
	sort.Slice(database.Entries, func(i, j int) bool {
		left := database.Entries[i]
		right := database.Entries[j]
		if left.Kind != right.Kind {
			return left.Kind == "directory"
		}
		return strings.ToLower(left.Path) < strings.ToLower(right.Path)
	})
	if err := writeWorkspaceFileDatabase(folder, database); err != nil {
		return workspaceFileDatabase{}, err
	}
	return database, nil
}

func writeWorkspaceFileDatabase(folder WorkspaceFolder, database workspaceFileDatabase) error {
	path, err := workspaceFileDatabasePath(folder)
	if err != nil {
		return err
	}
	data, err := json.Marshal(database)
	if err != nil {
		return fmt.Errorf("encode workspace file database: %w", err)
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, workspaceCacheFilePermission); err != nil {
		return fmt.Errorf("write workspace file database: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace workspace file database: %w", err)
	}
	return nil
}

func workspaceFileDatabasePath(folder WorkspaceFolder) (string, error) {
	return workspaceFileDatabaseCachePath(folder, workspaceFileDatabaseName)
}

func workspaceFileDatabaseExistingPath(folder WorkspaceFolder) (string, error) {
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, workspaceCacheDirName, workspaceFileDatabaseDirName, workspaceFileDatabaseName), nil
}

func (s *SystemService) removeWorkspaceFileDatabases(workspaceID string) {
	workspace, err := s.workspaceByID(workspaceID)
	if err != nil {
		return
	}
	removeWorkspaceFileDatabases(workspace)
}

func removeWorkspaceFileDatabases(workspace Workspace) {
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		path, err := workspaceFileDatabaseExistingPath(folder)
		if err == nil {
			_ = os.Remove(path)
		}
	}
}

func workspaceFileDatabaseMatchesFolder(database workspaceFileDatabase, folder WorkspaceFolder) bool {
	if database.Version != workspaceFileDatabaseVersion || database.FolderID != folder.ID || database.FolderLabel != folder.Label {
		return false
	}
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(database.RootPath), filepath.Clean(root))
}

func currentWorkspaceFileEntry(database workspaceFileDatabase, entry workspaceFileDatabaseEntry) (WorkspaceFileEntry, bool) {
	absolute, relative, name, ok := workspaceFileDatabaseEntryTarget(database, entry)
	if !ok {
		return WorkspaceFileEntry{}, false
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return WorkspaceFileEntry{}, false
	}
	return WorkspaceFileEntry{
		Name:       name,
		Path:       relative,
		Kind:       workspaceFileKind(info),
		Bytes:      info.Size(),
		ModifiedAt: formatWorkspaceModifiedAt(info.ModTime()),
	}, true
}

func workspaceFileDatabaseEntryTarget(database workspaceFileDatabase, entry workspaceFileDatabaseEntry) (string, string, string, bool) {
	if strings.TrimSpace(entry.RootRelative) == "" || filepath.IsAbs(entry.RootRelative) || filepath.VolumeName(entry.RootRelative) != "" {
		return "", "", "", false
	}
	absolute := filepath.Clean(filepath.Join(database.RootPath, filepath.FromSlash(entry.RootRelative)))
	relative, err := filepath.Rel(database.RootPath, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", "", false
	}
	rootRelative := filepath.ToSlash(relative)
	return absolute, database.FolderLabel + "/" + rootRelative, filepath.Base(absolute), true
}
