package fossil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

type fossilIntegration struct {
	binary       string
	root         string
	repository   string
	workspaceID  string
	provider     *Provider
	repositoryID string
	fs           *workspacefs.Service
	request      int
}

func newFossilIntegration(t *testing.T) *fossilIntegration {
	t.Helper()
	binary, err := exec.LookPath("fossil")
	if err != nil {
		t.Skip("fossil executable is not installed")
	}
	directory := t.TempDir()
	repository := filepath.Join(directory, "repository.fossil")
	root := filepath.Join(directory, "checkout space ü")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runFossilIntegration(t, binary, directory, "init", "--admin-user", "echo-test", repository)
	runFossilIntegration(t, binary, root, "open", repository, "--nosync", "--user", "echo-test")
	runFossilIntegration(t, binary, root, "user", "default", "echo-test")
	writeFossilIntegrationFile(t, root, "tracked.txt", "before\n")
	writeFossilIntegrationFile(t, root, "delete-me.txt", "remove me\n")
	runFossilIntegration(t, binary, root, "add", "./tracked.txt", "./delete-me.txt")
	runFossilIntegration(t, binary, root, "commit", "--nosync", "--no-prompt", "--no-warnings", "-m", "initial file")

	dataPath := filepath.Join(directory, "config", "echo.json")
	manager := workspaces.NewManager(dataPath)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "Fossil Integration", MainPath: root})
	if err != nil {
		t.Fatal(err)
	}
	fs := workspacefs.New(manager, dataPath)
	t.Cleanup(fs.Close)
	provider := New(manager, fs, nil, filepath.Join(directory, "config", "source-control", "fossil"))
	repositories, err := provider.Repositories(context.Background(), workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].ProviderID != ID || !repositories[0].Available {
		t.Fatalf("unexpected Fossil discovery: %#v", repositories)
	}
	return &fossilIntegration{
		binary: binary, root: root, repository: repository, workspaceID: workspace.ID,
		provider: provider, repositoryID: repositories[0].ID, fs: fs,
	}
}

func TestFossilIntegrationProtectedChangesFreezeDiscardAndCommit(t *testing.T) {
	integration := newFossilIntegration(t)
	writeFossilIntegrationFile(t, integration.root, "delete-me.txt", "user stash content\n")
	runFossilIntegration(t, integration.binary, integration.root, "stash", "save", "-m", "user-owned stash")
	userStashes, err := integration.provider.Metadata(context.Background(), integration.workspaceID, integration.repositoryID)
	if err != nil || len(userStashes.Stashes) != 1 {
		t.Fatalf("user-created stash setup = %#v, %v", userStashes.Stashes, err)
	}
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "protected A\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"tracked.txt"}})
	status := integration.status(t)
	assertFossilIntegrationChange(t, status, protectedGroupID, "tracked.txt", "modified")
	if hasFossilIntegrationChange(status, "working", "tracked.txt") {
		t.Fatalf("unchanged protected version also appeared as working: %#v", status.Groups)
	}
	_, blockedErr := integration.provider.Action(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.ActionRequest{
		RequestID: "ordinary-commit-blocked", Action: "commit_all", ExpectedRevision: status.Revision, Message: "must not commit",
	})
	var blocked *sourcecontrol.Error
	if !errors.As(blockedErr, &blocked) || blocked.Code != "protected_changes_active" {
		t.Fatalf("ordinary commit while protection is active = %v", blockedErr)
	}

	protectedDiff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{Kind: "change", GroupID: protectedGroupID, Path: "tracked.txt"})
	if err != nil || protectedDiff.Original.Content != "before\n" || protectedDiff.Modified.Content != "protected A\n" || protectedDiff.Editable {
		t.Fatalf("protected diff = %#v, %v", protectedDiff, err)
	}
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "updated protected A2\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"tracked.txt"}})
	if hasFossilIntegrationChange(integration.status(t), "working", "tracked.txt") {
		t.Fatal("updating protection left the newly frozen version in Changes")
	}
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "later B\n")
	status = integration.status(t)
	assertFossilIntegrationChange(t, status, protectedGroupID, "tracked.txt", "modified")
	assertFossilIntegrationChange(t, status, "working", "tracked.txt", "modified")
	laterDiff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{Kind: "change", GroupID: "working", Path: "tracked.txt"})
	if err != nil || laterDiff.Original.Content != "updated protected A2\n" || laterDiff.Modified.Content != "later B\n" || !laterDiff.Editable {
		t.Fatalf("later diff = %#v, %v", laterDiff, err)
	}

	integration.action(t, sourcecontrol.ActionRequest{Action: "discard", Paths: []string{"tracked.txt"}, Confirmed: true})
	if content, err := os.ReadFile(filepath.Join(integration.root, "tracked.txt")); err != nil || string(content) != "updated protected A2\n" {
		t.Fatalf("discard did not restore updated protected A2: %q, %v", content, err)
	}
	status = integration.status(t)
	assertFossilIntegrationChange(t, status, protectedGroupID, "tracked.txt", "modified")
	if hasFossilIntegrationChange(status, "working", "tracked.txt") {
		t.Fatalf("discard left a later working version: %#v", status.Groups)
	}

	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "later B\n")
	beforeMetadata, err := integration.provider.Metadata(context.Background(), integration.workspaceID, integration.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "commit_protected", Message: "protected version"})
	if content, err := os.ReadFile(filepath.Join(integration.root, "tracked.txt")); err != nil || string(content) != "later B\n" {
		t.Fatalf("protected commit did not restore later B: %q, %v", content, err)
	}
	status = integration.status(t)
	if hasFossilIntegrationChange(status, protectedGroupID, "tracked.txt") {
		t.Fatalf("successful commit retained protection: %#v", status.Groups)
	}
	assertFossilIntegrationChange(t, status, "working", "tracked.txt", "modified")
	checkedIn := runFossilIntegration(t, integration.binary, integration.root, "finfo", "-p", "-r", "current", "./tracked.txt")
	if checkedIn != "updated protected A2\n" {
		t.Fatalf("protected check-in content = %q", checkedIn)
	}
	afterMetadata, err := integration.provider.Metadata(context.Background(), integration.workspaceID, integration.repositoryID)
	if err != nil || len(afterMetadata.Stashes) != len(beforeMetadata.Stashes) {
		t.Fatalf("protected workflow changed Fossil stashes: before=%#v after=%#v err=%v", beforeMetadata.Stashes, afterMetadata.Stashes, err)
	}
}

func TestFossilIntegrationProtectedCommitPreservesHiddenParentChanges(t *testing.T) {
	binary, err := exec.LookPath("fossil")
	if err != nil {
		t.Skip("fossil executable is not installed")
	}
	directory := t.TempDir()
	repository := filepath.Join(directory, "repository.fossil")
	checkout := filepath.Join(directory, "parent checkout")
	visibleRoot := filepath.Join(checkout, "visible")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	runFossilIntegration(t, binary, directory, "init", "--admin-user", "echo-test", repository)
	runFossilIntegration(t, binary, checkout, "open", repository, "--nosync", "--user", "echo-test")
	runFossilIntegration(t, binary, checkout, "user", "default", "echo-test")
	if err := os.MkdirAll(visibleRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFossilIntegrationFile(t, checkout, "visible/tracked.txt", "visible baseline\n")
	writeFossilIntegrationFile(t, checkout, "hidden.txt", "hidden baseline\n")
	runFossilIntegration(t, binary, checkout, "add", "./visible/tracked.txt", "./hidden.txt")
	runFossilIntegration(t, binary, checkout, "commit", "--nosync", "--no-prompt", "--no-warnings", "-m", "initial parent checkout")

	dataPath := filepath.Join(directory, "config", "echo.json")
	manager := workspaces.NewManager(dataPath)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "Nested Fossil", MainPath: visibleRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetSearchParentRepositories(workspace.ID, true); err != nil {
		t.Fatal(err)
	}
	fs := workspacefs.New(manager, dataPath)
	t.Cleanup(fs.Close)
	provider := New(manager, fs, nil, filepath.Join(directory, "config", "source-control", "fossil"))
	repositories, err := provider.Repositories(context.Background(), workspace.ID)
	if err != nil || len(repositories) != 1 || !repositories[0].Parent {
		t.Fatalf("parent Fossil discovery = %#v, %v", repositories, err)
	}

	writeFossilIntegrationFile(t, checkout, "visible/tracked.txt", "protected visible\n")
	writeFossilIntegrationFile(t, checkout, "hidden.txt", "later hidden\n")
	repositoryID := repositories[0].ID
	status, err := provider.Status(context.Background(), workspace.ID, repositoryID)
	if err != nil || status.HiddenChangeCount != 1 {
		t.Fatalf("parent checkout hidden status = %#v, %v", status, err)
	}
	if _, err := provider.Action(context.Background(), workspace.ID, repositoryID, sourcecontrol.ActionRequest{
		RequestID: "protect-visible", Action: "protect", ExpectedRevision: status.Revision, Paths: []string{"visible/tracked.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	status, err = provider.Status(context.Background(), workspace.ID, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Action(context.Background(), workspace.ID, repositoryID, sourcecontrol.ActionRequest{
		RequestID: "commit-visible", Action: "commit_protected", ExpectedRevision: status.Revision, Message: "protected visible path",
	}); err != nil {
		t.Fatal(err)
	}
	if checkedIn := runFossilIntegration(t, binary, checkout, "finfo", "-p", "-r", "current", "./visible/tracked.txt"); checkedIn != "protected visible\n" {
		t.Fatalf("protected visible check-in = %q", checkedIn)
	}
	if checkedIn := runFossilIntegration(t, binary, checkout, "finfo", "-p", "-r", "current", "./hidden.txt"); checkedIn != "hidden baseline\n" {
		t.Fatalf("hidden change leaked into protected commit = %q", checkedIn)
	}
	if content, readErr := os.ReadFile(filepath.Join(checkout, "hidden.txt")); readErr != nil || string(content) != "later hidden\n" {
		t.Fatalf("hidden working change was not preserved: %q, %v", content, readErr)
	}
}

func TestFossilIntegrationProtectsUntrackedWithoutSchedulingAdd(t *testing.T) {
	integration := newFossilIntegration(t)
	writeFossilIntegrationFile(t, integration.root, "new file ü.txt", "new A\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"new file ü.txt"}})
	status := integration.status(t)
	assertFossilIntegrationChange(t, status, protectedGroupID, "new file ü.txt", "added")
	if hasFossilIntegrationChange(status, "untracked", "new file ü.txt") {
		t.Fatalf("protected addition remained in the untracked group: %#v", status.Groups)
	}
	changes := runFossilIntegration(t, integration.binary, integration.root, "changes", "--classify", "--rel-paths")
	if strings.Contains(changes, "ADDED") {
		t.Fatalf("protect scheduled a Fossil add: %q", changes)
	}
	extras := runFossilIntegration(t, integration.binary, integration.root, "extras", "--rel-paths")
	if !strings.Contains(extras, "new file ü.txt") {
		t.Fatalf("protected addition is not still a Fossil extra: %q", extras)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "unprotect", Paths: []string{"new file ü.txt"}})
	assertFossilIntegrationChange(t, integration.status(t), "untracked", "new file ü.txt", "untracked")
	writeFossilIntegrationFile(t, integration.root, "scheduled.txt", "scheduled addition\n")
	runFossilIntegration(t, integration.binary, integration.root, "add", "./scheduled.txt")
	integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"scheduled.txt"}})
	integration.action(t, sourcecontrol.ActionRequest{Action: "unprotect", Paths: []string{"scheduled.txt"}})
	if changes := runFossilIntegration(t, integration.binary, integration.root, "changes", "--classify", "--rel-paths"); !strings.Contains(changes, "ADDED") || !strings.Contains(changes, "scheduled.txt") {
		t.Fatalf("unprotect changed Fossil's scheduled addition: %q", changes)
	}

	// Protecting an extra is still a one-click inclusion operation. The file is
	// scheduled only inside the protected-commit transaction, and its later
	// working version is restored against the new baseline afterward.
	integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"new file ü.txt"}})
	writeFossilIntegrationFile(t, integration.root, "new file ü.txt", "new B\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "commit_protected", Message: "protected untracked addition"})
	checkedIn := runFossilIntegration(t, integration.binary, integration.root, "finfo", "-p", "-r", "current", "./new file ü.txt")
	if checkedIn != "new A\n" {
		t.Fatalf("protected added-file check-in content = %q", checkedIn)
	}
	if content, err := os.ReadFile(filepath.Join(integration.root, "new file ü.txt")); err != nil || string(content) != "new B\n" {
		t.Fatalf("protected added-file commit did not restore later content: %q, %v", content, err)
	}
	assertFossilIntegrationChange(t, integration.status(t), "working", "new file ü.txt", "modified")
}

func TestFossilIntegrationDiscardAllKeepsProtectedSnapshot(t *testing.T) {
	integration := newFossilIntegration(t)
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "protected A\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"tracked.txt"}})
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "later B\n")
	writeFossilIntegrationFile(t, integration.root, "delete-me.txt", "unprotected edit\n")
	writeFossilIntegrationFile(t, integration.root, "throw-away.txt", "trash me\n")

	result := integration.action(t, sourcecontrol.ActionRequest{Action: "discard_all", Confirmed: true})
	if len(result.TrashIDs) != 1 {
		t.Fatalf("discard all trash IDs = %#v", result.TrashIDs)
	}
	if content, err := os.ReadFile(filepath.Join(integration.root, "tracked.txt")); err != nil || string(content) != "protected A\n" {
		t.Fatalf("discard all did not restore protected A: %q, %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(integration.root, "delete-me.txt")); err != nil || string(content) != "remove me\n" {
		t.Fatalf("discard all did not revert unprotected tracked edit: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(integration.root, "throw-away.txt")); !os.IsNotExist(err) {
		t.Fatalf("discard all did not move untracked file to Trash: %v", err)
	}
	status := integration.status(t)
	assertFossilIntegrationChange(t, status, protectedGroupID, "tracked.txt", "modified")
	if hasFossilIntegrationChange(status, "working", "tracked.txt") {
		t.Fatalf("discard all retained the later protected-file edit: %#v", status.Groups)
	}
}

func TestFossilIntegrationProtectedDeletionAndRename(t *testing.T) {
	t.Run("deletion", func(t *testing.T) {
		integration := newFossilIntegration(t)
		if err := os.Remove(filepath.Join(integration.root, "delete-me.txt")); err != nil {
			t.Fatal(err)
		}
		integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"delete-me.txt"}})
		assertFossilIntegrationChange(t, integration.status(t), protectedGroupID, "delete-me.txt", "deleted")

		writeFossilIntegrationFile(t, integration.root, "delete-me.txt", "later recreation\n")
		integration.action(t, sourcecontrol.ActionRequest{Action: "commit_protected", Message: "protected deletion"})
		if _, exists, err := integration.provider.revisionFile(context.Background(), integration.providerState(t), "current", "delete-me.txt"); err != nil || exists {
			t.Fatalf("protected deletion was not committed: exists=%v err=%v", exists, err)
		}
		if content, err := os.ReadFile(filepath.Join(integration.root, "delete-me.txt")); err != nil || string(content) != "later recreation\n" {
			t.Fatalf("later recreated file was not restored: %q, %v", content, err)
		}
		assertFossilIntegrationChange(t, integration.status(t), "working", "delete-me.txt", "added")
	})

	t.Run("rename", func(t *testing.T) {
		integration := newFossilIntegration(t)
		runFossilIntegration(t, integration.binary, integration.root, "mv", "--hard", "./tracked.txt", "./renamed ü.txt")
		writeFossilIntegrationFile(t, integration.root, "renamed ü.txt", "protected rename A\n")
		integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"renamed ü.txt"}})
		protectedDiff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{
			Kind: "change", GroupID: protectedGroupID, Path: "renamed ü.txt", OldPath: "tracked.txt",
		})
		if err != nil || protectedDiff.Original.Content != "before\n" || protectedDiff.Modified.Content != "protected rename A\n" || protectedDiff.Editable {
			t.Fatalf("protected rename diff = %#v, %v", protectedDiff, err)
		}
		writeFossilIntegrationFile(t, integration.root, "renamed ü.txt", "later rename B\n")
		laterDiff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{
			Kind: "change", GroupID: "working", Path: "renamed ü.txt", OldPath: "tracked.txt",
		})
		if err != nil || laterDiff.Original.Content != "protected rename A\n" || laterDiff.Modified.Content != "later rename B\n" || !laterDiff.Editable {
			t.Fatalf("later renamed-file diff = %#v, %v", laterDiff, err)
		}
		integration.action(t, sourcecontrol.ActionRequest{Action: "commit_protected", Message: "protected rename"})

		if _, exists, err := integration.provider.revisionFile(context.Background(), integration.providerState(t), "current", "tracked.txt"); err != nil || exists {
			t.Fatalf("old rename endpoint remains in the check-in: exists=%v err=%v", exists, err)
		}
		checkedIn := runFossilIntegration(t, integration.binary, integration.root, "finfo", "-p", "-r", "current", "./renamed ü.txt")
		if checkedIn != "protected rename A\n" {
			t.Fatalf("protected rename check-in content = %q", checkedIn)
		}
		if content, err := os.ReadFile(filepath.Join(integration.root, "renamed ü.txt")); err != nil || string(content) != "later rename B\n" {
			t.Fatalf("later renamed-file content was not restored: %q, %v", content, err)
		}
		assertFossilIntegrationChange(t, integration.status(t), "working", "renamed ü.txt", "modified")
	})

	t.Run("rename after protection", func(t *testing.T) {
		integration := newFossilIntegration(t)
		writeFossilIntegrationFile(t, integration.root, "tracked.txt", "protected before rename\n")
		integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"tracked.txt"}})
		runFossilIntegration(t, integration.binary, integration.root, "mv", "--hard", "./tracked.txt", "./later-name.txt")
		writeFossilIntegrationFile(t, integration.root, "later-name.txt", "later renamed version\n")
		status := integration.status(t)
		assertFossilIntegrationChange(t, status, protectedGroupID, "tracked.txt", "modified")
		assertFossilIntegrationChange(t, status, "working", "later-name.txt", "renamed")
		laterDiff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{
			Kind: "change", GroupID: "working", Path: "later-name.txt", OldPath: "tracked.txt",
		})
		if err != nil || laterDiff.Original.Content != "protected before rename\n" || laterDiff.Modified.Content != "later renamed version\n" || !laterDiff.Editable {
			t.Fatalf("later rename diff = %#v, %v", laterDiff, err)
		}

		integration.action(t, sourcecontrol.ActionRequest{Action: "commit_protected", Message: "protected before later rename"})
		checkedIn := runFossilIntegration(t, integration.binary, integration.root, "finfo", "-p", "-r", "current", "./tracked.txt")
		if checkedIn != "protected before rename\n" {
			t.Fatalf("protected content before later rename = %q", checkedIn)
		}
		if _, err := os.Stat(filepath.Join(integration.root, "tracked.txt")); !os.IsNotExist(err) {
			t.Fatalf("later rename source was restored unexpectedly: %v", err)
		}
		if content, err := os.ReadFile(filepath.Join(integration.root, "later-name.txt")); err != nil || string(content) != "later renamed version\n" {
			t.Fatalf("later rename destination was not restored: %q, %v", content, err)
		}
		assertFossilIntegrationChange(t, integration.status(t), "working", "later-name.txt", "renamed")
	})
}

func TestFossilIntegrationStaleProtectionFailsClosed(t *testing.T) {
	integration := newFossilIntegration(t)
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "protected A\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"tracked.txt"}})
	runFossilIntegration(t, integration.binary, integration.root, "commit", "--nosync", "--no-prompt", "--no-warnings", "-m", "external baseline change", "./tracked.txt")

	status := integration.status(t)
	var protectedGroup *sourcecontrol.ChangeGroup
	for index := range status.Groups {
		if status.Groups[index].ID == protectedGroupID {
			protectedGroup = &status.Groups[index]
			break
		}
	}
	if protectedGroup == nil || protectedGroup.Diagnostic == "" || len(protectedGroup.Actions) != 1 || protectedGroup.Actions[0] != "unprotect" {
		t.Fatalf("stale protection was not exposed fail-closed: %#v", protectedGroup)
	}
	_, err := integration.provider.Action(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.ActionRequest{
		RequestID: "stale-protection", Action: "discard", ExpectedRevision: status.Revision, Paths: []string{"tracked.txt"}, Confirmed: true,
	})
	var sourceErr *sourcecontrol.Error
	if !errors.As(err, &sourceErr) || sourceErr.Code != "protected_changes_stale" {
		t.Fatalf("stale protection mutation error = %v", err)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "unprotect_all", Confirmed: true})
}

func TestFossilIntegrationProtectedCommitRecovery(t *testing.T) {
	for _, test := range []struct {
		stage     string
		committed bool
	}{
		{stage: "before_materialization"},
		{stage: "before_commit"},
		{stage: "after_commit", committed: true},
		{stage: "before_restoration", committed: true},
	} {
		t.Run(test.stage, func(t *testing.T) {
			integration := newFossilIntegration(t)
			writeFossilIntegrationFile(t, integration.root, "tracked.txt", "protected A\n")
			integration.action(t, sourcecontrol.ActionRequest{Action: "protect", Paths: []string{"tracked.txt"}})
			writeFossilIntegrationFile(t, integration.root, "tracked.txt", "later B\n")
			before := integration.status(t)
			integration.provider.protectedCommitFault = func(stage string) error {
				if stage == test.stage {
					return errors.New("injected protected commit interruption")
				}
				return nil
			}
			_, err := integration.provider.Action(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.ActionRequest{
				RequestID: "fault-" + test.stage, Action: "commit_protected", ExpectedRevision: before.Revision, Message: "protected recovery",
			})
			var sourceErr *sourcecontrol.Error
			if !errors.As(err, &sourceErr) || sourceErr.Code != "protected_changes_recovery_required" {
				t.Fatalf("fault result = %v", err)
			}
			integration.provider.protectedCommitFault = nil

			// Status is the next provider access and must finish the durable
			// recovery before exposing any temporary materialization.
			status := integration.status(t)
			if content, readErr := os.ReadFile(filepath.Join(integration.root, "tracked.txt")); readErr != nil || string(content) != "later B\n" {
				t.Fatalf("recovery did not restore later B: %q, %v", content, readErr)
			}
			checkedIn := runFossilIntegration(t, integration.binary, integration.root, "finfo", "-p", "-r", "current", "./tracked.txt")
			if test.committed {
				if checkedIn != "protected A\n" || hasFossilIntegrationChange(status, protectedGroupID, "tracked.txt") {
					t.Fatalf("post-commit recovery state: checkedIn=%q groups=%#v", checkedIn, status.Groups)
				}
				assertFossilIntegrationChange(t, status, "working", "tracked.txt", "modified")
			} else {
				if checkedIn != "before\n" {
					t.Fatalf("pre-commit recovery unexpectedly committed: %q", checkedIn)
				}
				assertFossilIntegrationChange(t, status, protectedGroupID, "tracked.txt", "modified")
				assertFossilIntegrationChange(t, status, "working", "tracked.txt", "modified")
			}
		})
	}
}

func (integration *fossilIntegration) status(t *testing.T) sourcecontrol.StatusSnapshot {
	t.Helper()
	status, err := integration.provider.Status(context.Background(), integration.workspaceID, integration.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (integration *fossilIntegration) action(t *testing.T, request sourcecontrol.ActionRequest) sourcecontrol.ActionResult {
	t.Helper()
	integration.request++
	request.RequestID = fmt.Sprintf("integration-%d", integration.request)
	request.ExpectedRevision = integration.status(t).Revision
	result, err := integration.provider.Action(context.Background(), integration.workspaceID, integration.repositoryID, request)
	if err != nil {
		t.Fatalf("Fossil action %s failed: %v", request.Action, err)
	}
	return result
}

func (integration *fossilIntegration) providerState(t *testing.T) *repositoryState {
	t.Helper()
	state, err := integration.provider.repository(context.Background(), integration.workspaceID, integration.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestFossilIntegrationDailyWorkflow(t *testing.T) {
	integration := newFossilIntegration(t)
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "edited\n")
	writeFossilIntegrationFile(t, integration.root, "scheduled.txt", "scheduled\n")
	writeFossilIntegrationFile(t, integration.root, "extra.txt", "extra\n")

	status := integration.status(t)
	assertFossilIntegrationChange(t, status, "working", "tracked.txt", "modified")
	assertFossilIntegrationChange(t, status, "untracked", "scheduled.txt", "untracked")
	_, invalidUntrackErr := integration.provider.Action(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.ActionRequest{
		RequestID: "invalid-untrack", Action: "untrack", ExpectedRevision: status.Revision, Paths: []string{"tracked.txt"},
	})
	var invalidUntrack *sourcecontrol.Error
	if !errors.As(invalidUntrackErr, &invalidUntrack) || invalidUntrack.Code != "invalid_fossil_path_state" {
		t.Fatalf("untracking a normal tracked file should fail safely: %v", invalidUntrackErr)
	}
	assertFossilIntegrationChange(t, integration.status(t), "working", "tracked.txt", "modified")
	diff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{Kind: "change", GroupID: "working", Path: "tracked.txt"})
	if err != nil || diff.Original.Content != "before\n" || diff.Modified.Content != "edited\n" || !diff.Editable {
		t.Fatalf("unexpected working diff: %#v %v", diff, err)
	}

	integration.action(t, sourcecontrol.ActionRequest{Action: "track", Paths: []string{"scheduled.txt"}})
	assertFossilIntegrationChange(t, integration.status(t), "working", "scheduled.txt", "added")
	addedDiff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{Kind: "change", GroupID: "working", Path: "scheduled.txt"})
	if err != nil || addedDiff.Original.Exists || addedDiff.Modified.Content != "scheduled\n" {
		t.Fatalf("unexpected added-file diff: %#v %v", addedDiff, err)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "untrack", Paths: []string{"scheduled.txt"}})
	assertFossilIntegrationChange(t, integration.status(t), "untracked", "scheduled.txt", "untracked")
	trashed := integration.action(t, sourcecontrol.ActionRequest{Action: "discard", Paths: []string{"scheduled.txt"}, Confirmed: true})
	if len(trashed.TrashIDs) != 1 {
		t.Fatalf("untracked discard did not use Echo Trash: %#v", trashed)
	}
	if _, err := os.Stat(filepath.Join(integration.root, "scheduled.txt")); !os.IsNotExist(err) {
		t.Fatalf("discarded untracked file remains: %v", err)
	}

	integration.action(t, sourcecontrol.ActionRequest{Action: "track", Paths: []string{"extra.txt"}})
	integration.action(t, sourcecontrol.ActionRequest{Action: "commit_selected", Paths: []string{"extra.txt"}, Message: "selected file"})
	status = integration.status(t)
	assertFossilIntegrationChange(t, status, "working", "tracked.txt", "modified")
	if hasFossilIntegrationChange(status, "working", "extra.txt") {
		t.Fatalf("selected commit left the selected file changed: %#v", status.Groups)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "commit_all", Message: "remaining edit"})
	if status = integration.status(t); status.TotalChangeCount != 0 {
		t.Fatalf("commit all did not clean the checkout: %#v", status.Groups)
	}

	if err := os.Remove(filepath.Join(integration.root, "delete-me.txt")); err != nil {
		t.Fatal(err)
	}
	assertFossilIntegrationChange(t, integration.status(t), "working", "delete-me.txt", "deleted")
	integration.action(t, sourcecontrol.ActionRequest{Action: "commit_all", Message: "remove tracked file"})
	if status = integration.status(t); status.TotalChangeCount != 0 {
		t.Fatalf("deleted-file commit did not clean the checkout: %#v", status.Groups)
	}

	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "stash edit\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "stash", Message: "integration stash"})
	if content, err := os.ReadFile(filepath.Join(integration.root, "tracked.txt")); err != nil || string(content) != "edited\n" {
		t.Fatalf("stash did not restore baseline: %q %v", content, err)
	}
	metadata, err := integration.provider.Metadata(context.Background(), integration.workspaceID, integration.repositoryID)
	if err != nil || len(metadata.Stashes) != 1 {
		t.Fatalf("stash metadata: %#v %v", metadata, err)
	}
	stashID := metadata.Stashes[0].Ref
	detail, err := integration.provider.RevisionDetail(context.Background(), integration.workspaceID, integration.repositoryID, stashID, "stash")
	if err != nil || len(detail.Files) == 0 {
		t.Fatalf("stash detail: %#v %v", detail, err)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "pop_stash", Ref: stashID})
	if content, err := os.ReadFile(filepath.Join(integration.root, "tracked.txt")); err != nil || string(content) != "stash edit\n" {
		t.Fatalf("stash pop did not restore changes: %q %v", content, err)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "discard", Paths: []string{"tracked.txt"}, Confirmed: true})

	history, err := integration.provider.History(context.Background(), integration.workspaceID, integration.repositoryID, 0, 20)
	if err != nil || len(history.Commits) < 3 {
		t.Fatalf("history: %#v %v", history, err)
	}
	baseRevision, editedRevision := "", ""
	for _, commit := range history.Commits {
		switch commit.Subject {
		case "selected file":
			baseRevision = commit.Hash
		case "remaining edit":
			editedRevision = commit.Hash
		}
	}
	revisionDiff, err := integration.provider.Diff(context.Background(), integration.workspaceID, integration.repositoryID, sourcecontrol.DiffTarget{
		Kind: "revisions", Path: "tracked.txt", BaseRef: baseRevision, Ref: editedRevision,
	})
	if err != nil || revisionDiff.Original.Content != "before\n" || revisionDiff.Modified.Content != "edited\n" || revisionDiff.Editable {
		t.Fatalf("revision diff: %#v %v", revisionDiff, err)
	}
	detail, err = integration.provider.RevisionDetail(context.Background(), integration.workspaceID, integration.repositoryID, history.Commits[0].Hash, "commit")
	if err != nil || len(detail.Files) == 0 {
		t.Fatalf("revision detail: %#v %v", detail, err)
	}
	annotation, err := integration.provider.Annotate(context.Background(), integration.workspaceID, integration.repositoryID, "tracked.txt", "current", 1, 1)
	if err != nil || strings.TrimSpace(annotation.Text) == "" {
		t.Fatalf("annotation: %#v %v", annotation, err)
	}

	integration.action(t, sourcecontrol.ActionRequest{Action: "create_branch", Name: "feature"})
	if branch := integration.status(t).Branch; branch != "feature" {
		t.Fatalf("branch creation did not switch checkout: %q", branch)
	}
	integration.action(t, sourcecontrol.ActionRequest{Action: "checkout", Ref: "trunk"})

	upstream := filepath.Join(filepath.Dir(integration.root), "upstream.fossil")
	runFossilIntegration(t, integration.binary, filepath.Dir(integration.root), "clone", fossilIntegrationFileURL(integration.repository), upstream, "--admin-user", "echo-test")
	runFossilIntegration(t, integration.binary, integration.root, "remote", fossilIntegrationFileURL(upstream))
	integration.action(t, sourcecontrol.ActionRequest{Action: "push"})
	integration.action(t, sourcecontrol.ActionRequest{Action: "pull"})
	integration.action(t, sourcecontrol.ActionRequest{Action: "sync"})
}

func TestFossilIntegrationReportsMergeConflicts(t *testing.T) {
	integration := newFossilIntegration(t)
	integration.action(t, sourcecontrol.ActionRequest{Action: "create_branch", Name: "feature"})
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "feature\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "commit_all", Message: "feature edit"})
	integration.action(t, sourcecontrol.ActionRequest{Action: "checkout", Ref: "trunk"})
	writeFossilIntegrationFile(t, integration.root, "tracked.txt", "trunk\n")
	integration.action(t, sourcecontrol.ActionRequest{Action: "commit_all", Message: "trunk edit"})
	integration.action(t, sourcecontrol.ActionRequest{Action: "merge", Ref: "feature"})
	status := integration.status(t)
	if !hasFossilIntegrationChange(status, "conflicts", "tracked.txt") || !status.State.MergeInProgress {
		t.Fatalf("merge conflict was not classified: %#v", status)
	}
}

func assertFossilIntegrationChange(t *testing.T, status sourcecontrol.StatusSnapshot, groupID, pathValue, kind string) {
	t.Helper()
	for _, group := range status.Groups {
		if group.ID != groupID {
			continue
		}
		for _, change := range group.Changes {
			if change.Path == pathValue && change.Kind == kind {
				return
			}
		}
	}
	t.Fatalf("missing %s %s change %q: %#v", groupID, kind, pathValue, status.Groups)
}

func hasFossilIntegrationChange(status sourcecontrol.StatusSnapshot, groupID, pathValue string) bool {
	for _, group := range status.Groups {
		if group.ID != groupID {
			continue
		}
		for _, change := range group.Changes {
			if change.Path == pathValue {
				return true
			}
		}
	}
	return false
}

func runFossilIntegration(t *testing.T, binary, directory string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), localCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "NO_COLOR=1", "FOSSIL_EDITOR=true", "VISUAL=true", "EDITOR=true")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fossil %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeFossilIntegrationFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fossilIntegrationFileURL(pathValue string) string {
	if runtime.GOOS == "windows" {
		// Fossil's Windows filesystem transport treats the drive as the URI
		// host (file://C:/path). RFC file:///C:/ URLs are drive-relative there.
		clean := filepath.Clean(pathValue)
		volume := filepath.VolumeName(clean)
		rest := strings.TrimLeft(strings.TrimPrefix(clean, volume), `/\`)
		return (&url.URL{Scheme: "file", Host: volume, Path: "/" + filepath.ToSlash(rest)}).String()
	}
	slash := filepath.ToSlash(pathValue)
	return (&url.URL{Scheme: "file", Path: slash}).String()
}
