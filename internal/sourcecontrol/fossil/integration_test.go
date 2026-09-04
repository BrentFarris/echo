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
	provider := New(manager, fs, nil)
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
