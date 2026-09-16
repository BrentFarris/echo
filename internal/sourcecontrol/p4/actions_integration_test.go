package p4

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/mutation"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestP4IntegrationMoveReassignmentAndScopedUnchangedRevert(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.save(t, "tracked.txt", "renamed edit\n")
	moved, err := f.fs.Rename(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"}, "renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	list := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "Move assignment"}).GroupID
	f.action(t, sourcecontrol.ActionRequest{Action: "reopen", GroupID: "default", TargetGroupID: list, Paths: []string{moved.Ref.Path}})
	for _, path := range []string{"tracked.txt", "renamed.txt"} {
		meta, err := f.p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, path))
		if err != nil || meta["change"] != list {
			t.Fatalf("move pair assignment for %s: %+v %v", path, meta, err)
		}
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "checkout", Paths: []string{"delete.txt"}})
	f.action(t, sourcecontrol.ActionRequest{Action: "revert_unchanged", GroupID: list, Confirmed: true})
	meta, err := f.p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, "delete.txt"))
	if err != nil || meta["action"] != "" {
		t.Fatalf("unchanged checkout remains: %+v %v", meta, err)
	}
	status := f.status(t)
	if len(status.Groups[1].Changes) != 1 || status.Groups[1].Changes[0].Path != moved.Ref.Path {
		t.Fatalf("changed move was reverted: %+v", status)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "revert", GroupID: list, Confirmed: true})
	if file, err := f.fs.Read(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"}); err != nil || file.Content != "base\n" {
		t.Fatalf("whole-list revert: %+v %v", file, err)
	}
}

func TestP4IntegrationSandboxNeverInvokesHostP4(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	config := workspaces.DefaultSandboxConfig()
	config.Enabled = true
	if _, err := f.manager.SetSandboxConfig(f.workspace, config); err != nil {
		t.Fatal(err)
	}
	sandboxManager := sandbox.NewManager(f.manager, filepath.Join(t.TempDir(), "sandbox"), "test", nil)
	f.p.sandbox = sandboxManager
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sandboxManager.Shutdown(ctx)
	})
	f.p.commandHook = func([]string) error { t.Fatal("sandbox invoked host P4"); return nil }
	if descriptor := f.p.Descriptor(context.Background(), f.workspace); descriptor.Available {
		t.Fatalf("available in sandbox: %+v", descriptor)
	}
	repositories, err := f.p.Repositories(context.Background(), f.workspace)
	if err != nil || len(repositories) != 1 || repositories[0].Available {
		t.Fatalf("sandbox repository: %+v %v", repositories, err)
	}
	if _, err := f.p.Status(context.Background(), f.workspace, f.r.id); err == nil {
		t.Fatal("sandbox status succeeded")
	}
	if _, err := f.p.Diff(context.Background(), f.workspace, f.r.id, sourcecontrol.DiffTarget{Path: "tracked.txt"}); err == nil {
		t.Fatal("sandbox diff succeeded")
	}
	if _, err := f.p.Action(context.Background(), f.workspace, f.r.id, sourcecontrol.ActionRequest{Action: "checkout", Paths: []string{"tracked.txt"}}); err == nil {
		t.Fatal("sandbox action succeeded")
	}
	path := filepath.Join(f.root.HostPath, "tracked.txt")
	if f.p.Tracks(f.workspace, path) {
		t.Fatal("sandbox host tracking enabled")
	}
	f.p.HandleFileEvent(workspacefs.WatchEvent{WorkspaceID: f.workspace, Changes: []workspacefs.Change{{Ref: workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"}}}})
	_, err = f.p.Run(context.Background(), mutation.Operation{WorkspaceID: f.workspace, Kind: "edit", Path: path}, func() error { return errors.New("filesystem callback") })
	if err == nil || err.Error() != "filesystem callback" {
		t.Fatalf("sandbox filesystem delegation: %v", err)
	}
}

func TestP4IntegrationSharedClientDoesNotOverwriteAnotherUsersOpenFile(t *testing.T) {
	f := newFixture(t)
	other := f.connection
	other.User = "second-user"
	path := filepath.Join(f.root.HostPath, "tracked.txt")
	if _, err := f.p.files(context.Background(), other, []string{"edit"}, []string{path}); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	before, err := f.fs.Read(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: before.Ref, Content: "must not overwrite\n", ExpectedRevision: before.Revision})
	if err == nil || !strings.Contains(err.Error(), "another P4 user") {
		t.Fatalf("another user's pending file was writable: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != before.Content {
		t.Fatalf("local file changed: %q %v", data, err)
	}
}

func TestP4IntegrationNonUnicodeServer(t *testing.T) {
	f := newFixtureWithUnicode(t, false)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	list := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "Non-Unicode server"}).GroupID
	f.save(t, "tracked.txt", "changed\n")
	f.save(t, "new%#@.txt", "added\n")
	diff, err := f.p.Diff(context.Background(), f.workspace, f.r.id, sourcecontrol.DiffTarget{Kind: "change", Path: "tracked.txt", GroupID: list})
	if err != nil || diff.Original.Content != "base\n" || diff.Modified.Content != "changed\n" {
		t.Fatalf("non-Unicode diff: %+v %v", diff, err)
	}
	status := f.status(t)
	if len(status.Groups[1].Changes) != 2 {
		t.Fatalf("non-Unicode changelist: %+v", status)
	}
}

func TestP4IntegrationMetadataCharsetIsSeparateFromFileCharset(t *testing.T) {
	f := newFixture(t)
	f.p.env = append(f.p.env, "P4CHARSET=iso8859-1")
	if err := f.p.Configure(f.workspace, f.p.Settings(f.workspace)); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	list := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "Metadata stays UTF-8: é"}).GroupID
	f.save(t, "new é.txt", "ASCII file content\n")
	status := f.status(t)
	if len(status.Groups[1].Changes) != 1 || status.Groups[1].Changes[0].Path != "new é.txt" || !strings.Contains(status.Groups[1].Description, "é") {
		t.Fatalf("metadata charset: %+v", status)
	}
	diff, err := f.p.Diff(context.Background(), f.workspace, f.r.id, sourcecontrol.DiffTarget{Kind: "change", Path: "new é.txt", GroupID: list})
	if err != nil || diff.Modified.Content != "ASCII file content\n" {
		t.Fatalf("diff charset: %+v %v", diff, err)
	}
}
