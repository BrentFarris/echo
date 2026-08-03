package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIsDelveDebugBinaryName(t *testing.T) {
	for _, name := range []string{
		"__debug_bin",
		"__debug_bin123456",
		"__debug_bin.exe",
		"__debug_bin.exe97672765",
	} {
		if !isDelveDebugBinaryName(name) {
			t.Errorf("isDelveDebugBinaryName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"debug_bin.exe",
		"__debug_bin_custom.exe",
		"__debug_bin.exe.backup",
		"__debug_bin.exe~",
	} {
		if isDelveDebugBinaryName(name) {
			t.Errorf("isDelveDebugBinaryName(%q) = true, want false", name)
		}
	}
}

func TestDelveDebugBinaryCleanupRemovesOnlyFilesCreatedAfterLaunch(t *testing.T) {
	root := t.TempDir()
	preexisting := filepath.Join(root, "__debug_bin.exe111")
	writeDebugTestFile(t, preexisting)
	cleanup := newDelveDebugBinaryCleanup(root, nil)
	if cleanup == nil {
		t.Fatal("expected cleanup callback")
	}

	created := []string{
		filepath.Join(root, "__debug_bin.exe"),
		filepath.Join(root, "__debug_bin.exe97672765"),
		filepath.Join(root, "__debug_bin222"),
	}
	for _, path := range created {
		writeDebugTestFile(t, path)
	}
	unrelated := filepath.Join(root, "__debug_bin_custom.exe")
	writeDebugTestFile(t, unrelated)

	cleanup()

	for _, path := range created {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("debug binary %s still exists (stat error %v)", path, err)
		}
	}
	for _, path := range []string{preexisting, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("preserved file %s: %v", path, err)
		}
	}
}

func TestDebugAdapterStopCleansDelveDebugBinaries(t *testing.T) {
	root := t.TempDir()
	cleanup := newDelveDebugBinaryCleanup(root, nil)
	created := filepath.Join(root, "__debug_bin.exe97672765")
	writeDebugTestFile(t, created)

	(&debugAdapterHandle{cleanup: cleanup}).stop()

	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("debug binary still exists after stop (stat error %v)", err)
	}
}

func TestSystemServiceShutdownCleansDelveDebugBinaries(t *testing.T) {
	root := t.TempDir()
	cleanup := newDelveDebugBinaryCleanup(root, nil)
	created := filepath.Join(root, "__debug_bin.exe97672765")
	writeDebugTestFile(t, created)

	ctx, cancel := context.WithCancel(context.Background())
	service := &SystemService{}
	manager := &debugManager{service: service}
	manager.session = &debugSession{
		workspace:   debugTestWorkspace(root),
		id:          "session",
		adapterType: "go",
		status:      DebugStatusRunning,
		ctx:         ctx,
		cancel:      cancel,
		adapter:     &debugAdapterHandle{cleanup: cleanup},
		breakpoints: make(map[string][]DebugBreakpoint),
	}
	service.debugger = manager

	service.Shutdown()

	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("debug binary still exists after service shutdown (stat error %v)", err)
	}
}

func writeDebugTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("debug"), 0o600); err != nil {
		t.Fatal(err)
	}
}
