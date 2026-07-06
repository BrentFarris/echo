package services

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pathsEqualFold compares two filesystem paths after normalizing to absolute,
// cleaned forms and doing a case-insensitive comparison. This handles Windows
// short/long path differences (e.g. JOHN~1.JAC vs john.jackson).
func pathsEqualFold(a, b string) bool {
	aAbs := filepath.Clean(a)
	bAbs := filepath.Clean(b)

	if abs, err := filepath.Abs(aAbs); err == nil {
		aAbs = abs
	}
	if abs, err := filepath.Abs(bAbs); err == nil {
		bAbs = abs
	}

	// Resolve via EvalSymlinks to normalize short 8.3 names to long names on Windows.
	// For non-existent files, resolve the parent directory instead.
	if real, err := filepath.EvalSymlinks(aAbs); err == nil {
		aAbs = real
	} else if dirReal, err := filepath.EvalSymlinks(filepath.Dir(aAbs)); err == nil {
		aAbs = filepath.Join(dirReal, filepath.Base(aAbs))
	}
	if real, err := filepath.EvalSymlinks(bAbs); err == nil {
		bAbs = real
	} else if dirReal, err := filepath.EvalSymlinks(filepath.Dir(bAbs)); err == nil {
		bAbs = filepath.Join(dirReal, filepath.Base(bAbs))
	}

	return strings.EqualFold(aAbs, bAbs)
}

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
	if cache.WorkspaceID != workspaceID || cache.FolderLabel != "workspace" {
		t.Fatalf("unexpected cache metadata: %#v", cache)
	}
	// WorkspaceRootPath may differ from root due to EvalSymlinks or short/long path resolution on Windows.
	if !pathsEqualFold(cache.WorkspaceRootPath, root) {
		t.Fatalf("expected workspace root %q, got %q", root, cache.WorkspaceRootPath)
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
	expectedCache := filepath.Join(root, ".echo")
	if !pathsEqualFold(cache.Path, expectedCache) {
		t.Fatalf("expected cache path %q, got %q", expectedCache, cache.Path)
	}
	expectedSkills := filepath.Join(root, ".echo", "skills")
	if !pathsEqualFold(cache.SkillsPath, expectedSkills) {
		t.Fatalf("expected skills path %q, got %q", expectedSkills, cache.SkillsPath)
	}
	expectedDB := filepath.Join(root, ".echo", "file-database")
	if !pathsEqualFold(cache.FileDatabasePath, expectedDB) {
		t.Fatalf("expected file database path %q, got %q", expectedDB, cache.FileDatabasePath)
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
	if !pathsEqualFold(skillPath, expectedSkillPath) {
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
	if !pathsEqualFold(searchPath, expectedSearchPath) {
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

func TestMigrateLegacyModesToWorkspaceScopedMovesDirectory(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)

	// Create the legacy .echo/modes/ directory with a mode.
	legacyPath := filepath.Join(root, ".echo", "modes")
	modeDir := filepath.Join(legacyPath, "a1b2c3d4-e5f6-a7b8-c9d0-e1f2a3b4c5d6")
	if err := os.MkdirAll(modeDir, 0o755); err != nil {
		t.Fatalf("create legacy modes dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modeDir, "mode.json"), []byte(`{"id":"a1b2c3d4-e5f6-a7b8-c9d0-e1f2a3b4c5d6","name":"Legacy Mode"}`), 0o600); err != nil {
		t.Fatalf("write legacy mode: %v", err)
	}

	// Ensure cache folders; migration should run.
	caches, err := service.ensureWorkspaceCacheFolders(workspaceID)
	if err != nil {
		t.Fatalf("ensure workspace cache folders: %v", err)
	}
	if len(caches) != 1 {
		t.Fatalf("expected one cache folder, got %d", len(caches))
	}

	// Verify legacy directory was moved to scoped path.
	scopedPath := filepath.Join(root, ".echo", "modes-"+workspaceID)
	children, err := os.ReadDir(scopedPath)
	if err != nil {
		t.Fatalf("read scoped modes directory: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 mode in scoped dir, got %d", len(children))
	}
	if children[0].Name() != "a1b2c3d4-e5f6-a7b8-c9d0-e1f2a3b4c5d6" {
		t.Fatalf("expected mode dir name, got %q", children[0].Name())
	}

	// Verify legacy directory no longer exists.
	if _, err := os.Lstat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy modes directory should be gone after migration")
	}

	// Verify the mode file content is intact.
	modeJSON, err := os.ReadFile(filepath.Join(scopedPath, "a1b2c3d4-e5f6-a7b8-c9d0-e1f2a3b4c5d6", "mode.json"))
	if err != nil {
		t.Fatalf("read migrated mode file: %v", err)
	}
	if !strings.Contains(string(modeJSON), "Legacy Mode") {
		t.Fatalf("migrated mode content lost: %s", modeJSON)
	}
}

func TestMigrateLegacyModesSkipsWhenScopedExistsWithContent(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)

	// Create both legacy and scoped directories.
	legacyPath := filepath.Join(root, ".echo", "modes")
	legacyModeDir := filepath.Join(legacyPath, "11111111-1111-1111-1111-111111111111")
	if err := os.MkdirAll(legacyModeDir, 0o755); err != nil {
		t.Fatalf("create legacy modes dir: %v", err)
	}

	scopedPath := filepath.Join(root, ".echo", "modes-"+workspaceID)
	scopedModeDir := filepath.Join(scopedPath, "22222222-2222-2222-2222-222222222222")
	if err := os.MkdirAll(scopedModeDir, 0o755); err != nil {
		t.Fatalf("create scoped modes dir: %v", err)
	}

	// Ensure cache folders; migration should skip because scoped has content.
	if _, err := service.ensureWorkspaceCacheFolders(workspaceID); err != nil {
		t.Fatalf("ensure workspace cache folders: %v", err)
	}

	// Legacy directory should still exist (not moved).
	children, err := os.ReadDir(legacyPath)
	if err != nil {
		t.Fatalf("read legacy modes directory: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected legacy dir preserved with 1 mode, got %d", len(children))
	}

	// Scoped directory should still have only its original content.
	scopedChildren, err := os.ReadDir(scopedPath)
	if err != nil {
		t.Fatalf("read scoped modes directory: %v", err)
	}
	if len(scopedChildren) != 1 || scopedChildren[0].Name() != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("scoped dir should be unchanged, got %d children", len(scopedChildren))
	}
}

func TestMigrateLegacyModesNoOpWhenNoLegacyDirectory(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)

	// No legacy directory exists; migration should be a no-op.
	if _, err := service.ensureWorkspaceCacheFolders(workspaceID); err != nil {
		t.Fatalf("ensure workspace cache folders: %v", err)
	}

	// Scoped directory should exist (created by ensureWorkspaceFolderCache).
	scopedPath := filepath.Join(root, ".echo", "modes-"+workspaceID)
	info, err := os.Lstat(scopedPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("scoped modes dir should exist after cache init: %v", err)
	}

	// Legacy directory should not exist.
	legacyPath := filepath.Join(root, ".echo", "modes")
	if _, err := os.Lstat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy dir should not exist")
	}
}
