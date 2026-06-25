package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/tools"
)

func TestSearchWorkspaceFilesCreatesFileDatabase(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "needle.go"), []byte("package src\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search workspace files: %v", err)
	}
	if got := strings.Join(entryPaths(result.Entries), ","); got != "workspace/src/needle.go" {
		t.Fatalf("expected database-backed search result, got %v", got)
	}

	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	databasePath, err := workspaceFileDatabasePath(workspace.Folders[0])
	if err != nil {
		t.Fatalf("resolve database path: %v", err)
	}
	if info, err := os.Stat(databasePath); err != nil || info.IsDir() {
		t.Fatalf("expected file database at %s, info=%#v err=%v", databasePath, info, err)
	}
}

func TestSearchWorkspaceFilesUsesFreshFileDatabase(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "needle-one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SearchWorkspaceFiles(workspaceID, "needle", false); err != nil {
		t.Fatalf("build file database: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "needle-two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search workspace files: %v", err)
	}
	if got := strings.Join(entryPaths(result.Entries), ","); got != "workspace/needle-one.txt" {
		t.Fatalf("expected fresh database to be reused before expiry, got %v", got)
	}
}

func TestSearchWorkspaceFilesRebuildsStaleFileDatabase(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "needle-one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SearchWorkspaceFiles(workspaceID, "needle", false); err != nil {
		t.Fatalf("build file database: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "needle-two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeWorkspaceFileDatabaseStale(t, service, workspaceID)

	result, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search workspace files: %v", err)
	}
	if got := strings.Join(entryPaths(result.Entries), ","); got != "workspace/needle-one.txt,workspace/needle-two.txt" {
		t.Fatalf("expected stale database rebuild to include new file, got %v", got)
	}
}

func TestSearchWorkspaceFilesSkipsDeletedFreshDatabaseEntries(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "needle.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SearchWorkspaceFiles(workspaceID, "needle", false); err != nil {
		t.Fatalf("build file database: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	result, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search workspace files: %v", err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("expected deleted database entry to be skipped, got %#v", result.Entries)
	}
}

func TestWorkspaceFileDatabaseInvalidatesAfterWorkspaceFileCreate(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "needle-one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SearchWorkspaceFiles(workspaceID, "needle", false); err != nil {
		t.Fatalf("build file database: %v", err)
	}

	if _, err := service.CreateWorkspaceFile(workspaceID, "workspace", "needle-two.txt"); err != nil {
		t.Fatalf("create workspace file: %v", err)
	}
	result, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search workspace files: %v", err)
	}
	if got := strings.Join(entryPaths(result.Entries), ","); got != "workspace/needle-one.txt,workspace/needle-two.txt" {
		t.Fatalf("expected create to invalidate file database, got %v", got)
	}
}

func TestWorkspaceFileDatabaseInvalidatesAfterToolFileChange(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "needle-one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SearchWorkspaceFiles(workspaceID, "needle", false); err != nil {
		t.Fatalf("build file database: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "needle-two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}

	service.recordToolFileChanges(workspaceID, WorkspaceChangeSource{Type: "chat", ToolName: "filesystem_create_text"}, []tools.FileChange{{
		Path:      "workspace/needle-two.txt",
		Operation: tools.FileChangeCreated,
	}})
	result, err := service.SearchWorkspaceFiles(workspaceID, "needle", false)
	if err != nil {
		t.Fatalf("search workspace files: %v", err)
	}
	if got := strings.Join(entryPaths(result.Entries), ","); got != "workspace/needle-one.txt,workspace/needle-two.txt" {
		t.Fatalf("expected tool change to invalidate file database, got %v", got)
	}
}

func makeWorkspaceFileDatabaseStale(t *testing.T, service *SystemService, workspaceID string) {
	t.Helper()
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	path, err := workspaceFileDatabasePath(workspace.Folders[0])
	if err != nil {
		t.Fatalf("resolve file database path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file database: %v", err)
	}
	var database workspaceFileDatabase
	if err := json.Unmarshal(data, &database); err != nil {
		t.Fatalf("decode file database: %v", err)
	}
	database.GeneratedAt = formatWorkspaceModifiedAt(time.Now().Add(-(workspaceFileDatabaseMaxAge + time.Second)))
	if err := writeWorkspaceFileDatabase(workspace.Folders[0], database); err != nil {
		t.Fatalf("write stale file database: %v", err)
	}
}
