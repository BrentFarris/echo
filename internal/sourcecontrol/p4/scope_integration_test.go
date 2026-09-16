package p4

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestP4IntegrationOfflineRestart(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.status(t)
	f.stopServer()
	p := New(f.fs, nil, f.p.dataDir)
	p.env = f.p.env
	p.timeout = 300 * time.Millisecond
	t.Cleanup(p.Close)
	f.fs.SetMutationCoordinator(p)
	before, err := f.fs.Read(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Tracks(f.workspace, filepath.Join(f.root.HostPath, "tracked.txt")) {
		t.Fatal("offline restart lost checkout enforcement")
	}
	_, err = f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: before.Ref, ExpectedRevision: before.Revision, Content: "must not replace\n"})
	if err == nil {
		t.Fatal("offline tracked save bypassed checkout")
	}
	if data, err := os.ReadFile(filepath.Join(f.root.HostPath, "tracked.txt")); err != nil || string(data) != "base\n" {
		t.Fatalf("offline tracked content: %q %v", data, err)
	}
	added, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: workspacefs.FileRef{RootID: f.root.ID, Path: "offline-new.txt"}, Content: "keep this work\n", CreateOnly: true})
	if err != nil || added.Registration == nil {
		t.Fatalf("offline creation should queue after writing: %+v %v", added, err)
	}
	snapshot, err := p.Status(context.Background(), f.workspace, f.r.id)
	if err != nil || !snapshot.Stale || snapshot.Groups[0].ID != "default" {
		t.Fatalf("offline status: %+v %v", snapshot, err)
	}
	local := snapshot.Groups[len(snapshot.Groups)-1]
	if len(local.Changes) != 1 || local.Changes[0].Path != "offline-new.txt" || local.Changes[0].Diagnostic == "" {
		t.Fatalf("offline journal not visible: %+v", local)
	}
}

func TestP4IntegrationExcludedDestinationAndHiddenChangelist(t *testing.T) {
	f := newFixture(t)
	c := f.connection
	c.CommandCharset = "utf8"
	form, err := f.p.command(context.Background(), c, nil, false, "client", "-o", c.Client)
	if err != nil {
		t.Fatal(err)
	}
	view := "//depot/... //echo-test/...\n-//depot/excluded/... //echo-test/excluded/..."
	form = []byte(replaceField(string(form), "View", view))
	if _, err = f.p.command(context.Background(), c, form, false, "client", "-i"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(f.root.HostPath, "excluded"), 0755); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	source := workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"}
	if _, err = f.fs.Move(f.workspace, source, workspacefs.FileRef{RootID: f.root.ID, Path: "excluded"}); err == nil {
		t.Fatal("unmapped tracked destination accepted")
	}
	if _, err = os.Stat(filepath.Join(f.root.HostPath, "tracked.txt")); err != nil {
		t.Fatal("failed preflight physically moved source")
	}
	f.save(t, "excluded/local.txt", "excluded new file\n")
	if len(f.p.pending(f.r)) != 0 {
		t.Fatal("excluded addition should remain outside P4")
	}

	// Inspect one subtree of a larger P4 client. Other opened client paths must
	// count as hidden and prevent a whole-list destructive operation.
	f.native(t, []string{"edit"}, "tracked.txt")
	workspace, err := f.manager.Create(workspaces.CreateRequest{Name: "Narrow P4 scope", MainPath: filepath.Join(f.root.HostPath, "visible")})
	if err != nil {
		if mkdirErr := os.Mkdir(filepath.Join(f.root.HostPath, "visible"), 0755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		workspace, err = f.manager.Create(workspaces.CreateRequest{Name: "Narrow P4 scope", MainPath: filepath.Join(f.root.HostPath, "visible")})
	}
	if err != nil {
		t.Fatal(err)
	}
	roots, err := f.fs.Roots(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := f.p.Settings(workspace.ID)
	settings.Roots[roots[0].ID] = Override{Server: c.Server, User: c.User, Client: c.Client}
	if err = f.p.Configure(workspace.ID, settings); err != nil {
		t.Fatal(err)
	}
	repos, err := f.p.Repositories(context.Background(), workspace.ID)
	if err != nil || len(repos) != 1 {
		t.Fatalf("narrow discovery: %+v %v", repos, err)
	}
	status, err := f.p.Status(context.Background(), workspace.ID, repos[0].ID)
	if err != nil || status.HiddenChangeCount != 1 || status.Groups[0].HiddenChangeCount != 1 {
		t.Fatalf("hidden membership: %+v %v", status, err)
	}
	if _, err = f.p.Action(context.Background(), workspace.ID, repos[0].ID, sourcecontrol.ActionRequest{Action: "revert", GroupID: "default", Confirmed: true}); err == nil {
		t.Fatal("whole changelist reverted hidden path")
	}
}

func TestP4IntegrationReconcileReservedAndIgnoredPaths(t *testing.T) {
	f := newFixture(t)
	f.p.env = append(f.p.env, "P4IGNORE=.p4ignore")
	if err := os.WriteFile(filepath.Join(f.root.HostPath, ".p4ignore"), []byte("ignored*\nä.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"offline%#@ü.txt", "ignored-new.txt"} {
		if err := os.WriteFile(filepath.Join(f.root.HostPath, name), []byte("external\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	preview := f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview", Paths: []string{"offline%#@ü.txt", "ignored-new.txt"}}).Preview
	if preview == nil || len(preview.Changes) != 1 || preview.Changes[0].Path != "offline%#@ü.txt" {
		t.Fatalf("exact ignored/reserved preview: %+v", preview)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_apply", PreviewToken: preview.Token, Confirmed: true})
	status := f.status(t)
	if len(status.Groups[0].Changes) != 1 || status.Groups[0].Changes[0].Path != "offline%#@ü.txt" {
		t.Fatalf("reserved path not registered: %+v", status)
	}
	f.p.commandHook = func(args []string) error {
		if args[0] == "reconcile" {
			t.Fatal("empty selection executed reconcile")
		}
		return nil
	}
	empty := f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview"}).Preview
	if empty == nil || empty.Token != "" {
		t.Fatalf("empty selection: %+v", empty)
	}
	f.p.commandHook = nil
	// A tracked file deliberately matching ignore rules must remain detectable.
	f.native(t, []string{"add", "-I"}, "ignored-new.txt")
	if _, err := f.p.run(context.Background(), f.connection, "submit", "-d", "ignore fixture"); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.save(t, "ä.txt", "ignored Unicode addition\n")
	meta, err := f.p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, "ä.txt"))
	if err != nil || meta["action"] != "" {
		t.Fatalf("Unicode ignore was bypassed: %+v %v", meta, err)
	}
	f.p.HandleFileEvent(workspacefs.WatchEvent{WorkspaceID: f.workspace, Changes: []workspacefs.Change{{Op: "write", Ref: workspacefs.FileRef{RootID: f.root.ID, Path: "ä.txt"}}}})
	if _, ok := f.r.candidates[filepath.Join(f.root.HostPath, "ä.txt")]; ok {
		t.Fatal("ignored Unicode addition became an external candidate")
	}
	file := filepath.Join(f.root.HostPath, "ignored-new.txt")
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f.p.HandleFileEvent(workspacefs.WatchEvent{WorkspaceID: f.workspace, Changes: []workspacefs.Change{{Op: "write", Ref: workspacefs.FileRef{RootID: f.root.ID, Path: "ignored-new.txt"}}}})
	if _, ok := f.r.candidates[file]; !ok {
		t.Fatal("ignore rule hid a tracked external edit")
	}
	f.p.HandleFileEvent(workspacefs.WatchEvent{WorkspaceID: f.workspace, ResyncRequired: true})
	if !f.status(t).DetectionIncomplete {
		t.Fatal("watcher gap reported complete detection")
	}
}

func TestP4IntegrationNativeReconcileMoveScope(t *testing.T) {
	f := newFixture(t)
	source := filepath.Join(f.root.HostPath, "tracked.txt")
	destination := filepath.Join(f.root.HostPath, "external-move.txt")
	if err := os.Rename(source, destination); err != nil {
		t.Fatal(err)
	}
	preview := f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview", Paths: []string{"tracked.txt", "external-move.txt"}}).Preview
	if preview == nil || len(preview.Changes) != 2 {
		t.Fatalf("move preview: %+v", preview)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_apply", PreviewToken: preview.Token, Confirmed: true})
	// Native servers may report a move pair or independent add/delete actions.
	// In either case only the reviewed pair is allowed to be opened.
	for _, change := range f.status(t).Groups[0].Changes {
		if change.Path != "tracked.txt" && change.Path != "external-move.txt" {
			t.Fatalf("reconciled outside selected pair: %+v", change)
		}
	}
	rows, err := f.p.run(context.Background(), f.connection, "opened", "-C", f.connection.Client, "-u", f.connection.User)
	if err != nil || len(rows) != 2 {
		t.Fatalf("native scoped move: %+v %v", rows, err)
	}
	for _, row := range rows {
		if !strings.HasSuffix(row["depotFile"], "tracked.txt") && !strings.HasSuffix(row["depotFile"], "external-move.txt") {
			t.Fatal(row)
		}
	}
}
