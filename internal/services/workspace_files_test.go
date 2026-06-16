package services

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSystemServiceListWorkspaceDirectorySortsDirectoriesFirst(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.Mkdir(filepath.Join(root, "z-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	rootDirectory, err := service.ListWorkspaceDirectory(workspaceID, ".")
	if err != nil {
		t.Fatalf("list workspace root: %v", err)
	}
	if strings.Join(entryNames(rootDirectory.Entries), ",") != "workspace" {
		t.Fatalf("expected virtual root entry, got %#v", rootDirectory.Entries)
	}

	directory, err := service.ListWorkspaceDirectory(workspaceID, "workspace")
	if err != nil {
		t.Fatalf("list directory: %v", err)
	}
	if directory.WorkspaceID != workspaceID || directory.Path != "workspace" {
		t.Fatalf("unexpected directory metadata: %#v", directory)
	}
	names := entryNames(directory.Entries)
	expected := []string{"a-dir", "z-dir", "a.txt", "b.txt"}
	if strings.Join(names, ",") != strings.Join(expected, ",") {
		t.Fatalf("expected sorted entries %v, got %v", expected, names)
	}
	if directory.Entries[0].Kind != "directory" || directory.Entries[2].Kind != "file" {
		t.Fatalf("expected directories before files, got %#v", directory.Entries)
	}
}

func TestSystemServiceReadWorkspaceFileReturnsTextFile(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := service.ReadWorkspaceFile(workspaceID, "workspace/src/main.go")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if file.WorkspaceID != workspaceID || file.Path != "workspace/src/main.go" || file.Content != "package main\n" {
		t.Fatalf("unexpected file: %#v", file)
	}
	if file.Bytes != int64(len("package main\n")) || file.ModifiedAt == "" {
		t.Fatalf("expected bytes and modified timestamp, got %#v", file)
	}
}

func TestSystemServiceSaveWorkspaceFileWritesWhenUnchanged(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	opened, err := service.ReadWorkspaceFile(workspaceID, "workspace/README.md")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	saved, err := service.SaveWorkspaceFile(workspaceID, "workspace/README.md", "after\n", opened.ModifiedAt)
	if err != nil {
		t.Fatalf("save file: %v", err)
	}
	if saved.Content != "after\n" || saved.Bytes != int64(len("after\n")) {
		t.Fatalf("unexpected saved file: %#v", saved)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after\n" {
		t.Fatalf("expected file content to be saved, got %q", data)
	}
}

func TestSystemServiceSaveWorkspaceFileRejectsStaleModifiedAt(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := service.ReadWorkspaceFile(workspaceID, "workspace/README.md")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	_, err = service.SaveWorkspaceFile(workspaceID, "workspace/README.md", "after\n", opened.ModifiedAt)
	if err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("expected conflict error, got %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "external\n" {
		t.Fatalf("expected stale save to leave file alone, got %q", data)
	}
}

func TestSystemServiceCreateWorkspaceFileCreatesEmptyTextFile(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	components := filepath.Join(root, "src", "components")
	if err := os.MkdirAll(components, 0o755); err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateWorkspaceFile(workspaceID, "workspace/src", "components/Button.ts")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if created.Path != "workspace/src/components/Button.ts" || created.Content != "" || created.Bytes != 0 {
		t.Fatalf("unexpected created file: %#v", created)
	}
	if data, err := os.ReadFile(filepath.Join(components, "Button.ts")); err != nil {
		t.Fatalf("read created file: %v", err)
	} else if string(data) != "" {
		t.Fatalf("expected empty file, got %q", string(data))
	}
}

func TestSystemServiceCreateWorkspaceFolderCreatesDirectory(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	created, err := service.CreateWorkspaceFolder(workspaceID, "workspace/src", "components")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if created.Path != "workspace/src/components" || created.Name != "components" || created.Kind != "directory" {
		t.Fatalf("unexpected created folder entry: %#v", created)
	}
	directory, err := service.ListWorkspaceDirectory(workspaceID, "workspace/src")
	if err != nil {
		t.Fatalf("list directory: %v", err)
	}
	if strings.Join(entryNames(directory.Entries), ",") != "components" {
		t.Fatalf("expected created folder in directory listing, got %#v", directory.Entries)
	}
}

func TestSystemServiceCreateWorkspaceFileRejectsInvalidTargets(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("readme"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "existing.txt"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		parentPath string
		fileName   string
	}{
		{name: "empty name", parentPath: "workspace/src", fileName: ""},
		{name: "absolute name", parentPath: "workspace/src", fileName: filepath.Join(root, "outside.txt")},
		{name: "parent traversal", parentPath: "workspace/src", fileName: "../outside.txt"},
		{name: "nested traversal", parentPath: "workspace/src", fileName: "components/../Button.ts"},
		{name: "existing target", parentPath: "workspace/src", fileName: "existing.txt"},
		{name: "missing parent", parentPath: "workspace/missing", fileName: "new.txt"},
		{name: "missing nested parent", parentPath: "workspace/src", fileName: "missing/new.txt"},
		{name: "file parent", parentPath: "workspace/README.md", fileName: "child.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.CreateWorkspaceFile(workspaceID, tc.parentPath, tc.fileName); err == nil {
				t.Fatalf("expected error creating %q under %q", tc.fileName, tc.parentPath)
			}
		})
	}
}

func TestSystemServiceCreateWorkspaceFolderRejectsInvalidTargets(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "src", "existing"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		parentPath string
		fileName   string
	}{
		{name: "empty name", parentPath: "workspace/src", fileName: ""},
		{name: "absolute name", parentPath: "workspace/src", fileName: filepath.Join(root, "outside")},
		{name: "traversal", parentPath: "workspace/src", fileName: "../outside"},
		{name: "existing target", parentPath: "workspace/src", fileName: "existing"},
		{name: "missing nested parent", parentPath: "workspace/src", fileName: "missing/new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.CreateWorkspaceFolder(workspaceID, tc.parentPath, tc.fileName); err == nil {
				t.Fatalf("expected error creating folder %q under %q", tc.fileName, tc.parentPath)
			}
		})
	}
}

func TestSystemServiceWorkspaceFilesRejectBinaryAndLargeFiles(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "image.bin"), []byte{0x01, 0x00, 0x02}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadWorkspaceFile(workspaceID, "workspace/image.bin"); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary error, got %v", err)
	}

	large := strings.Repeat("x", maxWorkspaceEditorFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadWorkspaceFile(workspaceID, "workspace/large.txt"); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("expected large file error, got %v", err)
	}
}

func TestSystemServiceWorkspaceFilesRejectTraversalAndAbsolutePaths(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := service.ReadWorkspaceFile(workspaceID, "workspace/../outside.txt"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal error, got %v", err)
	}
	absolute := outside
	if runtime.GOOS != "windows" {
		absolute = filepath.ToSlash(absolute)
	}
	if _, err := service.ReadWorkspaceFile(workspaceID, absolute); err == nil || !strings.Contains(err.Error(), "relative") {
		t.Fatalf("expected absolute path error, got %v", err)
	}
}

func TestSystemServiceResolveWorkspaceTextFilePath(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "src", "main.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	relative, err := service.ResolveWorkspaceTextFilePath(workspaceID, "workspace/src/main.go:12")
	if err != nil {
		t.Fatalf("resolve relative path: %v", err)
	}
	if relative != "workspace/src/main.go" {
		t.Fatalf("expected workspace/src/main.go, got %q", relative)
	}

	quoted, err := service.ResolveWorkspaceTextFilePath(workspaceID, `"workspace/src/main.go:12:4",`)
	if err != nil {
		t.Fatalf("resolve quoted path: %v", err)
	}
	if quoted != "workspace/src/main.go" {
		t.Fatalf("expected workspace/src/main.go, got %q", quoted)
	}

	absolute, err := service.ResolveWorkspaceTextFilePath(workspaceID, filePath)
	if err != nil {
		t.Fatalf("resolve absolute path: %v", err)
	}
	if absolute != "workspace/src/main.go" {
		t.Fatalf("expected workspace/src/main.go, got %q", absolute)
	}
}

func TestSystemServiceResolveWorkspaceTextFilePathRejectsInvalidTargets(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.bin"), []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := service.ResolveWorkspaceTextFilePath(workspaceID, "workspace/../outside.txt"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal error, got %v", err)
	}
	if _, err := service.ResolveWorkspaceTextFilePath(workspaceID, outside); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected absolute outside error, got %v", err)
	}
	if _, err := service.ResolveWorkspaceTextFilePath(workspaceID, "workspace/image.bin"); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary error, got %v", err)
	}
}

func TestSystemServiceSearchWorkspaceFilesFindsNestedMatches(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src", "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "feature", "search_handler.go"), []byte("package feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "feature", "other.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.SearchWorkspaceFiles(workspaceID, "search", false)
	if err != nil {
		t.Fatalf("search workspace: %v", err)
	}
	if result.WorkspaceID != workspaceID || result.Query != "search" {
		t.Fatalf("unexpected result metadata: %#v", result)
	}
	paths := entryPaths(result.Entries)
	if strings.Join(paths, ",") != "workspace/src/feature/search_handler.go" {
		t.Fatalf("expected nested search match, got %v", paths)
	}

	backslashResult, err := service.SearchWorkspaceFiles(workspaceID, `workspace\src\feature\search`, false)
	if err != nil {
		t.Fatalf("search workspace with backslashes: %v", err)
	}
	if got := strings.Join(entryPaths(backslashResult.Entries), ","); got != "workspace/src/feature/search_handler.go" {
		t.Fatalf("expected backslash query to match nested path, got %v", got)
	}
}

func TestSystemServiceSearchWorkspaceFilesSkipsIgnoredFoldersByDefault(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "needle.js"), []byte("module"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "needle.txt"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}

	filtered, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search filtered workspace: %v", err)
	}
	if got := strings.Join(entryPaths(filtered.Entries), ","); got != "workspace/needle.txt" {
		t.Fatalf("expected ignored folder to be skipped, got %v", got)
	}
	included, err := service.SearchWorkspaceFiles(workspaceID, "needle", true)
	if err != nil {
		t.Fatalf("search unfiltered workspace: %v", err)
	}
	if got := strings.Join(entryPaths(included.Entries), ","); got != "workspace/needle.txt,workspace/node_modules/pkg/needle.js" {
		t.Fatalf("expected ignored folder match when included, got %v", got)
	}
}

func TestSystemServiceSearchWorkspaceFilesEmptyQueryListsWorkspaceEntries(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("readme"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "ignored.js"), []byte("module"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.SearchWorkspaceFiles(workspaceID, "", false)
	if err != nil {
		t.Fatalf("search workspace: %v", err)
	}
	paths := strings.Join(entryPaths(result.Entries), ",")
	for _, expected := range []string{"workspace/README.md", "workspace/src", "workspace/src/main.go"} {
		if !strings.Contains(paths, expected) {
			t.Fatalf("expected empty query results to include %q, got %v", expected, paths)
		}
	}
	if strings.Contains(paths, "node_modules") {
		t.Fatalf("expected ignored folders to be skipped, got %v", paths)
	}
}

func TestSystemServiceSearchWorkspaceFilesRejectsInvalidWorkspace(t *testing.T) {
	service, _, _ := newWorkspaceFilesTestService(t)
	if _, err := service.SearchWorkspaceFiles("missing", "anything", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing workspace error, got %v", err)
	}
}

func TestSystemServiceSearchWorkspaceFilesCapsResults(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	for index := 0; index < maxWorkspaceFileSearchResults+5; index++ {
		name := filepath.Join(root, fmt.Sprintf("match-%03d.txt", index))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.SearchWorkspaceFiles(workspaceID, "match", false)
	if err != nil {
		t.Fatalf("search workspace: %v", err)
	}
	if len(result.Entries) != maxWorkspaceFileSearchResults {
		t.Fatalf("expected capped results, got %d", len(result.Entries))
	}
	if !result.Truncated {
		t.Fatal("expected truncated result")
	}
}

func newWorkspaceFilesTestService(t *testing.T) (*SystemService, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return service, state.ActiveWorkspaceID, root
}

func entryNames(entries []WorkspaceFileEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func entryPaths(entries []WorkspaceFileEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	return paths
}
