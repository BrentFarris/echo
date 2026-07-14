package services

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSystemServiceReadsAndSavesExternalTextFile(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	path := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(path, []byte("before\r\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	opened, err := service.ReadExternalTextFile(path)
	if err != nil {
		t.Fatalf("read external text file: %v", err)
	}
	if opened.WorkspaceID != "" || opened.Path != filepath.Clean(path) || opened.Content != "before\r\n" {
		t.Fatalf("unexpected external file: %#v", opened)
	}

	saved, err := service.SaveExternalTextFile(path, "after\r\n", opened.ModifiedAt)
	if err != nil {
		t.Fatalf("save external text file: %v", err)
	}
	if saved.Path != filepath.Clean(path) || saved.Content != "after\r\n" {
		t.Fatalf("unexpected saved external file: %#v", saved)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after\r\n" {
		t.Fatalf("expected external file to be updated, got %q", data)
	}
}

func TestSystemServiceExternalTextFileValidation(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	if _, err := service.ReadExternalTextFile("relative.txt"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected relative external path rejection, got %v", err)
	}

	binaryPath := filepath.Join(t.TempDir(), "binary.dat")
	if err := os.WriteFile(binaryPath, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadExternalTextFile(binaryPath); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary external file rejection, got %v", err)
	}

	textPath := filepath.Join(t.TempDir(), "stale.txt")
	if err := os.WriteFile(textPath, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := service.ReadExternalTextFile(textPath)
	if err != nil {
		t.Fatal(err)
	}
	nextTime := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(textPath, []byte("changed elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(textPath, nextTime, nextTime); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveExternalTextFile(textPath, "local edit", opened.ModifiedAt); err == nil ||
		!strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("expected stale external save rejection, got %v", err)
	}
}

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

func TestSystemServiceSaveWorkspaceFileFormatsGoFilesBeforeWriting(t *testing.T) {
	withWorkspaceGoImportOrganizer(t, func(_ *SystemService, _ Workspace, _ string, content string) (string, error) {
		return content, nil
	})
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	opened, err := service.ReadWorkspaceFile(workspaceID, "workspace/main.go")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	unformatted := "package main\n\nimport (\n\"fmt\"\n\"strings\"\n)\n\nfunc main(){fmt.Println(strings.TrimSpace(\" ok \"))}\n"
	expected := "package main\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc main() { fmt.Println(strings.TrimSpace(\" ok \")) }\n"
	saved, err := service.SaveWorkspaceFile(workspaceID, "workspace/main.go", unformatted, opened.ModifiedAt)
	if err != nil {
		t.Fatalf("save file: %v", err)
	}
	if saved.Content != expected || saved.Bytes != int64(len(expected)) {
		t.Fatalf("expected formatted Go content, got %#v", saved)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("expected formatted content on disk, got %q", string(data))
	}
}

func TestSystemServiceSaveWorkspaceFileDoesNotWriteGoFileWhenFormattingFails(t *testing.T) {
	withWorkspaceGoImportOrganizer(t, func(_ *SystemService, _ Workspace, _ string, content string) (string, error) {
		return content, nil
	})
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := service.ReadWorkspaceFile(workspaceID, "workspace/main.go")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	_, err = service.SaveWorkspaceFile(workspaceID, "workspace/main.go", "package main\n\nfunc main( {\n", opened.ModifiedAt)
	if err == nil || !strings.Contains(err.Error(), "gofmt failed") {
		t.Fatalf("expected gofmt error, got %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("expected failed format to leave file alone, got %q", string(data))
	}
}

func TestSystemServiceSaveWorkspaceFileOrganizesGoImportsBeforeWriting(t *testing.T) {
	withWorkspaceGoImportOrganizer(t, func(_ *SystemService, _ Workspace, _ string, content string) (string, error) {
		organized := strings.Replace(content, "package main\n\n", "package main\n\nimport \"fmt\"\n\n", 1)
		organized = strings.Replace(organized, "import (\n\t\"strings\"\n)\n\n", "", 1)
		return organized, nil
	})
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	opened, err := service.ReadWorkspaceFile(workspaceID, "workspace/main.go")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	input := "package main\n\nimport (\n\t\"strings\"\n)\n\nfunc main(){fmt.Println(\"ok\")}\n"
	saved, err := service.SaveWorkspaceFile(workspaceID, "workspace/main.go", input, opened.ModifiedAt)
	if err != nil {
		t.Fatalf("save file: %v", err)
	}
	expected := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"ok\") }\n"
	if saved.Content != expected {
		t.Fatalf("expected imports organized and formatted, got %q", saved.Content)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("expected saved file content %q, got %q", expected, string(data))
	}
}

func TestSystemServiceSaveWorkspaceFileDoesNotWriteWhenGoImportOrganizationFails(t *testing.T) {
	withWorkspaceGoImportOrganizer(t, func(_ *SystemService, _ Workspace, _ string, _ string) (string, error) {
		return "", fmt.Errorf("organizer unavailable")
	})
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "main.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := service.ReadWorkspaceFile(workspaceID, "workspace/main.go")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	_, err = service.SaveWorkspaceFile(workspaceID, "workspace/main.go", "package main\n\nfunc main() {}\n", opened.ModifiedAt)
	if err == nil || !strings.Contains(err.Error(), "organizer unavailable") {
		t.Fatalf("expected organizer error, got %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("expected failed organization to leave file alone, got %q", string(data))
	}
}

func TestSystemServiceSaveWorkspaceFileBypassesGoImportOrganizationForNonGoFiles(t *testing.T) {
	withWorkspaceGoImportOrganizer(t, func(_ *SystemService, _ Workspace, _ string, _ string) (string, error) {
		return "", fmt.Errorf("organizer should not run")
	})
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
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
	if saved.Content != "after\n" {
		t.Fatalf("expected non-Go save to bypass organizer, got %#v", saved)
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

func TestSystemServiceMoveWorkspacePathMovesFileIntoDirectory(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "src", "main.go")
	if err := os.WriteFile(source, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	moved, err := service.MoveWorkspacePath(workspaceID, "workspace/src/main.go", "workspace/docs")
	if err != nil {
		t.Fatalf("move file: %v", err)
	}
	if moved.Path != "workspace/docs/main.go" || moved.Name != "main.go" || moved.Kind != "file" {
		t.Fatalf("unexpected moved file entry: %#v", moved)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("expected source file to be moved, stat error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "main.go"))
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("expected moved file content to be preserved, got %q", string(data))
	}
}

func TestSystemServiceMoveWorkspacePathMovesFolderSubtree(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src", "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "feature", "readme.md"), []byte("feature"), 0o600); err != nil {
		t.Fatal(err)
	}

	moved, err := service.MoveWorkspacePath(workspaceID, "workspace/src/feature", "workspace/docs")
	if err != nil {
		t.Fatalf("move folder: %v", err)
	}
	if moved.Path != "workspace/docs/feature" || moved.Name != "feature" || moved.Kind != "directory" {
		t.Fatalf("unexpected moved folder entry: %#v", moved)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "feature")); !os.IsNotExist(err) {
		t.Fatalf("expected source folder to be moved, stat error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "feature", "readme.md"))
	if err != nil {
		t.Fatalf("read moved nested file: %v", err)
	}
	if string(data) != "feature" {
		t.Fatalf("expected moved nested file content to be preserved, got %q", string(data))
	}
}

func TestSystemServiceMoveWorkspacePathRejectsInvalidTargets(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "main.go"), []byte("duplicate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		sourcePath string
		targetPath string
	}{
		{name: "empty source", sourcePath: "", targetPath: "workspace/docs"},
		{name: "workspace root source", sourcePath: "workspace", targetPath: "workspace/docs"},
		{name: "missing source", sourcePath: "workspace/missing.go", targetPath: "workspace/docs"},
		{name: "target is file", sourcePath: "workspace/src/main.go", targetPath: "workspace/target.txt"},
		{name: "duplicate target", sourcePath: "workspace/src/main.go", targetPath: "workspace/docs"},
		{name: "folder into itself", sourcePath: "workspace/src", targetPath: "workspace/src/nested"},
		{name: "traversal source", sourcePath: "workspace/../outside.txt", targetPath: "workspace/docs"},
		{name: "traversal target", sourcePath: "workspace/src/main.go", targetPath: "workspace/../outside"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.MoveWorkspacePath(workspaceID, tc.sourcePath, tc.targetPath); err == nil {
				t.Fatalf("expected error moving %q to %q", tc.sourcePath, tc.targetPath)
			}
		})
	}
}

func TestSystemServiceRenameWorkspacePathRenamesFile(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	renamed, err := service.RenameWorkspacePath(workspaceID, "workspace/main.go", "app.go")
	if err != nil {
		t.Fatalf("rename file: %v", err)
	}
	if renamed.Path != "workspace/app.go" || renamed.Name != "app.go" || renamed.Kind != "file" {
		t.Fatalf("unexpected renamed file entry: %#v", renamed)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("expected source file to be renamed, stat error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "app.go"))
	if err != nil {
		t.Fatalf("read renamed file: %v", err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("expected renamed file content to be preserved, got %q", string(data))
	}
}

func TestSystemServiceRenameWorkspacePathRenamesFolderSubtree(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src", "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "feature", "readme.md"), []byte("feature"), 0o600); err != nil {
		t.Fatal(err)
	}

	renamed, err := service.RenameWorkspacePath(workspaceID, "workspace/src/feature", "renamed")
	if err != nil {
		t.Fatalf("rename folder: %v", err)
	}
	if renamed.Path != "workspace/src/renamed" || renamed.Name != "renamed" || renamed.Kind != "directory" {
		t.Fatalf("unexpected renamed folder entry: %#v", renamed)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "feature")); !os.IsNotExist(err) {
		t.Fatalf("expected source folder to be renamed, stat error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "src", "renamed", "readme.md"))
	if err != nil {
		t.Fatalf("read renamed nested file: %v", err)
	}
	if string(data) != "feature" {
		t.Fatalf("expected renamed nested file content to be preserved, got %q", string(data))
	}
}

func TestSystemServiceRenameWorkspacePathRejectsInvalidTargets(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "existing.go"), []byte("package existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		sourcePath string
		newName    string
	}{
		{name: "empty source", sourcePath: "", newName: "next.go"},
		{name: "workspace root source", sourcePath: "workspace", newName: "next"},
		{name: "missing source", sourcePath: "workspace/missing.go", newName: "next.go"},
		{name: "empty name", sourcePath: "workspace/main.go", newName: ""},
		{name: "nested name", sourcePath: "workspace/main.go", newName: "nested/next.go"},
		{name: "parent segment", sourcePath: "workspace/main.go", newName: ".."},
		{name: "duplicate target", sourcePath: "workspace/main.go", newName: "existing.go"},
		{name: "traversal source", sourcePath: "workspace/../outside.txt", newName: "next.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.RenameWorkspacePath(workspaceID, tc.sourcePath, tc.newName); err == nil {
				t.Fatalf("expected error renaming %q to %q", tc.sourcePath, tc.newName)
			}
		})
	}
}

func TestSystemServiceDeleteWorkspacePathsDeletesFilesAndFolders(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src", "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "feature", "readme.md"), []byte("feature"), 0o600); err != nil {
		t.Fatal(err)
	}

	deleted, err := service.DeleteWorkspacePaths(workspaceID, []string{
		"workspace/main.go",
		"workspace/src",
		"workspace/src/feature/readme.md",
	})
	if err != nil {
		t.Fatalf("delete paths: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected parent folder delete to cover nested child, got %#v", deleted)
	}
	if _, err := os.Stat(filepath.Join(root, "main.go")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "src")); !os.IsNotExist(err) {
		t.Fatalf("expected folder subtree to be deleted, stat error: %v", err)
	}
}

func TestSystemServiceDeleteWorkspacePathsRejectsInvalidTargets(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		paths []string
	}{
		{name: "empty selection", paths: nil},
		{name: "workspace root", paths: []string{"workspace"}},
		{name: "missing path", paths: []string{"workspace/missing.go"}},
		{name: "traversal", paths: []string{"workspace/../outside.txt"}},
		{name: "absolute path", paths: []string{outside}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.DeleteWorkspacePaths(workspaceID, tc.paths); err == nil {
				t.Fatalf("expected error deleting %#v", tc.paths)
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

func TestSystemServiceSearchWorkspaceFilesFuzzyMatchesAndRanksPaths(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src", "host"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"host_test.go":                    "package workspace\n",
		"host_render_test.go":             "package workspace\n",
		"host_entity_test.go":             "package workspace\n",
		"src/host/render_test_helpers.go": "package host\n",
		"src/host/unrelated_component.go": "package host\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.SearchWorkspaceFiles(workspaceID, "host_test", false)
	if err != nil {
		t.Fatalf("fuzzy search workspace: %v", err)
	}
	paths := entryPaths(result.Entries)
	if len(paths) != 4 {
		t.Fatalf("expected four fuzzy matches, got %v", paths)
	}
	if paths[0] != "workspace/host_test.go" {
		t.Fatalf("expected closest filename first, got %v", paths)
	}
	for _, expected := range []string{
		"workspace/host_entity_test.go",
		"workspace/host_render_test.go",
		"workspace/src/host/render_test_helpers.go",
	} {
		if !slices.Contains(paths, expected) {
			t.Fatalf("expected fuzzy result %q, got %v", expected, paths)
		}
	}
	if slices.Contains(paths, "workspace/src/host/unrelated_component.go") {
		t.Fatalf("expected out-of-order/nonmatching path to be excluded, got %v", paths)
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
	includedPaths := entryPaths(included.Entries)
	slices.Sort(includedPaths)
	if got := strings.Join(includedPaths, ","); got != "workspace/needle.txt,workspace/node_modules/pkg/needle.js" {
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

func TestSystemServiceSaveWorkspaceFileAsCreatesAndOverwritesTextFile(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)

	created, err := service.SaveWorkspaceFileAs(
		workspaceID,
		"ideas.txt",
		"first idea\n",
	)
	if err != nil {
		t.Fatalf("save new workspace file: %v", err)
	}
	if created.Path != "workspace/ideas.txt" || created.Content != "first idea\n" {
		t.Fatalf("unexpected created file: %#v", created)
	}

	overwritten, err := service.SaveWorkspaceFileAs(
		workspaceID,
		"workspace/ideas.txt",
		"second idea\n",
	)
	if err != nil {
		t.Fatalf("overwrite workspace file: %v", err)
	}
	if overwritten.Content != "second idea\n" {
		t.Fatalf("expected overwritten content, got %#v", overwritten)
	}
	data, err := os.ReadFile(filepath.Join(root, "ideas.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second idea\n" {
		t.Fatalf("unexpected saved data: %q", data)
	}
}

func TestSystemServiceSaveWorkspaceFileAsRejectsOutsideWorkspace(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	outside := filepath.Join(filepath.Dir(root), "outside-note.txt")

	if _, err := service.SaveWorkspaceFileAs(workspaceID, outside, "nope"); err == nil ||
		!strings.Contains(err.Error(), "inside a workspace folder") {
		t.Fatalf("expected outside workspace error, got %v", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("expected outside file not to be created, stat error: %v", err)
	}
}

func TestSystemServiceSearchWorkspaceTextFindsLiteralMatches(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "Hello Needle\nneedle again\n"
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{Query: "needle"})
	if err != nil {
		t.Fatalf("search workspace text: %v", err)
	}
	if result.MatchCount != 2 || result.FileCount != 1 || len(result.Files[0].Matches) != 2 {
		t.Fatalf("expected two matches in one file, got %#v", result)
	}
	first := result.Files[0].Matches[0]
	if first.Line != 1 || first.Column != 7 || first.Offset != 6 || first.MatchText != "Needle" {
		t.Fatalf("unexpected first match metadata: %#v", first)
	}

	caseSensitive, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{Query: "needle", CaseSensitive: true})
	if err != nil {
		t.Fatalf("case-sensitive search: %v", err)
	}
	if caseSensitive.MatchCount != 1 || caseSensitive.Files[0].Matches[0].Line != 2 {
		t.Fatalf("expected only lower-case match, got %#v", caseSensitive)
	}
}

func TestSystemServiceSearchWorkspaceTextRegexAndFileFilters(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	for _, dir := range []string{"src", filepath.Join("src", "generated"), "docs"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join("src", "main.go"):             "func Alpha() {}\n",
		filepath.Join("src", "generated", "gen.go"): "func AlphaGenerated() {}\n",
		filepath.Join("docs", "readme.md"):          "Alpha docs\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{
		Query:   `Alpha\w*`,
		Regex:   true,
		Include: "**/*.go",
		Exclude: "src/generated/",
	})
	if err != nil {
		t.Fatalf("search workspace text: %v", err)
	}
	if result.MatchCount != 1 || result.FileCount != 1 || result.Files[0].Path != "workspace/src/main.go" {
		t.Fatalf("expected only filtered Go source match, got %#v", result)
	}
}

func TestWorkspaceTextSearchPathFiltersSupportVSCodeGlobExpressions(t *testing.T) {
	tests := []struct {
		name         string
		expression   string
		relative     string
		rootRelative string
		fileName     string
		want         bool
	}{
		{
			name:         "brace extension group",
			expression:   "*.{ts,tsx}",
			relative:     "workspace/src/main.tsx",
			rootRelative: "src/main.tsx",
			fileName:     "main.tsx",
			want:         true,
		},
		{
			name:         "brace extension group rejects other extension",
			expression:   "*.{ts,tsx}",
			relative:     "workspace/src/main.js",
			rootRelative: "src/main.js",
			fileName:     "main.js",
			want:         false,
		},
		{
			name:         "grouped folders and character range",
			expression:   "{src,test}/**/*.[jt]s",
			relative:     "workspace/packages/app/src/lib/main.ts",
			rootRelative: "packages/app/src/lib/main.ts",
			fileName:     "main.ts",
			want:         true,
		},
		{
			name:         "implicit recursive prefix",
			expression:   "src/*.ts",
			relative:     "workspace/packages/app/src/main.ts",
			rootRelative: "packages/app/src/main.ts",
			fileName:     "main.ts",
			want:         true,
		},
		{
			name:         "dot slash anchors the expression",
			expression:   "./src/*.ts",
			relative:     "workspace/packages/app/src/main.ts",
			rootRelative: "packages/app/src/main.ts",
			fileName:     "main.ts",
			want:         false,
		},
		{
			name:         "dot slash matches from a root",
			expression:   "./src/*.ts",
			relative:     "workspace/src/main.ts",
			rootRelative: "src/main.ts",
			fileName:     "main.ts",
			want:         true,
		},
		{
			name:         "negated character range",
			expression:   "example.[!0-9]",
			relative:     "workspace/example.a",
			rootRelative: "example.a",
			fileName:     "example.a",
			want:         true,
		},
		{
			name:         "negated character range rejects digit",
			expression:   "example.[!0-9]",
			relative:     "workspace/example.4",
			rootRelative: "example.4",
			fileName:     "example.4",
			want:         false,
		},
		{
			name:         "literal brackets",
			expression:   "src/routes/post/[[]id[]]/**",
			relative:     "workspace/src/routes/post/[id]/handler.ts",
			rootRelative: "src/routes/post/[id]/handler.ts",
			fileName:     "handler.ts",
			want:         true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filters, err := compileWorkspaceTextPathFilters(test.expression)
			if err != nil {
				t.Fatalf("compile filter: %v", err)
			}
			if got := workspaceTextPathFilterMatches(filters, test.relative, test.rootRelative, test.fileName); got != test.want {
				t.Fatalf("match %q = %v, want %v", test.expression, got, test.want)
			}
		})
	}
}

func TestWorkspaceTextSearchPathFiltersKeepBraceCommasInOneExpression(t *testing.T) {
	filters, err := compileWorkspaceTextPathFilters("*.{ts, tsx}, README.md")
	if err != nil {
		t.Fatalf("compile filters: %v", err)
	}
	if len(filters) != 3 {
		t.Fatalf("expected three expanded filters, got %d", len(filters))
	}
	if _, err := compileWorkspaceTextPathFilters("*.{ts,tsx"); err == nil || !strings.Contains(err.Error(), "unmatched opening brace") {
		t.Fatalf("expected an unmatched-brace error, got %v", err)
	}
}

func TestSystemServiceSearchWorkspaceTextReportsUTF16OffsetsAcrossLines(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "unicode.txt"), []byte("😀 x\r\nneedle"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{Query: "needle"})
	if err != nil {
		t.Fatalf("search workspace text: %v", err)
	}
	if result.MatchCount != 1 {
		t.Fatalf("expected one match, got %#v", result)
	}
	match := result.Files[0].Matches[0]
	if match.Line != 2 || match.Column != 1 || match.Offset != 6 || match.EndOffset != 12 {
		t.Fatalf("unexpected UTF-16 match metadata: %#v", match)
	}
}

func TestSystemServiceSearchWorkspaceTextSkipsIgnoredFoldersByDefault(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "needle.js"), []byte("needle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "needle.txt"), []byte("needle"), 0o600); err != nil {
		t.Fatal(err)
	}

	filtered, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{Query: "needle"})
	if err != nil {
		t.Fatalf("search filtered workspace text: %v", err)
	}
	if got := workspaceTextSearchFilePaths(filtered.Files); strings.Join(got, ",") != "workspace/needle.txt" {
		t.Fatalf("expected ignored folder to be skipped, got %v", got)
	}
	included, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{Query: "needle", IncludeIgnored: true})
	if err != nil {
		t.Fatalf("search unfiltered workspace text: %v", err)
	}
	if got := strings.Join(workspaceTextSearchFilePaths(included.Files), ","); got != "workspace/needle.txt,workspace/node_modules/pkg/needle.js" {
		t.Fatalf("expected ignored folder match when included, got %v", got)
	}
}

func TestSystemServiceSearchWorkspaceTextRejectsInvalidRegex(t *testing.T) {
	service, workspaceID, _ := newWorkspaceFilesTestService(t)
	if _, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{Query: "[", Regex: true}); err == nil || !strings.Contains(err.Error(), "valid regular expression") {
		t.Fatalf("expected invalid regex error, got %v", err)
	}
}

func TestSystemServiceSearchWorkspaceTextCapsResults(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	var builder strings.Builder
	for index := 0; index < maxWorkspaceTextSearchMatches+5; index++ {
		builder.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(root, "matches.txt"), []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{Query: "needle"})
	if err != nil {
		t.Fatalf("search workspace text: %v", err)
	}
	if result.MatchCount != maxWorkspaceTextSearchMatches {
		t.Fatalf("expected capped matches, got %d", result.MatchCount)
	}
	if !result.Truncated {
		t.Fatal("expected truncated result")
	}
}

func TestWorkspaceTextSearchStartingNewRunCancelsPrevious(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	first, firstID := service.startWorkspaceTextSearch("workspace")
	second, secondID := service.startWorkspaceTextSearch("workspace")
	if firstID == secondID {
		t.Fatal("expected distinct search run IDs")
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("expected the superseded search to be canceled")
	}
	select {
	case <-second.Done():
		t.Fatal("expected the current search to remain active")
	default:
	}
	service.finishWorkspaceTextSearch("workspace", firstID)
	select {
	case <-second.Done():
		t.Fatal("expected finishing an old search not to cancel the current search")
	default:
	}
	service.finishWorkspaceTextSearch("workspace", secondID)
	select {
	case <-second.Done():
	default:
		t.Fatal("expected the finished search to be canceled")
	}
}

func TestSystemServiceSearchWorkspaceTextStreamsMatches(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "match.txt"), []byte("needle"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := SubscribeEvents(service, 16)
	defer unsubscribe()

	result, err := service.SearchWorkspaceText(workspaceID, WorkspaceTextSearchRequest{SearchID: "search-1", Query: "needle"})
	if err != nil {
		t.Fatalf("search workspace text: %v", err)
	}
	if result.MatchCount != 1 {
		t.Fatalf("expected one final match, got %#v", result)
	}

	seenStarted := false
	seenMatches := false
	for {
		select {
		case runtimeEvent := <-events:
			if runtimeEvent.Name != WorkspaceTextSearchRuntimeEventName {
				continue
			}
			event, ok := runtimeEvent.Data.(WorkspaceTextSearchEvent)
			if !ok || event.SearchID != "search-1" || event.WorkspaceID != workspaceID {
				t.Fatalf("unexpected streamed search event: %#v", runtimeEvent.Data)
			}
			switch event.Type {
			case "started":
				seenStarted = true
			case "matches":
				seenMatches = len(event.Files) == 1 && event.MatchCount == 1
			case "complete":
				if !seenStarted || !seenMatches || event.Result == nil || event.Result.MatchCount != 1 {
					t.Fatalf("incomplete streamed event sequence: started=%v matches=%v complete=%#v", seenStarted, seenMatches, event)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for streamed search events")
		}
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

func withWorkspaceGoImportOrganizer(t *testing.T, organize func(*SystemService, Workspace, string, string) (string, error)) {
	t.Helper()
	previous := organizeWorkspaceGoImportsBeforeSave
	organizeWorkspaceGoImportsBeforeSave = organize
	t.Cleanup(func() {
		organizeWorkspaceGoImportsBeforeSave = previous
	})
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

func workspaceTextSearchFilePaths(files []WorkspaceTextSearchFileResult) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// Minimal valid PNG (1x1 red pixel) - 67 bytes
var smallPNGData = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
	0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk length + type
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // width=1, height=1
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, // bit depth, color type, CRC start
	0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, // IDAT chunk length + type
	0x54, 0x78, 0x9C, 0x62, 0x60, 0x18, 0x00, 0x1F, // compressed data
	0x38, 0x81, 0xFC, 0xFF, 0xFF, 0x00, 0x00, 0x00, // more compressed data
	0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, // IEND chunk
	0x82, // CRC
}

func TestSystemServiceReadWorkspaceMediaFileReturnsDataURLOrImage(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "icon.png"), smallPNGData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/icon.png")
	if err != nil {
		t.Fatalf("read media file: %v", err)
	}
	if result.WorkspaceID != workspaceID {
		t.Fatalf("expected workspace ID %q, got %q", workspaceID, result.WorkspaceID)
	}
	if result.Path != "workspace/icon.png" {
		t.Fatalf("expected path workspace/icon.png, got %q", result.Path)
	}
	if result.MimeType != "image/png" {
		t.Fatalf("expected mime type image/png, got %q", result.MimeType)
	}
	if !strings.HasPrefix(result.DataURL, "data:image/png;base64,") {
		t.Fatalf("expected data URL prefix, got %q", result.DataURL)
	}
	if result.Bytes != int64(len(smallPNGData)) {
		t.Fatalf("expected bytes %d, got %d", len(smallPNGData), result.Bytes)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsJPEG(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	if err := os.WriteFile(filepath.Join(root, "photo.jpg"), jpegData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/photo.jpg")
	if err != nil {
		t.Fatalf("read media file: %v", err)
	}
	if result.MimeType != "image/jpeg" {
		t.Fatalf("expected mime type image/jpeg, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsGIF(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	gifData := []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61}
	if err := os.WriteFile(filepath.Join(root, "anim.gif"), gifData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/anim.gif")
	if err != nil {
		t.Fatalf("read media file: %v", err)
	}
	if result.MimeType != "image/gif" {
		t.Fatalf("expected mime type image/gif, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioMP3(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	mp3Data := []byte{0x49, 0x44, 0x33, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if err := os.WriteFile(filepath.Join(root, "track.mp3"), mp3Data, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/track.mp3")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/mpeg" {
		t.Fatalf("expected mime type audio/mpeg, got %q", result.MimeType)
	}
	if !strings.HasPrefix(result.DataURL, "data:audio/mpeg;base64,") {
		t.Fatalf("expected data URL prefix, got %q", result.DataURL)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioWAV(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	wavHeader := append([]byte("RIFF"), []byte{0, 0, 0, 0}...)
	wavData := append(wavHeader, []byte("WAVE")...)
	if err := os.WriteFile(filepath.Join(root, "sample.wav"), wavData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/sample.wav")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/wav" {
		t.Fatalf("expected mime type audio/wav, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioOGG(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	oggData := []byte{0x4F, 0x67, 0x67, 0x53, 0x00, 0x02, 0x00, 0x00}
	if err := os.WriteFile(filepath.Join(root, "music.ogg"), oggData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/music.ogg")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/ogg" {
		t.Fatalf("expected mime type audio/ogg, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioFLAC(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	flacData := []byte{0x66, 0x4C, 0x61, 0x43, 0x00, 0x00, 0x00, 0x22}
	if err := os.WriteFile(filepath.Join(root, "lossless.flac"), flacData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/lossless.flac")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/flac" {
		t.Fatalf("expected mime type audio/flac, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioAAC(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	aacData := []byte{0xFF, 0xF1, 0x50, 0x80}
	if err := os.WriteFile(filepath.Join(root, "audio.aac"), aacData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/audio.aac")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/aac" {
		t.Fatalf("expected mime type audio/aac, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioM4A(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	m4aData := append([]byte("    "), []byte("ftypM4A ")...)
	if err := os.WriteFile(filepath.Join(root, "song.m4a"), m4aData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/song.m4a")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/mp4" {
		t.Fatalf("expected mime type audio/mp4, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioOpus(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	opusData := []byte("OpusHead")
	if err := os.WriteFile(filepath.Join(root, "voice.opus"), opusData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/voice.opus")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/opus" {
		t.Fatalf("expected mime type audio/opus, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileDetectsAudioByExtensionFallback(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	// Unknown magic bytes but .wma extension should resolve via extension map
	wmaData := []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}
	if err := os.WriteFile(filepath.Join(root, "track.wma"), wmaData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/track.wma")
	if err != nil {
		t.Fatalf("read audio file: %v", err)
	}
	if result.MimeType != "audio/x-ms-wma" {
		t.Fatalf("expected mime type audio/x-ms-wma, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileExistingImageStillWorks(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "icon.png"), smallPNGData, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/icon.png")
	if err != nil {
		t.Fatalf("read media file: %v", err)
	}
	if result.MimeType != "image/png" {
		t.Fatalf("expected mime type image/png, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileExistingVideoStillWorks(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	mp4Data := append([]byte("    "), []byte("ftypmp42")...)
	if err := os.WriteFile(filepath.Join(root, "video.mp4"), mp4Data, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/video.mp4")
	if err != nil {
		t.Fatalf("read media file: %v", err)
	}
	if result.MimeType != "video/mp4" {
		t.Fatalf("expected mime type video/mp4, got %q", result.MimeType)
	}
}

func TestSystemServiceReadWorkspaceMediaFileRejectsNonMediaFile(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/main.go")
	if err == nil || !strings.Contains(err.Error(), "not a supported media type") {
		t.Fatalf("expected non-media error, got %v", err)
	}
}

func TestSystemServiceReadWorkspaceMediaFileRejectsLargeFile(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	large := strings.Repeat("\x89PNG", maxWorkspaceMediaFileBytes/4+1)
	if err := os.WriteFile(filepath.Join(root, "huge.png"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/huge.png")
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("expected large file error, got %v", err)
	}
}

func TestSystemServiceReadWorkspaceMediaFileRejectsMissingFile(t *testing.T) {
	service, workspaceID, _ := newWorkspaceFilesTestService(t)
	_, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/missing.png")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestSystemServiceReadWorkspaceMediaFileRejectsDirectory(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := service.ReadWorkspaceMediaFile(workspaceID, "workspace/subdir")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected not a regular file error, got %v", err)
	}
}

func TestDetectMediaTypeFallsBackToExtension(t *testing.T) {
	mime := detectMediaType([]byte{0x01, 0x02, 0x03}, "image.unknown.png")
	if mime != "image/png" {
		t.Fatalf("expected image/png from extension fallback, got %q", mime)
	}

	mime = detectMediaType([]byte{0x01, 0x02, 0x03}, "main.go")
	if mime != "" {
		t.Fatalf("expected empty MIME for .go, got %q", mime)
	}
}

func TestDetectMagicByteMIME(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"png", []byte{0x89, 0x50, 0x4E, 0x47}, "image/png"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF}, "image/jpeg"},
		{"gif", []byte{'G', 'I', 'F', '8'}, "image/gif"},
		{"webp", append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, append([]byte("WEBP"), 0, 0)...)...), "image/webp"},
		{"mp4", append([]byte("    "), []byte("ftypmp42")...), "video/mp4"},
		{"m4a", append([]byte("    "), []byte("ftypM4A ")...), "audio/mp4"},
		{"m4b", append([]byte("    "), []byte("ftypM4B ")...), "audio/mp4"},
		{"webm", []byte{0x1A, 0x45, 0xDF, 0xA3}, "video/webm"},
		{"mp3_id3", []byte{0x49, 0x44, 0x33, 0x04, 0x00, 0x00}, "audio/mpeg"},
		{"mp3_sync", []byte{0xFF, 0xFB, 0x90, 0x00}, "audio/mpeg"},
		{"wav", append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("WAVE")...)...), "audio/wav"},
		{"ogg", []byte{0x4F, 0x67, 0x67, 0x53, 0x00, 0x02}, "audio/ogg"},
		{"flac", []byte{0x66, 0x4C, 0x61, 0x43, 0x00, 0x00, 0x00, 0x22}, "audio/flac"},
		{"aac", []byte{0xFF, 0xF1, 0x50, 0x80}, "audio/aac"},
		{"opus", []byte("OpusHead"), "audio/opus"},
		{"unknown", []byte{0x01, 0x02, 0x03}, ""},
		{"short", []byte{0x89}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectMagicByteMIME(tt.data)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
