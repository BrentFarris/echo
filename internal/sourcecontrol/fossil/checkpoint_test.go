package fossil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func newCheckpointFileTest(t *testing.T) (*Provider, *repositoryState, string) {
	t.Helper()
	directory := t.TempDir()
	root := filepath.Join(directory, "checkout")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(directory, "config", "echo.json")
	manager := workspaces.NewManager(settingsPath)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "Checkpoint", MainPath: root})
	if err != nil {
		t.Fatal(err)
	}
	fs := workspacefs.New(manager, settingsPath)
	t.Cleanup(fs.Close)
	roots, err := fs.Roots(workspace.ID)
	if err != nil || len(roots) != 1 {
		t.Fatalf("workspace roots = %#v, %v", roots, err)
	}
	if roots[0].BlockedReason != "" {
		t.Skipf("workspace filesystem is unavailable in this test environment: %s", roots[0].BlockedReason)
	}
	provider := New(manager, fs, nil, filepath.Join(directory, "config", "source-control", "fossil"))
	state := &repositoryState{
		workspaceID: workspace.ID,
		root:        root,
		repository:  filepath.Join(directory, "repository.fossil"),
		scopes: []repositoryScope{{
			Scope: sourceControlScope(roots[0].ID, roots[0].Label, ""), rootPath: root,
		}},
	}
	return provider, state, root
}

func TestCheckpointCaptureAndMaterializeExactFileState(t *testing.T) {
	provider, state, root := newCheckpointFileTest(t)
	binary := []byte{'E', 'c', 'h', 'o', 0, 0xff, '\n'}
	pathValue := "data/資料 file.bin"
	hostPath := filepath.Join(root, filepath.FromSlash(pathValue))
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostPath, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	entry, blobs, err := provider.captureFileState(state, pathValue, "", "EDITED", "modified")
	if err != nil || !entry.Exists || entry.Symlink || entry.Blob == "" {
		t.Fatalf("captured file state = %#v, %v (cause %v)", entry, err, errors.Unwrap(err))
	}
	manifest := checkpoint.Manifest{
		Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(),
		CheckoutFingerprint: state.checkoutFingerprint(), Baseline: "baseline", Generation: 1, Entries: []checkpoint.FileState{entry},
	}
	if err := provider.checkpoints.ReplaceManifest(manifest, blobs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.materializeFileState(state, entry); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(hostPath)
	if err != nil || !bytes.Equal(content, binary) {
		t.Fatalf("materialized content = %x, %v", content, err)
	}
	if runtime.GOOS != "windows" {
		if info, statErr := os.Stat(hostPath); statErr != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("materialized executable mode = %v, %v", info, statErr)
		}
	}
}

func TestCheckpointCaptureAndMaterializeSymlink(t *testing.T) {
	provider, state, root := newCheckpointFileTest(t)
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "current-link")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	entry, _, err := provider.captureFileState(state, "current-link", "", "EDITED", "modified")
	if err != nil || !entry.Exists || !entry.Symlink || entry.SymlinkTarget != "target.txt" {
		t.Fatalf("captured symlink state = %#v, %v (cause %v)", entry, err, errors.Unwrap(err))
	}
	manifest := checkpoint.Manifest{
		Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(),
		CheckoutFingerprint: state.checkoutFingerprint(), Baseline: "baseline", Generation: 1, Entries: []checkpoint.FileState{entry},
	}
	if err := provider.checkpoints.ReplaceManifest(manifest, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkPath, []byte("ordinary file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := provider.materializeFileState(state, entry); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(linkPath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("materialized link info = %v, %v", info, err)
	}
	if target, readErr := os.Readlink(linkPath); readErr != nil || target != "target.txt" {
		t.Fatalf("materialized link target = %q, %v", target, readErr)
	}
}

func TestRecoveryRejectsInconsistentJournalPhases(t *testing.T) {
	for _, test := range []struct {
		name        string
		phase       string
		newBaseline string
	}{
		{name: "unknown phase", phase: "future-phase"},
		{name: "committed without check-in", phase: journalCommitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, state, _ := newCheckpointFileTest(t)
			journal := checkpoint.Journal{
				Version: checkpoint.Version, WorkspaceID: state.workspaceID, ProviderID: ID, RepositoryID: state.repositoryID(),
				CheckoutFingerprint: state.checkoutFingerprint(), Baseline: "baseline", NewBaseline: test.newBaseline, Phase: test.phase,
				Current: []checkpoint.FileState{{Path: "tracked.txt"}},
			}
			if err := provider.checkpoints.WriteJournal(journal, nil); err != nil {
				t.Fatal(err)
			}
			err := provider.recoverProtectedCommit(context.Background(), state)
			var sourceErr *sourcecontrol.Error
			if !errors.As(err, &sourceErr) || sourceErr.Code != "protected_changes_recovery_required" {
				t.Fatalf("inconsistent journal recovery error = %v", err)
			}
			loaded, loadErr := provider.checkpoints.LoadJournal(state.workspaceID, ID, state.repositoryID())
			if loadErr != nil || loaded == nil {
				t.Fatalf("inconsistent journal was not preserved: %#v, %v", loaded, loadErr)
			}
		})
	}
}
