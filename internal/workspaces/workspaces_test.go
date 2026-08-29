package workspaces

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSandboxConfigLegacyDefaultsAndExplicitPersistence(t *testing.T) {
	directory := t.TempDir()
	main := filepath.Join(directory, "legacy workspace")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(filepath.Join(directory, "echo.json"))
	workspace, err := manager.Create(CreateRequest{Name: "Legacy", MainPath: main})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(main, EchoDirName, "workspace.json")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), `"sandbox"`) {
		t.Fatal("new/legacy workspace was rewritten with opt-in sandbox policy")
	}
	loaded, ok, err := manager.Get(workspace.ID)
	if err != nil || !ok {
		t.Fatalf("load workspace: ok=%v err=%v", ok, err)
	}
	if loaded.Sandbox != DefaultSandboxConfig() {
		t.Fatalf("unexpected defaults: %+v", loaded.Sandbox)
	}
	afterRead, _ := os.ReadFile(configPath)
	if string(afterRead) != string(before) {
		t.Fatal("reading a legacy workspace rewrote workspace.json")
	}

	want := SandboxConfig{Enabled: true, CPULimit: 8, MemoryMiB: 12288, IdleTimeoutMinutes: 0}
	updated, err := manager.SetSandboxConfig(workspace.ID, want)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Sandbox != want {
		t.Fatalf("sandbox config = %+v, want %+v", updated.Sandbox, want)
	}
	persisted, _ := os.ReadFile(configPath)
	if !strings.Contains(string(persisted), `"sandbox"`) || !strings.Contains(string(persisted), `"memoryMiB": 12288`) {
		t.Fatalf("sandbox config was not persisted: %s", persisted)
	}
}

func TestNormalizeSandboxConfigBoundaries(t *testing.T) {
	defaults, err := NormalizeSandboxConfig(SandboxConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.CPULimit != 4 || defaults.MemoryMiB != 6144 || defaults.IdleTimeoutMinutes != 0 {
		t.Fatalf("unexpected normalized explicit values: %+v", defaults)
	}
	valid := SandboxConfig{Enabled: true, CPULimit: 16, MemoryMiB: 32768, IdleTimeoutMinutes: 1440}
	if normalized, err := NormalizeSandboxConfig(valid); err != nil || normalized != valid {
		t.Fatalf("valid boundary rejected: %+v %v", normalized, err)
	}
	for _, invalid := range []SandboxConfig{
		{CPULimit: 17, MemoryMiB: 6144}, {CPULimit: -1, MemoryMiB: 6144},
		{CPULimit: 4, MemoryMiB: 4095}, {CPULimit: 4, MemoryMiB: 32769},
		{CPULimit: 4, MemoryMiB: 6144, IdleTimeoutMinutes: -1}, {CPULimit: 4, MemoryMiB: 6144, IdleTimeoutMinutes: 1441},
	} {
		if _, err := NormalizeSandboxConfig(invalid); err == nil {
			t.Fatalf("invalid config accepted: %+v", invalid)
		}
	}
}

func TestSandboxConfigJSONDistinguishesMissingIdleFromExplicitZero(t *testing.T) {
	var missing SandboxConfig
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.CPULimit != 4 || missing.MemoryMiB != 6144 || missing.IdleTimeoutMinutes != 30 {
		t.Fatalf("missing values did not receive defaults: %+v", missing)
	}
	var explicit SandboxConfig
	if err := json.Unmarshal([]byte(`{"enabled":true,"idleTimeoutMinutes":0}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.IdleTimeoutMinutes != 0 {
		t.Fatalf("explicit zero idle timeout was lost: %+v", explicit)
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

func TestUpdateWorkspaceEditableFieldsAndPreservesConfiguration(t *testing.T) {
	directory := t.TempDir()
	main := filepath.Join(directory, "main")
	extra := filepath.Join(directory, "extra")
	for _, folder := range []string{main, extra} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(filepath.Join(directory, "echo.json"))
	workspace, err := manager.Create(CreateRequest{
		Name: "Original", MainPath: main, Icon: &Icon{Data: []byte("old"), Ext: "png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetSearchParentGitRepositories(workspace.ID, true); err != nil {
		t.Fatal(err)
	}
	wantSandbox := SandboxConfig{Enabled: true, CPULimit: 6, MemoryMiB: 8192, IdleTimeoutMinutes: 12}
	if _, err := manager.SetSandboxConfig(workspace.ID, wantSandbox); err != nil {
		t.Fatal(err)
	}

	updated, err := manager.Update(workspace.ID, UpdateRequest{
		Name: "Renamed", Folders: []string{extra, extra, main}, Icon: &Icon{Data: []byte("new"), Ext: "webp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Renamed" || updated.MainPath != main || updated.IconExt != "webp" {
		t.Fatalf("unexpected update: %+v", updated)
	}
	if len(updated.Folders) != 2 || updated.Folders[0] != main || updated.Folders[1] != extra {
		t.Fatalf("folders were not normalized: %v", updated.Folders)
	}
	if !updated.SearchParentGitRepositories || updated.Sandbox != wantSandbox {
		t.Fatalf("unrelated configuration was lost: %+v", updated)
	}
	if _, err := os.Stat(filepath.Join(main, EchoDirName, "icon.png")); !os.IsNotExist(err) {
		t.Fatalf("old icon remains: %v", err)
	}
	icon, err := os.ReadFile(filepath.Join(main, EchoDirName, "icon.webp"))
	if err != nil || string(icon) != "new" {
		t.Fatalf("replacement icon = %q, %v", icon, err)
	}
	if _, err := manager.Update(workspace.ID, UpdateRequest{
		Name: "Renamed", Folders: []string{extra}, Icon: &Icon{Data: []byte("newer"), Ext: "webp"},
	}); err != nil {
		t.Fatalf("replace same-extension icon: %v", err)
	}
	icon, err = os.ReadFile(filepath.Join(main, EchoDirName, "icon.webp"))
	if err != nil || string(icon) != "newer" {
		t.Fatalf("same-extension replacement icon = %q, %v", icon, err)
	}

	withoutIcon, err := manager.Update(workspace.ID, UpdateRequest{Name: "Renamed", Folders: []string{extra}, RemoveIcon: true})
	if err != nil {
		t.Fatal(err)
	}
	if withoutIcon.IconExt != "" {
		t.Fatalf("icon was not cleared: %+v", withoutIcon)
	}
	if matches, _ := filepath.Glob(filepath.Join(main, EchoDirName, "icon.*")); len(matches) != 0 {
		t.Fatalf("icon files remain: %v", matches)
	}
}

func TestUpdateWorkspaceValidatesNameFoldersAndIconAction(t *testing.T) {
	directory := t.TempDir()
	mainA := filepath.Join(directory, "a")
	mainB := filepath.Join(directory, "b")
	for _, folder := range []string{mainA, mainB} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manager := NewManager(filepath.Join(directory, "echo.json"))
	first, err := manager.Create(CreateRequest{Name: "First", MainPath: mainA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(CreateRequest{Name: "Second", MainPath: mainB}); err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]UpdateRequest{
		"missing name":     {Name: ""},
		"duplicate name":   {Name: "second"},
		"missing folder":   {Name: "First", Folders: []string{filepath.Join(directory, "missing")}},
		"conflicting icon": {Name: "First", Icon: &Icon{Data: []byte("x"), Ext: "png"}, RemoveIcon: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.Update(first.ID, request); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestUnregisterRetainsWorkspaceFilesAndSelectsNextValidWorkspace(t *testing.T) {
	directory := t.TempDir()
	manager := NewManager(filepath.Join(directory, "echo.json"))
	created := make([]Workspace, 0, 3)
	for _, name := range []string{"First", "Second", "Third"} {
		main := filepath.Join(directory, strings.ToLower(name))
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatal(err)
		}
		workspace, err := manager.Create(CreateRequest{Name: name, MainPath: main})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, workspace)
	}
	if err := manager.SetActive(created[1].ID); err != nil {
		t.Fatal(err)
	}
	activeID, err := manager.Unregister(created[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if activeID != created[2].ID {
		t.Fatalf("active workspace = %q, want %q", activeID, created[2].ID)
	}
	if _, err := os.Stat(filepath.Join(created[1].MainPath, EchoDirName, "workspace.json")); err != nil {
		t.Fatalf("workspace-owned config was removed: %v", err)
	}

	if err := os.RemoveAll(created[0].MainPath); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Unregister(created[0].ID); err != nil {
		t.Fatalf("unregister unavailable workspace: %v", err)
	}
	activeID, err = manager.Unregister(created[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if activeID != "" {
		t.Fatalf("expected no active workspace, got %q", activeID)
	}
	if _, err := manager.Unregister("missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("missing unregister error = %v", err)
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

func TestListPrunesMissingWorkspaceConfigAndClearsActiveRegistration(t *testing.T) {
	directory := t.TempDir()
	staleMain := filepath.Join(directory, "stale")
	validMain := filepath.Join(directory, "valid")
	for _, main := range []string{staleMain, validMain} {
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := filepath.Join(directory, "echo.json")
	manager := NewManager(dataPath)
	stale, err := manager.Create(CreateRequest{Name: "Stale", MainPath: staleMain})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := manager.Create(CreateRequest{Name: "Valid", MainPath: validMain})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetActive(stale.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(staleMain, EchoDirName, "workspace.json")); err != nil {
		t.Fatal(err)
	}

	listed, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != valid.ID {
		t.Fatalf("missing workspace config was still listed: %+v", listed)
	}
	stored, err := appdata.NewStore(dataPath).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Workspaces) != 1 || stored.Workspaces[0].ID != valid.ID {
		t.Fatalf("missing workspace registration was not pruned: %+v", stored.Workspaces)
	}
	if stored.ActiveWorkspaceID != "" {
		t.Fatalf("stale active workspace id was not cleared: %q", stored.ActiveWorkspaceID)
	}
	if _, ok, err := manager.Get(stale.ID); err != nil || ok {
		t.Fatalf("pruned workspace remained addressable: ok=%v err=%v", ok, err)
	}
}

func TestListRetainsUnavailableAndMalformedWorkspaceRegistrations(t *testing.T) {
	directory := t.TempDir()
	unavailableMain := filepath.Join(directory, "unavailable")
	malformedMain := filepath.Join(directory, "malformed")
	for _, main := range []string{unavailableMain, malformedMain} {
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := filepath.Join(directory, "echo.json")
	manager := NewManager(dataPath)
	unavailable, err := manager.Create(CreateRequest{Name: "Unavailable", MainPath: unavailableMain})
	if err != nil {
		t.Fatal(err)
	}
	malformed, err := manager.Create(CreateRequest{Name: "Malformed", MainPath: malformedMain})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(unavailableMain, unavailableMain+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedMain, EchoDirName, "workspace.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}

	listed, err := manager.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("recoverable registrations should remain listed: %+v", listed)
	}
	stored, err := appdata.NewStore(dataPath).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Workspaces) != 2 || stored.Workspaces[0].ID != unavailable.ID || stored.Workspaces[1].ID != malformed.ID {
		t.Fatalf("recoverable registrations were unexpectedly pruned: %+v", stored.Workspaces)
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
