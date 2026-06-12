package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	FileChangeCreated = "created"
	FileChangeEdited  = "edited"
	FileChangeDeleted = "deleted"
)

var ignoredChangePathNames = map[string]bool{
	".cache":       true,
	".git":         true,
	".next":        true,
	".vite":        true,
	"bin":          true,
	"build":        true,
	"coverage":     true,
	"dist":         true,
	"node_modules": true,
	"obj":          true,
	"target":       true,
}

type FileChange struct {
	Path      string        `json:"path"`
	Operation string        `json:"operation"`
	Before    *FileSnapshot `json:"before,omitempty"`
	After     *FileSnapshot `json:"after,omitempty"`
}

type FileSnapshot struct {
	Path          string `json:"path"`
	Exists        bool   `json:"exists"`
	Bytes         int64  `json:"bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Text          string `json:"text,omitempty"`
	TextAvailable bool   `json:"textAvailable,omitempty"`
	Binary        bool   `json:"binary,omitempty"`
	Large         bool   `json:"large,omitempty"`
}

type workspaceSnapshot map[string]FileSnapshot

func IsIgnoredChangePath(path string) bool {
	path = strings.TrimSpace(filepath.ToSlash(path))
	if path == "" || path == "." {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if ignoredChangePathNames[strings.ToLower(part)] {
			return true
		}
	}
	return false
}

func snapshotWorkspaceChanges(ctx context.Context, execution ExecutionContext) (workspaceSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	roots := execution.workspaceRoots()
	if len(roots) == 0 {
		return nil, SafeError{Code: "missing_workspace", Message: "workspace path is required"}
	}
	snapshot := workspaceSnapshot{}
	for _, workspaceRoot := range roots {
		root, err := workspaceRootAbsolutePath(workspaceRoot)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if path == root {
				return nil
			}
			relative := relativeWorkspacePath(execution, path)
			if IsIgnoredChangePath(relative) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			fileSnapshot, err := readFileSnapshot(execution, path, info)
			if err != nil {
				return nil
			}
			snapshot[fileSnapshot.Path] = *fileSnapshot
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func diffWorkspaceSnapshots(before, after workspaceSnapshot) []FileChange {
	if len(before) == 0 && len(after) == 0 {
		return nil
	}
	paths := make(map[string]bool, len(before)+len(after))
	for path := range before {
		paths[path] = true
	}
	for path := range after {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	changes := make([]FileChange, 0)
	for _, path := range ordered {
		if IsIgnoredChangePath(path) {
			continue
		}
		beforeSnapshot, hadBefore := before[path]
		afterSnapshot, hasAfter := after[path]
		switch {
		case !hadBefore && hasAfter:
			afterCopy := afterSnapshot
			changes = append(changes, FileChange{Path: path, Operation: FileChangeCreated, After: &afterCopy})
		case hadBefore && !hasAfter:
			beforeCopy := beforeSnapshot
			changes = append(changes, FileChange{Path: path, Operation: FileChangeDeleted, Before: &beforeCopy})
		case hadBefore && hasAfter && beforeSnapshot.SHA256 != afterSnapshot.SHA256:
			beforeCopy := beforeSnapshot
			afterCopy := afterSnapshot
			changes = append(changes, FileChange{Path: path, Operation: FileChangeEdited, Before: &beforeCopy, After: &afterCopy})
		}
	}
	return changes
}

func fileChangeForPath(ctx ExecutionContext, absolutePath string, before *FileSnapshot, after *FileSnapshot) FileChange {
	path := ""
	if after != nil {
		path = after.Path
	} else if before != nil {
		path = before.Path
	} else {
		path = relativeWorkspacePath(ctx, absolutePath)
	}
	operation := FileChangeEdited
	switch {
	case before == nil && after != nil:
		operation = FileChangeCreated
	case before != nil && after == nil:
		operation = FileChangeDeleted
	}
	return FileChange{
		Path:      path,
		Operation: operation,
		Before:    before,
		After:     after,
	}
}

func snapshotExistingFile(ctx ExecutionContext, absolutePath string) (*FileSnapshot, error) {
	info, err := os.Stat(absolutePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	return readFileSnapshot(ctx, absolutePath, info)
}

func readFileSnapshot(ctx ExecutionContext, absolutePath string, info os.FileInfo) (*FileSnapshot, error) {
	path := relativeWorkspacePath(ctx, absolutePath)
	snapshot := &FileSnapshot{
		Path:   path,
		Exists: true,
		Bytes:  info.Size(),
	}
	if info.Size() > maxTextFileBytes {
		hash, err := hashFile(absolutePath)
		if err != nil {
			return nil, err
		}
		snapshot.SHA256 = hash
		snapshot.Large = true
		return snapshot, nil
	}

	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	snapshot.SHA256 = hex.EncodeToString(sum[:])
	if !isTextLike(data) || !utf8.Valid(data) {
		snapshot.Binary = true
		return snapshot, nil
	}
	snapshot.Text = string(data)
	snapshot.TextAvailable = true
	return snapshot, nil
}

func snapshotHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func changeSnapshotsEqual(before *FileSnapshot, after *FileSnapshot) bool {
	if before == nil && after == nil {
		return true
	}
	if before == nil || after == nil {
		return false
	}
	return before.Exists == after.Exists && before.SHA256 != "" && before.SHA256 == after.SHA256
}

func collectShellFileChanges(ctx context.Context, execution ExecutionContext, run func() error) ([]FileChange, error) {
	before, beforeErr := snapshotWorkspaceChanges(ctx, execution)
	runErr := run()
	after, afterErr := snapshotWorkspaceChanges(ctx, execution)
	var changes []FileChange
	if beforeErr == nil && afterErr == nil {
		changes = diffWorkspaceSnapshots(before, after)
	}
	if runErr != nil {
		return changes, runErr
	}
	return changes, errors.Join(beforeErr, afterErr)
}
