package services

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const maxWorkspaceEditorFileBytes = 1024 * 1024
const maxWorkspaceFileSearchResults = 200

type WorkspaceDirectory struct {
	WorkspaceID string               `json:"workspaceId"`
	Path        string               `json:"path"`
	Entries     []WorkspaceFileEntry `json:"entries"`
}

type WorkspaceFileEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Bytes      int64  `json:"bytes,omitempty"`
	ModifiedAt string `json:"modifiedAt"`
}

type WorkspaceFile struct {
	WorkspaceID string `json:"workspaceId"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Bytes       int64  `json:"bytes"`
	ModifiedAt  string `json:"modifiedAt"`
}

type WorkspaceFileSearchResult struct {
	WorkspaceID string               `json:"workspaceId"`
	Query       string               `json:"query"`
	Entries     []WorkspaceFileEntry `json:"entries"`
	Truncated   bool                 `json:"truncated"`
}

func (s *SystemService) ListWorkspaceDirectory(workspaceID string, path string) (WorkspaceDirectory, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceDirectory{}, err
	}
	resolved, err := resolveWorkspaceServicePath(workspace.FolderPath, path)
	if err != nil {
		return WorkspaceDirectory{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceDirectory{}, fmt.Errorf("directory was not found")
	}
	if !info.IsDir() {
		return WorkspaceDirectory{}, fmt.Errorf("path is not a directory")
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return WorkspaceDirectory{}, fmt.Errorf("read directory: %w", err)
	}
	output := WorkspaceDirectory{
		WorkspaceID: workspace.ID,
		Path:        workspaceRelativePath(workspace.FolderPath, resolved),
		Entries:     make([]WorkspaceFileEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			return WorkspaceDirectory{}, fmt.Errorf("read directory entry: %w", err)
		}
		output.Entries = append(output.Entries, WorkspaceFileEntry{
			Name:       entry.Name(),
			Path:       workspaceRelativePath(workspace.FolderPath, filepath.Join(resolved, entry.Name())),
			Kind:       workspaceFileKind(entryInfo),
			Bytes:      entryInfo.Size(),
			ModifiedAt: formatWorkspaceModifiedAt(entryInfo.ModTime()),
		})
	}
	sort.Slice(output.Entries, func(i, j int) bool {
		left := output.Entries[i]
		right := output.Entries[j]
		if left.Kind != right.Kind {
			return left.Kind == "directory"
		}
		return strings.ToLower(left.Name) < strings.ToLower(right.Name)
	})
	return output, nil
}

func (s *SystemService) SearchWorkspaceFiles(workspaceID string, query string, includeIgnored bool) (WorkspaceFileSearchResult, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFileSearchResult{}, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	output := WorkspaceFileSearchResult{
		WorkspaceID: workspace.ID,
		Query:       query,
		Entries:     []WorkspaceFileEntry{},
	}
	if query == "" {
		return output, nil
	}
	root, err := resolveWorkspaceServicePath(workspace.FolderPath, ".")
	if err != nil {
		return WorkspaceFileSearchResult{}, err
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		relative := workspaceRelativePath(workspace.FolderPath, path)
		if entry.IsDir() && !includeIgnored && isIgnoredWorkspaceDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if !workspaceSearchMatches(query, entry.Name(), relative) {
			return nil
		}
		if len(output.Entries) >= maxWorkspaceFileSearchResults {
			output.Truncated = true
			return filepath.SkipAll
		}
		output.Entries = append(output.Entries, WorkspaceFileEntry{
			Name:       entry.Name(),
			Path:       relative,
			Kind:       workspaceFileKind(info),
			Bytes:      info.Size(),
			ModifiedAt: formatWorkspaceModifiedAt(info.ModTime()),
		})
		return nil
	})
	if err != nil {
		return WorkspaceFileSearchResult{}, fmt.Errorf("search workspace: %w", err)
	}
	sort.Slice(output.Entries, func(i, j int) bool {
		left := output.Entries[i]
		right := output.Entries[j]
		if left.Kind != right.Kind {
			return left.Kind == "directory"
		}
		return strings.ToLower(left.Path) < strings.ToLower(right.Path)
	})
	return output, nil
}

func (s *SystemService) ReadWorkspaceFile(workspaceID string, path string) (WorkspaceFile, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFile{}, err
	}
	if strings.TrimSpace(path) == "" {
		return WorkspaceFile{}, fmt.Errorf("path is required")
	}
	resolved, err := resolveWorkspaceServicePath(workspace.FolderPath, path)
	if err != nil {
		return WorkspaceFile{}, err
	}
	return readWorkspaceTextFile(workspace, resolved)
}

func (s *SystemService) ResolveWorkspaceTextFilePath(workspaceID string, path string) (string, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return "", err
	}
	path = cleanWorkspacePathCandidate(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		relative, err := workspaceRelativeCandidate(workspace.FolderPath, path)
		if err != nil {
			return "", err
		}
		path = relative
	}
	resolved, err := resolveWorkspaceServicePath(workspace.FolderPath, path)
	if err != nil {
		return "", err
	}
	file, err := readWorkspaceTextFile(workspace, resolved)
	if err != nil {
		return "", err
	}
	return file.Path, nil
}

func (s *SystemService) SaveWorkspaceFile(workspaceID string, path string, content string, expectedModifiedAt string) (WorkspaceFile, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFile{}, err
	}
	if strings.TrimSpace(path) == "" {
		return WorkspaceFile{}, fmt.Errorf("path is required")
	}
	if strings.TrimSpace(expectedModifiedAt) == "" {
		return WorkspaceFile{}, fmt.Errorf("expected modified timestamp is required")
	}
	if len([]byte(content)) > maxWorkspaceEditorFileBytes {
		return WorkspaceFile{}, fmt.Errorf("content is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	if !utf8.ValidString(content) {
		return WorkspaceFile{}, fmt.Errorf("file content must be valid UTF-8")
	}

	resolved, err := resolveWorkspaceServicePath(workspace.FolderPath, path)
	if err != nil {
		return WorkspaceFile{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceFile{}, fmt.Errorf("file was not found")
	}
	if !info.Mode().IsRegular() {
		return WorkspaceFile{}, fmt.Errorf("path is not a regular file")
	}
	if info.Size() > maxWorkspaceEditorFileBytes {
		return WorkspaceFile{}, fmt.Errorf("file is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	currentModifiedAt := formatWorkspaceModifiedAt(info.ModTime())
	if expectedModifiedAt != currentModifiedAt {
		return WorkspaceFile{}, fmt.Errorf("file changed on disk; reload it before saving")
	}
	currentData, err := os.ReadFile(resolved)
	if err != nil {
		return WorkspaceFile{}, fmt.Errorf("read file: %w", err)
	}
	if !isWorkspaceTextLike(currentData) || !utf8.Valid(currentData) {
		return WorkspaceFile{}, fmt.Errorf("file appears to be binary")
	}
	if err := os.WriteFile(resolved, []byte(content), info.Mode().Perm()); err != nil {
		return WorkspaceFile{}, fmt.Errorf("write file: %w", err)
	}
	return readWorkspaceTextFile(workspace, resolved)
}

func cleanWorkspacePathCandidate(path string) string {
	for {
		cleaned := strings.TrimSpace(path)
		cleaned = strings.Trim(cleaned, "\"'`")
		cleaned = strings.TrimRight(cleaned, ".,;!?)]}")
		if cleaned == path {
			break
		}
		path = cleaned
	}
	path = trimLineSuffix(path)
	return path
}

func trimLineSuffix(path string) string {
	for count := 0; count < 2; count++ {
		colon := strings.LastIndex(path, ":")
		if colon < 0 {
			return path
		}
		lastSeparator := strings.LastIndex(path, "/")
		if backslash := strings.LastIndex(path, "\\"); backslash > lastSeparator {
			lastSeparator = backslash
		}
		if colon <= lastSeparator || !allDigits(path[colon+1:]) {
			return path
		}
		path = path[:colon]
	}
	return path
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func workspaceRelativeCandidate(workspacePath, candidate string) (string, error) {
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if realWorkspace, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = realWorkspace
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve candidate path: %w", err)
	}
	if realCandidate, err := filepath.EvalSymlinks(candidateAbs); err == nil {
		candidateAbs = realCandidate
	}
	relative, err := filepath.Rel(workspaceAbs, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the workspace")
	}
	return relative, nil
}

func readWorkspaceTextFile(workspace Workspace, resolved string) (WorkspaceFile, error) {
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceFile{}, fmt.Errorf("file was not found")
	}
	if !info.Mode().IsRegular() {
		return WorkspaceFile{}, fmt.Errorf("path is not a regular file")
	}
	if info.Size() > maxWorkspaceEditorFileBytes {
		return WorkspaceFile{}, fmt.Errorf("file is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return WorkspaceFile{}, fmt.Errorf("read file: %w", err)
	}
	if !isWorkspaceTextLike(data) || !utf8.Valid(data) {
		return WorkspaceFile{}, fmt.Errorf("file appears to be binary")
	}
	return WorkspaceFile{
		WorkspaceID: workspace.ID,
		Path:        workspaceRelativePath(workspace.FolderPath, resolved),
		Content:     string(data),
		Bytes:       int64(len(data)),
		ModifiedAt:  formatWorkspaceModifiedAt(info.ModTime()),
	}, nil
}

func resolveWorkspaceServicePath(workspacePath, requestedPath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if realWorkspace, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = realWorkspace
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = "."
	}
	if filepath.IsAbs(requestedPath) {
		return "", fmt.Errorf("path must be relative to the workspace")
	}

	resolved := filepath.Clean(filepath.Join(workspaceAbs, requestedPath))
	relative, err := filepath.Rel(workspaceAbs, resolved)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the workspace")
	}
	if realResolved, err := filepath.EvalSymlinks(resolved); err == nil {
		relative, err := filepath.Rel(workspaceAbs, realResolved)
		if err != nil {
			return "", fmt.Errorf("resolve real path: %w", err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path escapes the workspace")
		}
		resolved = realResolved
	}
	return resolved, nil
}

func workspaceRelativePath(workspacePath, absolutePath string) string {
	relative, err := filepath.Rel(workspacePath, absolutePath)
	if err != nil {
		return filepath.ToSlash(absolutePath)
	}
	if relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}

func workspaceFileKind(info os.FileInfo) string {
	if info.IsDir() {
		return "directory"
	}
	if info.Mode().IsRegular() {
		return "file"
	}
	return "other"
}

func workspaceSearchMatches(query string, name string, relativePath string) bool {
	name = strings.ToLower(name)
	relativePath = strings.ToLower(filepath.ToSlash(relativePath))
	return strings.Contains(name, query) || strings.Contains(relativePath, query)
}

func isIgnoredWorkspaceDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".next", ".vite", "bin", "build", "coverage", "dist", "node_modules", "obj", "target":
		return true
	default:
		return false
	}
}

func isWorkspaceTextLike(data []byte) bool {
	for _, value := range data {
		if value == 0 {
			return false
		}
	}
	return true
}

func formatWorkspaceModifiedAt(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
