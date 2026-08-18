package workspaces

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateValidatesNameAndPaths(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	extra := filepath.Join(dir, "extra")
	for _, p := range []string{main, extra} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	m := NewManager(filepath.Join(dir, "echo.json"))

	// Missing name.
	if _, err := m.Create(CreateRequest{MainPath: main}); err == nil {
		t.Fatal("expected error for missing name")
	}
	// Missing main path.
	if _, err := m.Create(CreateRequest{Name: "ws"}); err == nil {
		t.Fatal("expected error for missing main path")
	}
	// Nonexistent folder.
	if _, err := m.Create(CreateRequest{Name: "ws", MainPath: filepath.Join(dir, "nope")}); err == nil {
		t.Fatal("expected error for nonexistent main path")
	}
	// A file (not a dir) as a folder.
	filePath := filepath.Join(dir, "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := m.Create(CreateRequest{Name: "ws", MainPath: main, Folders: []string{filePath}}); err == nil {
		t.Fatal("expected error for non-folder path")
	}
}

func TestCreateWritesEchoLayoutAndRegisters(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	extra := filepath.Join(dir, "extra")
	for _, p := range []string{main, extra} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	m := NewManager(filepath.Join(dir, "echo.json"))
	ws, err := m.Create(CreateRequest{
		Name:     "My Workspace",
		MainPath: main,
		Folders:  []string{extra, main, ""}, // main is deduped, empty dropped
		Icon:     &Icon{Data: []byte("fake-png"), Ext: "png"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ws.ID == "" {
		t.Fatal("expected an id")
	}
	if ws.IconExt != "png" {
		t.Fatalf("expected iconExt png, got %q", ws.IconExt)
	}
	// Folders: main first, then extra (deduped main and empty dropped).
	if len(ws.Folders) != 2 || ws.Folders[0] != main || ws.Folders[1] != extra {
		t.Fatalf("unexpected folders: %v", ws.Folders)
	}

	echoDir := filepath.Join(main, EchoDirName)
	if fi, err := os.Stat(echoDir); err != nil || !fi.IsDir() {
		t.Fatalf("expected .echo dir: %v", err)
	}
	// workspace.json exists and contains the folders list.
	wfData, err := os.ReadFile(filepath.Join(echoDir, "workspace.json"))
	if err != nil {
		t.Fatalf("read workspace.json: %v", err)
	}
	if !strings.Contains(string(wfData), `"folders"`) || !strings.Contains(string(wfData), "extra") {
		t.Fatalf("workspace.json missing folders: %s", wfData)
	}
	// Icon copied.
	iconData, err := os.ReadFile(filepath.Join(echoDir, "icon.png"))
	if err != nil {
		t.Fatalf("read icon: %v", err)
	}
	if string(iconData) != "fake-png" {
		t.Fatalf("unexpected icon contents: %q", iconData)
	}

	// Registered in the shared app data file.
	list, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "My Workspace" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list[0].MainPath != main {
		t.Fatalf("unexpected main path: %q", list[0].MainPath)
	}
}

func TestCreateRejectsDuplicateName(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m := NewManager(filepath.Join(dir, "echo.json"))
	if _, err := m.Create(CreateRequest{Name: "Dup", MainPath: main}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Case-insensitive duplicate.
	if _, err := m.Create(CreateRequest{Name: "dup", MainPath: main}); err == nil {
		t.Fatal("expected duplicate name error")
	}
}

func TestIconPathAutoDetectsExtension(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m := NewManager(filepath.Join(dir, "echo.json"))
	ws, err := m.Create(CreateRequest{Name: "ws", MainPath: main, Icon: &Icon{Data: []byte("x"), Ext: "jpeg"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path, err := m.IconPath(ws.ID)
	if err != nil {
		t.Fatalf("icon path: %v", err)
	}
	want := filepath.Join(main, EchoDirName, "icon.jpeg")
	if path != want {
		t.Fatalf("expected %q, got %q", want, path)
	}
}

func TestIconPathReturnsEmptyWhenNoIcon(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	m := NewManager(filepath.Join(dir, "echo.json"))
	ws, err := m.Create(CreateRequest{Name: "ws", MainPath: main})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path, err := m.IconPath(ws.ID)
	if err != nil {
		t.Fatalf("icon path: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty icon path, got %q", path)
	}
}

func TestActiveWorkspaceLifecycle(t *testing.T) {
	dir := t.TempDir()
	mainA := filepath.Join(dir, "a")
	mainB := filepath.Join(dir, "b")
	for _, p := range []string{mainA, mainB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	m := NewManager(filepath.Join(dir, "echo.json"))

	// No active workspace initially.
	if _, ok, err := m.Active(); err != nil || ok {
		t.Fatalf("expected no active workspace, ok=%v err=%v", ok, err)
	}

	wsA, err := m.Create(CreateRequest{Name: "A", MainPath: mainA})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	wsB, err := m.Create(CreateRequest{Name: "B", MainPath: mainB})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	// Set A active and read it back.
	if err := m.SetActive(wsA.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}
	active, ok, err := m.Active()
	if err != nil || !ok {
		t.Fatalf("expected active workspace, ok=%v err=%v", ok, err)
	}
	if active.ID != wsA.ID || active.Name != "A" {
		t.Fatalf("unexpected active: %+v", active)
	}

	// Switch to B.
	if err := m.SetActive(wsB.ID); err != nil {
		t.Fatalf("set active B: %v", err)
	}
	active, _, _ = m.Active()
	if active.ID != wsB.ID {
		t.Fatalf("expected B active, got %+v", active)
	}

	// Setting an unknown id fails.
	if err := m.SetActive("ws-nope"); err == nil {
		t.Fatal("expected error for unknown active id")
	}
}

func TestActiveWorkspacePersistsAcrossStoreInstances(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "echo.json")
	m1 := NewManager(path)
	ws, err := m1.Create(CreateRequest{Name: "P", MainPath: main})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := m1.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	// A fresh manager reading the same file sees the persisted active id.
	m2 := NewManager(path)
	active, ok, err := m2.Active()
	if err != nil || !ok {
		t.Fatalf("expected active from fresh store, ok=%v err=%v", ok, err)
	}
	if active.ID != ws.ID {
		t.Fatalf("unexpected active: %+v", active)
	}
}

func TestCreateDeduplicatesCanonicalFolderPaths(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(filepath.Join(directory, "echo.json"))
	workspace, err := manager.Create(CreateRequest{
		Name: "Canonical", MainPath: root,
		Folders: []string{filepath.Join(root, "."), root + string(filepath.Separator)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Folders) != 1 {
		t.Fatalf("expected one canonical folder, got %#v", workspace.Folders)
	}
}
