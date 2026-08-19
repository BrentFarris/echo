package workspacefs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitForSearch(t *testing.T, service *Service, workspaceID, query string, includeDirectories bool) SearchResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		result := service.SearchEntries(workspaceID, query, 50, includeDirectories)
		if !result.Indexing {
			return result
		}
		if time.Now().After(deadline) {
			t.Fatal("workspace search index did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSearchCanIncludeWorkspaceFoldersWithoutChangingFileOnlySearch(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	t.Cleanup(service.Close)
	if err := os.MkdirAll(filepath.Join(rootPath, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "src", "nested", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	withDirectories := waitForSearch(t, service, workspaceID, "", true)
	found := map[string]string{}
	for _, item := range withDirectories.Items {
		found[item.Ref.Path] = item.Kind
	}
	for path, kind := range map[string]string{"": "directory", "src": "directory", "src/nested": "directory", "src/nested/main.go": "file"} {
		if found[path] != kind {
			t.Fatalf("search result %q = %q, want %q; results: %#v", path, found[path], kind, withDirectories.Items)
		}
	}
	if withDirectories.Items[0].Ref.RootID != root.ID {
		t.Fatalf("search returned a result for the wrong root: %#v", withDirectories.Items[0])
	}

	filesOnly := service.Search(workspaceID, "", 50)
	if len(filesOnly.Items) != 1 || filesOnly.Items[0].Kind != "file" || filesOnly.Items[0].Ref.Path != "src/nested/main.go" {
		t.Fatalf("file-only search included directories or lost the file: %#v", filesOnly.Items)
	}
	labeled := service.SearchEntries(workspaceID, root.ReferenceLabel+"/src/nested", 10, true)
	if len(labeled.Items) == 0 || labeled.Items[0].ReferencePath != root.ReferenceLabel+"/src/nested" {
		t.Fatalf("labeled reference path did not rank the directory first: %#v", labeled.Items)
	}
}

func TestSearchDirectoryIndexHonorsIgnoredAndPrivateFolders(t *testing.T) {
	service, workspaceID, rootPath, _ := newTestService(t)
	t.Cleanup(service.Close)
	for _, directory := range []string{"visible", "ignored", ".git", ".echo"} {
		if err := os.MkdirAll(filepath.Join(rootPath, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootPath, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := waitForSearch(t, service, workspaceID, "", true)
	for _, item := range result.Items {
		if item.Ref.Path == "ignored" || item.Ref.Path == ".git" || item.Ref.Path == ".echo" {
			t.Fatalf("search exposed an ignored/private directory: %#v", item)
		}
	}
}
