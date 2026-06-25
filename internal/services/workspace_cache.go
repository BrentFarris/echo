package services

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	workspaceCacheDirName        = ".echo"
	workspaceSkillCacheDirName   = "skills"
	workspaceSearchCacheDirName  = "file-search"
	workspaceCacheDirectoryPerm  = 0o755
	workspaceCacheFilePermission = 0o600
)

type WorkspaceCacheFolder struct {
	WorkspaceID       string `json:"workspaceId"`
	FolderID          string `json:"folderId"`
	FolderLabel       string `json:"folderLabel"`
	Path              string `json:"path"`
	SkillsPath        string `json:"skillsPath"`
	FileSearchPath    string `json:"fileSearchPath"`
	WorkspaceRootPath string `json:"workspaceRootPath"`
}

func (s *SystemService) ensureWorkspaceCacheFolders(workspaceID string) ([]WorkspaceCacheFolder, error) {
	workspace, err := s.workspaceByID(workspaceID)
	if err != nil {
		return nil, err
	}
	return ensureWorkspaceCacheFolders(workspace)
}

func ensureWorkspaceCacheFolders(workspace Workspace) ([]WorkspaceCacheFolder, error) {
	caches := make([]WorkspaceCacheFolder, 0, len(workspace.Folders))
	for _, folder := range workspace.Folders {
		if folder.Missing {
			continue
		}
		cache, err := ensureWorkspaceFolderCache(workspace.ID, folder)
		if err != nil {
			return nil, err
		}
		caches = append(caches, cache)
	}
	return caches, nil
}

func ensureWorkspaceFolderCache(workspaceID string, folder WorkspaceFolder) (WorkspaceCacheFolder, error) {
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return WorkspaceCacheFolder{}, err
	}
	cachePath := filepath.Join(root, workspaceCacheDirName)
	if err := ensureWorkspaceCacheDirectory(cachePath, root); err != nil {
		return WorkspaceCacheFolder{}, err
	}
	skillsPath := filepath.Join(cachePath, workspaceSkillCacheDirName)
	if err := ensureWorkspaceCacheDirectory(skillsPath, cachePath); err != nil {
		return WorkspaceCacheFolder{}, err
	}
	fileSearchPath := filepath.Join(cachePath, workspaceSearchCacheDirName)
	if err := ensureWorkspaceCacheDirectory(fileSearchPath, cachePath); err != nil {
		return WorkspaceCacheFolder{}, err
	}
	return WorkspaceCacheFolder{
		WorkspaceID:       workspaceID,
		FolderID:          folder.ID,
		FolderLabel:       folder.Label,
		Path:              cachePath,
		SkillsPath:        skillsPath,
		FileSearchPath:    fileSearchPath,
		WorkspaceRootPath: root,
	}, nil
}

func workspaceSkillCachePath(folder WorkspaceFolder, relativePath string) (string, error) {
	return workspaceCacheFilePath(folder, workspaceSkillCacheDirName, relativePath)
}

func workspaceFileSearchCachePath(folder WorkspaceFolder, relativePath string) (string, error) {
	return workspaceCacheFilePath(folder, workspaceSearchCacheDirName, relativePath)
}

func workspaceCacheFilePath(folder WorkspaceFolder, cacheName string, relativePath string) (string, error) {
	cacheName, err := cleanWorkspaceCacheName(cacheName)
	if err != nil {
		return "", err
	}
	relative, err := cleanWorkspaceCacheRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	root, err := workspaceFolderAbsolutePath(folder)
	if err != nil {
		return "", err
	}
	cacheRoot := filepath.Join(root, workspaceCacheDirName)
	if err := ensureWorkspaceCacheDirectory(cacheRoot, root); err != nil {
		return "", err
	}
	cacheDir := filepath.Join(cacheRoot, cacheName)
	if err := ensureWorkspaceCacheDirectory(cacheDir, cacheRoot); err != nil {
		return "", err
	}
	target := filepath.Join(cacheDir, relative)
	if err := ensureWorkspaceCachePathInside(cacheDir, target); err != nil {
		return "", err
	}
	parent := filepath.Dir(target)
	if err := ensureWorkspaceCacheParentDirectory(cacheDir, parent); err != nil {
		return "", err
	}
	return target, nil
}

func ensureWorkspaceCacheDirectory(dir string, boundary string) error {
	dir = filepath.Clean(dir)
	boundary = filepath.Clean(boundary)
	if err := ensureWorkspaceCachePathInside(boundary, dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace cache directory %s must not be a symlink", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace cache path %s is not a directory", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat workspace cache directory: %w", err)
	}
	if err := os.MkdirAll(dir, workspaceCacheDirectoryPerm); err != nil {
		return fmt.Errorf("create workspace cache directory: %w", err)
	}
	info, err = os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat workspace cache directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("workspace cache path %s is not a directory", dir)
	}
	return nil
}

func ensureWorkspaceCacheParentDirectory(boundary string, parent string) error {
	boundary = filepath.Clean(boundary)
	parent = filepath.Clean(parent)
	if err := ensureWorkspaceCachePathInside(boundary, parent); err != nil {
		return err
	}
	relative, err := filepath.Rel(boundary, parent)
	if err != nil {
		return fmt.Errorf("resolve workspace cache parent path: %w", err)
	}
	if relative == "." {
		return nil
	}
	current := boundary
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		if err := ensureWorkspaceCacheDirectory(current, boundary); err != nil {
			return err
		}
	}
	return nil
}

func cleanWorkspaceCacheName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("workspace cache name is required")
	}
	if filepath.IsAbs(name) || path.IsAbs(name) || filepath.VolumeName(name) != "" || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("workspace cache name must be a relative path segment")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("workspace cache name must not be current or parent directory")
	}
	return name, nil
}

func ensureWorkspaceCachePathInside(boundary string, target string) error {
	boundaryAbs, err := filepath.Abs(boundary)
	if err != nil {
		return fmt.Errorf("resolve workspace cache boundary: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve workspace cache path: %w", err)
	}
	relative, err := filepath.Rel(boundaryAbs, targetAbs)
	if err != nil {
		return fmt.Errorf("resolve workspace cache relative path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("workspace cache path escapes the cache directory")
	}
	return nil
}

func cleanWorkspaceCacheRelativePath(relativePath string) (string, error) {
	candidate := strings.TrimSpace(strings.ReplaceAll(relativePath, "\\", "/"))
	if candidate == "" {
		return "", fmt.Errorf("workspace cache path is required")
	}
	if filepath.IsAbs(candidate) || path.IsAbs(candidate) || filepath.VolumeName(candidate) != "" {
		return "", fmt.Errorf("workspace cache path must be relative")
	}
	segments := strings.Split(candidate, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("workspace cache path must not contain empty, current, or parent directory segments")
		}
	}
	return filepath.FromSlash(path.Clean(strings.Join(segments, "/"))), nil
}
