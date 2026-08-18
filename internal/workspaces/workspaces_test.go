package workspaces

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/brent/echo/internal/appdata"
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
	// workspace.json stores portable paths relative to its .echo directory.
	wfData, err := os.ReadFile(filepath.Join(echoDir, "workspace.json"))
	if err != nil {
		t.Fatalf("read workspace.json: %v", err)
	}
	var wf workspaceFile
	if err := json.Unmarshal(wfData, &wf); err != nil {
		t.Fatalf("parse workspace.json: %v", err)
	}
	if wf.MainPath != "../" || len(wf.Folders) != 2 || wf.Folders[0] != "../" || wf.Folders[1] != "../../extra" {
		t.Fatalf("workspace.json paths are not portable: %+v", wf)
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
	mainA := filepath.Join(dir, "main-a")
	mainB := filepath.Join(dir, "main-b")
	for _, main := range []string{mainA, mainB} {
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	m := NewManager(filepath.Join(dir, "echo.json"))
	if _, err := m.Create(CreateRequest{Name: "Dup", MainPath: mainA}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Case-insensitive duplicate.
	if _, err := m.Create(CreateRequest{Name: "dup", MainPath: mainB}); err == nil {
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

func TestCreateLoadsLegacyWorkspaceFileAndNormalizesIt(t *testing.T) {
	directory := t.TempDir()
	main := filepath.Join(directory, "main")
	extra := filepath.Join(directory, "extra")
	for _, folder := range []string{main, extra} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	echoDir := filepath.Join(main, EchoDirName)
	if err := os.MkdirAll(echoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRawWorkspaceFile(t, echoDir, workspaceFile{
		Name: "From File", MainPath: main, Folders: []string{main, extra},
		SearchParentGitRepositories: true,
	})

	manager := NewManager(filepath.Join(directory, "echo.json"))
	workspace, err := manager.Create(CreateRequest{Name: "Ignored", MainPath: main})
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Name != "From File" || workspace.MainPath != main || len(workspace.Folders) != 2 || workspace.Folders[1] != extra {
		t.Fatalf("unexpected loaded workspace: %+v", workspace)
	}
	if !workspace.SearchParentGitRepositories {
		t.Fatal("expected workspace-owned setting to load")
	}
	wf := readRawWorkspaceFile(t, echoDir)
	if wf.MainPath != "../" || len(wf.Folders) != 2 || wf.Folders[0] != "../" || wf.Folders[1] != "../../extra" {
		t.Fatalf("legacy config was not normalized: %+v", wf)
	}
}

func TestCreateRebindsExistingRegistrationAndPreservesIDState(t *testing.T) {
	directory := t.TempDir()
	oldMain := filepath.Join(directory, "old", "project")
	newMain := filepath.Join(directory, "new", "project")
	for _, folder := range []string{oldMain, newMain} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := filepath.Join(directory, "echo.json")
	manager := NewManager(dataPath)
	original, err := manager.Create(CreateRequest{Name: "Portable", MainPath: oldMain})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetActive(original.ID); err != nil {
		t.Fatal(err)
	}
	data := appdata.NewStore(dataPath)
	if err := data.Update(func(file *appdata.File) error {
		file.SavedCommands = map[string][]appdata.SavedCommand{
			original.ID: {{ID: "command", Name: "Status", Command: "git status"}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	echoDir := filepath.Join(newMain, EchoDirName)
	if err := os.MkdirAll(echoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	missingExtra := filepath.Join(directory, "missing-extra")
	missingRelative, err := filepath.Rel(echoDir, missingExtra)
	if err != nil {
		t.Fatal(err)
	}
	writeRawWorkspaceFile(t, echoDir, workspaceFile{
		Name: "Portable", MainPath: "../", Folders: []string{"../", filepath.ToSlash(missingRelative)},
	})
	rebound, err := manager.Create(CreateRequest{Name: "Ignored", MainPath: newMain})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.ID != original.ID || rebound.MainPath != newMain {
		t.Fatalf("registration was not rebound: original=%+v rebound=%+v", original, rebound)
	}
	if len(rebound.Folders) != 2 || rebound.Folders[1] != missingExtra {
		t.Fatalf("missing additional folder was not retained: %#v", rebound.Folders)
	}
	stored, err := data.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Workspaces) != 1 || stored.Workspaces[0].ID != original.ID || stored.Workspaces[0].MainPath != newMain {
		t.Fatalf("unexpected registrations after rebind: %+v", stored.Workspaces)
	}
	if stored.ActiveWorkspaceID != original.ID || len(stored.SavedCommands[original.ID]) != 1 {
		t.Fatalf("workspace-ID state was not preserved: %+v", stored)
	}
}

func TestCreateRejectsMalformedOrMismatchedWorkspaceFileWithoutRewriting(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		code string
	}{
		{name: "malformed", data: `{`, code: ConfigMalformed},
		{name: "mismatched main", data: `{"name":"Wrong","mainPath":"../../other","folders":["../../other"]}`, code: ConfigMainMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			main := filepath.Join(directory, "main")
			for _, folder := range []string{main, filepath.Join(directory, "other")} {
				if err := os.MkdirAll(folder, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			echoDir := filepath.Join(main, EchoDirName)
			if err := os.MkdirAll(echoDir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(echoDir, "workspace.json")
			if err := os.WriteFile(path, []byte(test.data), 0o644); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(filepath.Join(directory, "echo.json"))
			_, err := manager.Create(CreateRequest{Name: "Ignored", MainPath: main})
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Code != test.code {
				t.Fatalf("expected %s, got %T %v", test.code, err, err)
			}
			unchanged, readErr := os.ReadFile(path)
			if readErr != nil || string(unchanged) != test.data {
				t.Fatalf("invalid config was rewritten: %q %v", unchanged, readErr)
			}
		})
	}
}

func TestWorkspaceFileIsAuthoritativeAndSettingsStayPortable(t *testing.T) {
	directory := t.TempDir()
	main := filepath.Join(directory, "main")
	extra := filepath.Join(directory, "extra")
	for _, folder := range []string{main, extra} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(filepath.Join(directory, "echo.json"))
	created, err := manager.Create(CreateRequest{Name: "Original", MainPath: main})
	if err != nil {
		t.Fatal(err)
	}
	echoDir := filepath.Join(main, EchoDirName)
	writeRawWorkspaceFile(t, echoDir, workspaceFile{
		Name: "Edited", MainPath: "../", Folders: []string{"../", "../../extra"},
	})
	loaded, ok, err := manager.Get(created.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if loaded.Name != "Edited" || len(loaded.Folders) != 2 || loaded.Folders[1] != extra {
		t.Fatalf("workspace file did not override registry mirror: %+v", loaded)
	}
	other := filepath.Join(directory, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(CreateRequest{Name: "edited", MainPath: other}); err == nil {
		t.Fatal("authoritative workspace name was not used for uniqueness")
	}
	if _, err := manager.SetSearchParentGitRepositories(created.ID, true); err != nil {
		t.Fatal(err)
	}
	wf := readRawWorkspaceFile(t, echoDir)
	if wf.MainPath != "../" || wf.Folders[0] != "../" || wf.Folders[1] != "../../extra" || !wf.SearchParentGitRepositories {
		t.Fatalf("settings update reintroduced non-portable paths: %+v", wf)
	}
}

func TestPortableWorkspacePathFallsBackAcrossWindowsVolumes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("different-volume filepath.Rel behavior is Windows-specific")
	}
	echoDir := `C:\project\.echo`
	if got := portableWorkspacePath(echoDir, `Z:\shared`); got != `Z:\shared` {
		t.Fatalf("expected absolute fallback, got %q", got)
	}
}

func writeRawWorkspaceFile(t *testing.T, echoDir string, wf workspaceFile) {
	t.Helper()
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(echoDir, "workspace.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRawWorkspaceFile(t *testing.T, echoDir string) workspaceFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(echoDir, "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wf workspaceFile
	if err := json.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	return wf
}
