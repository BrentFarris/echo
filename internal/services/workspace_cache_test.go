package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSystemServiceEnsureWorkspaceCacheFoldersCreatesCacheLayout(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)

	caches, err := service.ensureWorkspaceCacheFolders(workspaceID)
	if err != nil {
		t.Fatalf("ensure workspace cache folders: %v", err)
	}
	if len(caches) != 1 {
		t.Fatalf("expected one cache folder, got %#v", caches)
	}
	cache := caches[0]
	if cache.WorkspaceID != workspaceID || cache.FolderLabel != "workspace" || cache.WorkspaceRootPath != root {
		t.Fatalf("unexpected cache metadata: %#v", cache)
	}
	for _, path := range []string{
		filepath.Join(root, ".echo"),
		filepath.Join(root, ".echo", "skills"),
		filepath.Join(root, ".echo", "file-database"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("expected cache directory %s: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("expected %s to be a directory", path)
		}
	}
	if cache.Path != filepath.Join(root, ".echo") ||
		cache.SkillsPath != filepath.Join(root, ".echo", "skills") ||
		cache.FileDatabasePath != filepath.Join(root, ".echo", "file-database") {
		t.Fatalf("unexpected cache paths: %#v", cache)
	}
}

func TestWorkspaceCacheFilePathCreatesNestedParents(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	folder := workspace.Folders[0]

	skillPath, err := workspaceSkillCachePath(folder, "openai/chat/completions.json")
	if err != nil {
		t.Fatalf("resolve skill cache path: %v", err)
	}
	expectedSkillPath := filepath.Join(root, ".echo", "skills", "openai", "chat", "completions.json")
	if skillPath != expectedSkillPath {
		t.Fatalf("expected skill cache path %q, got %q", expectedSkillPath, skillPath)
	}
	if info, err := os.Stat(filepath.Dir(skillPath)); err != nil || !info.IsDir() {
		t.Fatalf("expected nested skill cache parent, info=%#v err=%v", info, err)
	}

	searchPath, err := workspaceFileDatabaseCachePath(folder, "index/main.db")
	if err != nil {
		t.Fatalf("resolve file database cache path: %v", err)
	}
	expectedSearchPath := filepath.Join(root, ".echo", "file-database", "index", "main.db")
	if searchPath != expectedSearchPath {
		t.Fatalf("expected file database cache path %q, got %q", expectedSearchPath, searchPath)
	}
}

func TestWorkspaceCacheFilePathRejectsUnsafeRelativePaths(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	folder := workspace.Folders[0]

	cases := []string{
		"",
		"../outside.db",
		"nested/../outside.db",
		"nested//outside.db",
		filepath.Join(root, "outside.db"),
	}
	for _, candidate := range cases {
		t.Run(candidate, func(t *testing.T) {
			if _, err := workspaceFileDatabaseCachePath(folder, candidate); err == nil {
				t.Fatalf("expected unsafe cache path %q to be rejected", candidate)
			}
		})
	}
}

func TestWorkspaceCacheRejectsSymlinkedCacheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation can require elevated privileges on Windows")
	}
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".echo")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := service.ensureWorkspaceCacheFolders(workspaceID)
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink cache directory rejection, got %v", err)
	}
}

func TestSystemServiceSearchWorkspaceFilesSkipsEchoCacheByDefault(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if _, err := service.ensureWorkspaceCacheFolders(workspaceID); err != nil {
		t.Fatalf("ensure cache folders: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".echo", "file-database", "needle.db"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "needle.txt"), []byte("workspace"), 0o600); err != nil {
		t.Fatal(err)
	}

	filtered, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search filtered workspace: %v", err)
	}
	if got := strings.Join(entryPaths(filtered.Entries), ","); got != "workspace/needle.txt" {
		t.Fatalf("expected .echo cache to be skipped by default, got %v", got)
	}

	included, err := service.SearchWorkspaceFiles(workspaceID, "needle", true)
	if err != nil {
		t.Fatalf("search unfiltered workspace: %v", err)
	}
	if got := strings.Join(entryPaths(included.Entries), ","); got != "workspace/needle.txt" {
		t.Fatalf("expected .echo cache to stay hidden when ignored files are included, got %v", got)
	}
}
