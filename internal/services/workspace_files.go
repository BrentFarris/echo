package services

import (
	"fmt"
	"go/format"
	"os"
	"path"
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
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) == "." {
		return listWorkspaceVirtualRoot(workspace), nil
	}
	resolved, err := resolveWorkspaceServicePath(workspace, path)
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
		Path:        workspaceRelativePath(workspace, resolved),
		Entries:     make([]WorkspaceFileEntry, 0, len(entries)),
	}
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			return WorkspaceDirectory{}, fmt.Errorf("read directory entry: %w", err)
		}
		output.Entries = append(output.Entries, WorkspaceFileEntry{
			Name:       entry.Name(),
			Path:       workspaceRelativePath(workspace, filepath.Join(resolved, entry.Name())),
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

func listWorkspaceVirtualRoot(workspace Workspace) WorkspaceDirectory {
	output := WorkspaceDirectory{
		WorkspaceID: workspace.ID,
		Path:        ".",
		Entries:     make([]WorkspaceFileEntry, 0, len(workspace.Folders)),
	}
	for _, folder := range workspace.Folders {
		entry := WorkspaceFileEntry{
			Name: folder.Label,
			Path: folder.Label,
			Kind: "directory",
		}
		if info, err := os.Stat(folder.Path); err == nil {
			entry.Bytes = info.Size()
			entry.ModifiedAt = formatWorkspaceModifiedAt(info.ModTime())
		}
		output.Entries = append(output.Entries, entry)
	}
	sort.Slice(output.Entries, func(i, j int) bool {
		return strings.ToLower(output.Entries[i].Name) < strings.ToLower(output.Entries[j].Name)
	})
	return output
}

func (s *SystemService) SearchWorkspaceFiles(workspaceID string, query string, includeIgnored bool) (WorkspaceFileSearchResult, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFileSearchResult{}, err
	}
	query = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(query)), "\\", "/")
	output := WorkspaceFileSearchResult{
		WorkspaceID: workspace.ID,
		Query:       query,
		Entries:     []WorkspaceFileEntry{},
	}
	entries, truncated, err := searchWorkspaceFilesWithDatabase(workspace, query, includeIgnored, maxWorkspaceFileSearchResults)
	if err == nil {
		output.Entries = entries
		output.Truncated = truncated
		return output, nil
	}
	return searchWorkspaceFilesByWalking(workspace, query, includeIgnored, output)
}

func searchWorkspaceFilesByWalking(workspace Workspace, query string, includeIgnored bool, output WorkspaceFileSearchResult) (WorkspaceFileSearchResult, error) {
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		root, err := resolveWorkspaceServicePath(workspace, folder.Label)
		if err != nil {
			continue
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
			relative := workspaceRelativePath(workspace, path)
			if entry.IsDir() && !includeIgnored && isIgnoredWorkspaceDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			if !workspaceSearchMatches(query, entry.Name(), relative) {
				return nil
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
	}
	sortWorkspaceFileEntries(output.Entries, query)
	if len(output.Entries) > maxWorkspaceFileSearchResults {
		output.Entries = output.Entries[:maxWorkspaceFileSearchResults]
		output.Truncated = true
	}
	return output, nil
}

func (s *SystemService) CreateWorkspaceFile(workspaceID string, parentPath string, name string) (WorkspaceFile, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFile{}, err
	}
	resolved, err := resolveWorkspaceCreateTarget(workspace, parentPath, name)
	if err != nil {
		return WorkspaceFile{}, err
	}
	file, err := os.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return WorkspaceFile{}, fmt.Errorf("path already exists")
		}
		return WorkspaceFile{}, fmt.Errorf("create file: %w", err)
	}
	if err := file.Close(); err != nil {
		return WorkspaceFile{}, fmt.Errorf("close file: %w", err)
	}
	s.removeWorkspaceFileDatabases(workspaceID)
	return readWorkspaceTextFile(workspace, resolved)
}

func (s *SystemService) CreateWorkspaceFolder(workspaceID string, parentPath string, name string) (WorkspaceFileEntry, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}
	resolved, err := resolveWorkspaceCreateTarget(workspace, parentPath, name)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}
	if err := os.Mkdir(resolved, 0o755); err != nil {
		if os.IsExist(err) {
			return WorkspaceFileEntry{}, fmt.Errorf("path already exists")
		}
		return WorkspaceFileEntry{}, fmt.Errorf("create folder: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return WorkspaceFileEntry{}, fmt.Errorf("stat folder: %w", err)
	}
	s.removeWorkspaceFileDatabases(workspaceID)
	return workspaceFileEntry(workspace, resolved, info), nil
}

func (s *SystemService) MoveWorkspacePath(workspaceID string, sourcePath string, targetParentPath string) (WorkspaceFileEntry, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}
	source, target, err := resolveWorkspaceMoveTarget(workspace, sourcePath, targetParentPath)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}
	if err := os.Rename(source, target); err != nil {
		return WorkspaceFileEntry{}, fmt.Errorf("move path: %w", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return WorkspaceFileEntry{}, fmt.Errorf("stat moved path: %w", err)
	}
	s.removeWorkspaceFileDatabases(workspaceID)
	return workspaceFileEntry(workspace, target, info), nil
}

func (s *SystemService) RenameWorkspacePath(workspaceID string, sourcePath string, name string) (WorkspaceFileEntry, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}
	source, target, err := resolveWorkspaceRenameTarget(workspace, sourcePath, name)
	if err != nil {
		return WorkspaceFileEntry{}, err
	}
	if source != target {
		if err := os.Rename(source, target); err != nil {
			return WorkspaceFileEntry{}, fmt.Errorf("rename path: %w", err)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return WorkspaceFileEntry{}, fmt.Errorf("stat renamed path: %w", err)
	}
	s.removeWorkspaceFileDatabases(workspaceID)
	return workspaceFileEntry(workspace, target, info), nil
}

func (s *SystemService) ReadWorkspaceFile(workspaceID string, path string) (WorkspaceFile, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return WorkspaceFile{}, err
	}
	if strings.TrimSpace(path) == "" {
		return WorkspaceFile{}, fmt.Errorf("path is required")
	}
	resolved, err := resolveWorkspaceServicePath(workspace, path)
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
		relative, err := workspaceRelativeCandidate(workspace, path)
		if err != nil {
			return "", err
		}
		path = relative
	}
	resolved, err := resolveWorkspaceServicePath(workspace, path)
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

	resolved, err := resolveWorkspaceServicePath(workspace, path)
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
	content, err = formatWorkspaceFileContentBeforeSave(resolved, content)
	if err != nil {
		return WorkspaceFile{}, err
	}
	if len([]byte(content)) > maxWorkspaceEditorFileBytes {
		return WorkspaceFile{}, fmt.Errorf("formatted content is larger than the %d byte editor limit", maxWorkspaceEditorFileBytes)
	}
	if err := os.WriteFile(resolved, []byte(content), info.Mode().Perm()); err != nil {
		return WorkspaceFile{}, fmt.Errorf("write file: %w", err)
	}
	s.removeWorkspaceFileDatabases(workspaceID)
	return readWorkspaceTextFile(workspace, resolved)
}

func formatWorkspaceFileContentBeforeSave(resolvedPath string, content string) (string, error) {
	if !strings.EqualFold(filepath.Ext(resolvedPath), ".go") {
		return content, nil
	}
	formatted, err := format.Source([]byte(content))
	if err != nil {
		return "", fmt.Errorf("gofmt failed: %w", err)
	}
	return string(formatted), nil
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

func workspaceRelativeCandidate(workspace Workspace, candidate string) (string, error) {
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve candidate path: %w", err)
	}
	if realCandidate, err := filepath.EvalSymlinks(candidateAbs); err == nil {
		candidateAbs = realCandidate
	}
	for _, folder := range workspaceFoldersByPathDepth(workspace) {
		if folder.Missing {
			continue
		}
		workspaceAbs, err := workspaceFolderAbsolutePath(folder)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(workspaceAbs, candidateAbs)
		if err != nil {
			continue
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if relative == "." {
			return folder.Label, nil
		}
		return folder.Label + "/" + filepath.ToSlash(relative), nil
	}
	return "", fmt.Errorf("path escapes the workspace")
}

func resolveWorkspaceMoveTarget(workspace Workspace, sourcePath string, targetParentPath string) (string, string, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return "", "", fmt.Errorf("source path is required")
	}
	if strings.TrimSpace(targetParentPath) == "" || strings.TrimSpace(targetParentPath) == "." {
		return "", "", fmt.Errorf("target directory must start with a workspace folder label")
	}
	label, sourceRelative := splitWorkspaceLabeledPath(sourcePath)
	if label == "" || sourceRelative == "." {
		return "", "", fmt.Errorf("workspace folder roots cannot be moved")
	}
	source, err := resolveWorkspaceServicePath(workspace, sourcePath)
	if err != nil {
		return "", "", err
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return "", "", fmt.Errorf("source path was not found")
	}
	if !sourceInfo.IsDir() && !sourceInfo.Mode().IsRegular() {
		return "", "", fmt.Errorf("source path is not a movable file or folder")
	}
	targetParent, err := resolveWorkspaceServicePath(workspace, targetParentPath)
	if err != nil {
		return "", "", err
	}
	targetParentInfo, err := os.Stat(targetParent)
	if err != nil {
		return "", "", fmt.Errorf("target directory was not found")
	}
	if !targetParentInfo.IsDir() {
		return "", "", fmt.Errorf("target path is not a directory")
	}
	targetParent, err = filepath.EvalSymlinks(targetParent)
	if err != nil {
		return "", "", fmt.Errorf("resolve target directory: %w", err)
	}
	if _, err := workspaceRelativeCandidate(workspace, targetParent); err != nil {
		return "", "", err
	}
	if sourceInfo.IsDir() {
		sourceReal, err := filepath.EvalSymlinks(source)
		if err != nil {
			return "", "", fmt.Errorf("resolve source directory: %w", err)
		}
		relative, err := filepath.Rel(sourceReal, targetParent)
		if err != nil {
			return "", "", fmt.Errorf("resolve target directory: %w", err)
		}
		if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return "", "", fmt.Errorf("folder cannot be moved into itself")
		}
	}
	target := filepath.Join(targetParent, filepath.Base(source))
	if _, err := os.Lstat(target); err == nil {
		return "", "", fmt.Errorf("path already exists")
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("check target path: %w", err)
	}
	if _, err := workspaceRelativeCandidate(workspace, target); err != nil {
		return "", "", err
	}
	return source, target, nil
}

func resolveWorkspaceRenameTarget(workspace Workspace, sourcePath string, name string) (string, string, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return "", "", fmt.Errorf("source path is required")
	}
	label, sourceRelative := splitWorkspaceLabeledPath(sourcePath)
	if label == "" || sourceRelative == "." {
		return "", "", fmt.Errorf("workspace folder roots cannot be renamed")
	}
	source, err := resolveWorkspaceServicePath(workspace, sourcePath)
	if err != nil {
		return "", "", err
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return "", "", fmt.Errorf("source path was not found")
	}
	if !sourceInfo.IsDir() && !sourceInfo.Mode().IsRegular() {
		return "", "", fmt.Errorf("source path is not a renamable file or folder")
	}
	cleanName, err := cleanWorkspaceRenameName(name)
	if err != nil {
		return "", "", err
	}
	if cleanName == filepath.Base(source) {
		return source, source, nil
	}
	parent := filepath.Dir(source)
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", "", fmt.Errorf("resolve parent directory: %w", err)
	}
	if _, err := workspaceRelativeCandidate(workspace, realParent); err != nil {
		return "", "", err
	}
	target := filepath.Join(realParent, cleanName)
	if targetInfo, err := os.Lstat(target); err == nil {
		if !os.SameFile(sourceInfo, targetInfo) {
			return "", "", fmt.Errorf("path already exists")
		}
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("check target path: %w", err)
	}
	if _, err := workspaceRelativeCandidate(workspace, target); err != nil {
		return "", "", err
	}
	return source, target, nil
}

func resolveWorkspaceCreateTarget(workspace Workspace, parentPath string, name string) (string, error) {
	parent, err := resolveWorkspaceServicePath(workspace, parentPath)
	if err != nil {
		return "", err
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("parent directory was not found")
	}
	if !parentInfo.IsDir() {
		return "", fmt.Errorf("parent path is not a directory")
	}
	cleanName, err := cleanWorkspaceCreateName(name)
	if err != nil {
		return "", err
	}
	target := filepath.Clean(filepath.Join(parent, filepath.FromSlash(cleanName)))
	relative, err := filepath.Rel(parent, target)
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the parent directory")
	}
	targetParent := filepath.Dir(target)
	targetParentInfo, err := os.Stat(targetParent)
	if err != nil {
		return "", fmt.Errorf("parent directory was not found")
	}
	if !targetParentInfo.IsDir() {
		return "", fmt.Errorf("parent path is not a directory")
	}
	realTargetParent, err := filepath.EvalSymlinks(targetParent)
	if err != nil {
		return "", fmt.Errorf("resolve parent directory: %w", err)
	}
	if _, err := workspaceRelativeCandidate(workspace, realTargetParent); err != nil {
		return "", err
	}
	target = filepath.Join(realTargetParent, filepath.Base(target))
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("path already exists")
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("check target path: %w", err)
	}
	return target, nil
}

func cleanWorkspaceCreateName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if filepath.IsAbs(name) || path.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("name must be relative")
	}
	segments := strings.Split(name, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("name must not contain empty, current, or parent directory segments")
		}
	}
	return path.Clean(strings.Join(segments, "/")), nil
}

func cleanWorkspaceRenameName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if filepath.IsAbs(name) || path.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("name must be relative")
	}
	if strings.Contains(name, "/") {
		return "", fmt.Errorf("name must not contain path separators")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("name must not be current or parent directory")
	}
	return name, nil
}

func workspaceFileEntry(workspace Workspace, absolutePath string, info os.FileInfo) WorkspaceFileEntry {
	return WorkspaceFileEntry{
		Name:       filepath.Base(absolutePath),
		Path:       workspaceRelativePath(workspace, absolutePath),
		Kind:       workspaceFileKind(info),
		Bytes:      info.Size(),
		ModifiedAt: formatWorkspaceModifiedAt(info.ModTime()),
	}
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
		Path:        workspaceRelativePath(workspace, resolved),
		Content:     string(data),
		Bytes:       int64(len(data)),
		ModifiedAt:  formatWorkspaceModifiedAt(info.ModTime()),
	}, nil
}

func resolveWorkspaceServicePath(workspace Workspace, requestedPath string) (string, error) {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = "."
	}
	if filepath.IsAbs(requestedPath) {
		return "", fmt.Errorf("path must be relative to the workspace")
	}
	if requestedPath == "." {
		return "", fmt.Errorf("path must start with a workspace folder label")
	}
	label, relativePath := splitWorkspaceLabeledPath(requestedPath)
	if label == "" {
		return "", fmt.Errorf("path must start with a workspace folder label")
	}
	folder, ok := workspaceFolderByLabel(workspace, label)
	if !ok {
		return "", fmt.Errorf("workspace folder %q was not found", label)
	}
	if folder.Missing {
		return "", fmt.Errorf("workspace folder %q is unavailable", folder.Label)
	}
	workspaceAbs, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return "", err
	}
	resolved := filepath.Clean(filepath.Join(workspaceAbs, relativePath))
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

func workspaceRelativePath(workspace Workspace, absolutePath string) string {
	absPath := strings.TrimSpace(absolutePath)
	if absPath == "" {
		return filepath.ToSlash(absolutePath)
	}
	absAbs, err := filepath.Abs(absPath)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	if realAbs, err := filepath.EvalSymlinks(absAbs); err == nil {
		absAbs = realAbs
	}
	for _, folder := range workspaceFoldersByPathDepth(workspace) {
		workspaceAbs, err := workspaceFolderAbsolutePath(folder)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(workspaceAbs, absAbs)
		if err != nil {
			continue
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if relative == "." {
			return folder.Label
		}
		return folder.Label + "/" + filepath.ToSlash(relative)
	}
	return filepath.ToSlash(absPath)
}

func splitWorkspaceLabeledPath(requestedPath string) (string, string) {
	path := strings.TrimSpace(strings.ReplaceAll(requestedPath, "\\", "/"))
	path = strings.TrimPrefix(path, "./")
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return "", "."
	}
	parts := strings.SplitN(path, "/", 2)
	label := strings.TrimSpace(parts[0])
	relativePath := "."
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		relativePath = filepath.FromSlash(parts[1])
	}
	return label, relativePath
}

func workspaceFolderByLabel(workspace Workspace, label string) (WorkspaceFolder, bool) {
	label = strings.TrimSpace(label)
	for _, folder := range workspace.Folders {
		if strings.EqualFold(folder.Label, label) {
			return folder, true
		}
	}
	return WorkspaceFolder{}, false
}

func workspaceFolderForAbsolutePath(workspace Workspace, absolutePath string) (WorkspaceFolder, error) {
	relative, err := workspaceRelativeCandidate(workspace, absolutePath)
	if err != nil {
		return WorkspaceFolder{}, err
	}
	label, _ := splitWorkspaceLabeledPath(relative)
	folder, ok := workspaceFolderByLabel(workspace, label)
	if !ok {
		return WorkspaceFolder{}, fmt.Errorf("workspace folder %q was not found", label)
	}
	return folder, nil
}

func workspaceFolderAbsolutePath(folder WorkspaceFolder) (string, error) {
	workspaceAbs, err := filepath.Abs(folder.Path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace folder path: %w", err)
	}
	if realWorkspace, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = realWorkspace
	}
	return workspaceAbs, nil
}

func workspaceFoldersByPathDepth(workspace Workspace) []WorkspaceFolder {
	folders := append([]WorkspaceFolder{}, workspace.Folders...)
	sort.SliceStable(folders, func(i, j int) bool {
		return len(filepath.Clean(folders[i].Path)) > len(filepath.Clean(folders[j].Path))
	})
	return folders
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
	_, matched := workspaceSearchScore(query, name, relativePath)
	return matched
}

func isIgnoredWorkspaceDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".echo", ".git", ".next", ".vite", "bin", "build", "coverage", "dist", "node_modules", "obj", "target":
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
