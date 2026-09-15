package p4

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
)

func (f *fixture) save(t *testing.T, path, content string) workspacefs.FileSnapshot {
	t.Helper()
	ref := workspacefs.FileRef{RootID: f.root.ID, Path: path}
	before, err := f.fs.Read(f.workspace, ref)
	request := workspacefs.SaveRequest{Ref: ref, Content: content, ExpectedRevision: before.Revision, CreateOnly: err != nil}
	after, err := f.fs.Save(f.workspace, request)
	if err != nil || after.Registration != nil {
		t.Fatalf("save %s: %+v %v", path, after.Registration, err)
	}
	return after
}

func (f *fixture) native(t *testing.T, args []string, paths ...string) []record {
	t.Helper()
	for i := range paths {
		paths[i] = filepath.Join(f.root.HostPath, paths[i])
	}
	rows, err := f.p.files(context.Background(), f.connection, args, paths)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestP4IntegrationRestoreExactTrashAfterDisablement(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	list := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "Original assignment"}).GroupID
	edited := f.save(t, "tracked.txt", "first local edit\n")
	first, err := f.fs.Trash(f.workspace, edited.Ref)
	if err != nil || first.Registration != nil {
		t.Fatalf("trash: %+v %v", first, err)
	}
	// Source-control dispatch must still handle an explicit restore when normal
	// tracking is off, including a new provider process with only the journal.
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: false})
	p := New(f.fs, nil, f.p.dataDir)
	p.env = f.p.env
	t.Cleanup(p.Close)
	service := sourcecontrol.New()
	if err := service.Register(p); err != nil {
		t.Fatal(err)
	}
	f.fs.SetMutationCoordinator(service)
	restored, err := f.fs.Restore(f.workspace, first.ID)
	if err != nil || restored.Registration != nil {
		t.Fatalf("restore disabled: %+v %v", restored, err)
	}
	meta, err := p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, "tracked.txt"))
	if err != nil || meta["action"] != "edit" || meta["change"] != list {
		t.Fatalf("prior edit assignment: %+v %v", meta, err)
	}
	// A second deletion of the same filename must use its own original state.
	f.p = p
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.native(t, []string{"reopen", "-c", "default"}, "tracked.txt")
	second, err := f.fs.Trash(f.workspace, edited.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.fs.Restore(f.workspace, second.ID); err != nil {
		t.Fatal(err)
	}
	meta, err = p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, "tracked.txt"))
	if err != nil || groupID(meta["change"]) != "default" {
		t.Fatalf("wrong Trash identity: %+v %v", meta, err)
	}
	added := f.save(t, "added.txt", "unsubmitted add\n")
	item, err := f.fs.Trash(f.workspace, added.Ref)
	if err != nil || item.Registration != nil {
		t.Fatalf("trash add: %+v %v", item, err)
	}
	if _, err = f.fs.Restore(f.workspace, item.ID); err != nil {
		t.Fatal(err)
	}
	meta, err = p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, "added.txt"))
	if err != nil || meta["action"] != "add" {
		t.Fatalf("restore add: %+v %v", meta, err)
	}
}

func TestP4IntegrationDirectoryMoveAndTrash(t *testing.T) {
	f := newFixture(t)
	sub := filepath.Join(f.root.HostPath, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b ü.txt"} {
		if err := os.WriteFile(filepath.Join(sub, name), []byte("base\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	f.native(t, []string{"add"}, "sub/a.txt", "sub/b ü.txt")
	if _, err := f.p.run(context.Background(), f.connection, "submit", "-d", "directory fixture"); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.save(t, "sub/a.txt", "modified\n")
	f.save(t, "sub/new.txt", "new\n")
	moved, err := f.fs.Rename(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "sub"}, "moved")
	if err != nil || moved.Registration != nil {
		t.Fatalf("directory move: %+v %v", moved, err)
	}
	status := f.status(t)
	if len(status.Groups[0].Changes) != 3 {
		t.Fatalf("directory intents: %+v", status)
	}
	for _, change := range status.Groups[0].Changes {
		if !strings.HasPrefix(change.Path, "moved/") {
			t.Fatalf("unmoved child: %+v", change)
		}
	}
	// Revert the native tracked pairs, leaving the moved new add as an add.
	f.action(t, sourcecontrol.ActionRequest{Action: "revert", Paths: []string{"moved/a.txt", "moved/b ü.txt"}, Confirmed: true})
	trash, err := f.fs.Trash(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "sub"})
	if err != nil || trash.Registration != nil {
		t.Fatalf("directory trash: %+v %v", trash, err)
	}
	if _, err = f.fs.Restore(f.workspace, trash.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b ü.txt"} {
		if _, err = os.Stat(filepath.Join(sub, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestP4IntegrationRecoveryDoesNotOverrideP4V(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.p.commandHook = func(args []string) error {
		if args[0] == "add" {
			return errors.New("registration failed")
		}
		return nil
	}
	ref := workspacefs.FileRef{RootID: f.root.ID, Path: "queued.txt"}
	file, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: ref, Content: "kept\n", CreateOnly: true})
	if err != nil || file.Registration == nil {
		t.Fatalf("expected successful queued write: %+v %v", file, err)
	}
	f.p.commandHook = nil
	other := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "Changed in P4V"}).GroupID
	f.native(t, []string{"add", "-c", other}, ref.Path)
	f.action(t, sourcecontrol.ActionRequest{Action: "retry_registration", Paths: []string{ref.Path}})
	meta, err := f.p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, ref.Path))
	if err != nil || meta["change"] != other {
		t.Fatalf("retry overrode P4V: %+v %v", meta, err)
	}
	// A stale preview does not create an ambiguous mutation journal.
	if err = os.WriteFile(filepath.Join(f.root.HostPath, "external.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	preview := f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview", Paths: []string{"external.txt"}}).Preview
	if err = os.WriteFile(filepath.Join(f.root.HostPath, "external.txt"), []byte("two\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.Action(context.Background(), f.workspace, f.r.id, sourcecontrol.ActionRequest{Action: "reconcile_apply", Confirmed: true, PreviewToken: preview.Token}); err == nil {
		t.Fatal("stale preview accepted")
	}
	if records := f.p.pending(f.r); len(records) != 0 {
		t.Fatalf("rejected preview left recovery work: %+v", records)
	}
}

func TestP4IntegrationRecoveryRejectsChangedClientView(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.p.commandHook = func(args []string) error {
		if args[0] == "add" {
			return errors.New("interrupted registration")
		}
		return nil
	}
	ref := workspacefs.FileRef{RootID: f.root.ID, Path: "queued.txt"}
	file, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: ref, Content: "keep local work\n", CreateOnly: true})
	if err != nil || file.Registration == nil {
		t.Fatalf("queued create: %+v %v", file, err)
	}
	f.p.commandHook = nil
	form, err := f.p.command(context.Background(), f.connection, nil, false, "client", "-o", f.connection.Client)
	if err != nil {
		t.Fatal(err)
	}
	oldPreview := f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview", Paths: []string{ref.Path}}).Preview
	changed := replaceField(string(form), "View", "//depot/redirected/... //echo-test/...")
	if _, err = f.p.command(context.Background(), f.connection, []byte(changed), false, "client", "-i"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.Action(context.Background(), f.workspace, f.r.id, sourcecontrol.ActionRequest{Action: "reconcile_apply", Confirmed: true, PreviewToken: oldPreview.Token}); err == nil {
		t.Fatal("preview survived a change to the client view")
	}
	_, err = f.p.Action(context.Background(), f.workspace, f.r.id, sourcecontrol.ActionRequest{Action: "retry_registration", Paths: []string{ref.Path}})
	if err == nil || !strings.Contains(err.Error(), "mapping changed") {
		t.Fatalf("redirected retry: %v", err)
	}
	meta, err := f.p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, ref.Path))
	if err != nil || meta["action"] != "" || len(f.p.pending(f.r)) == 0 {
		t.Fatalf("retry mutated redirected depot: %+v %v", meta, err)
	}
	// An explicit review is allowed to register the file under the current view.
	preview := f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview", Paths: []string{ref.Path}}).Preview
	f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_apply", Confirmed: true, PreviewToken: preview.Token})
	meta, err = f.p.stat(context.Background(), f.r, filepath.Join(f.root.HostPath, ref.Path))
	if err != nil || meta["depotFile"] != "//depot/redirected/queued.txt" || meta["action"] != "add" || len(f.p.pending(f.r)) != 0 {
		t.Fatalf("reviewed mapping: %+v %v", meta, err)
	}
}

func TestP4IntegrationTrashRecoveryRequiresIntactState(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	file := f.save(t, "tracked.txt", "work to preserve\n")
	f.p.commandHook = func(args []string) error {
		if args[0] == "revert" {
			return errors.New("delete registration failed")
		}
		return nil
	}
	item, err := f.fs.Trash(f.workspace, file.Ref)
	if err != nil || item.Registration == nil {
		t.Fatalf("partial Trash: %+v %v", item, err)
	}
	f.p.commandHook = nil
	other := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "P4V reassignment"}).GroupID
	f.native(t, []string{"reopen", "-c", other}, "tracked.txt")
	if _, err = f.fs.Restore(f.workspace, item.ID); err == nil || !strings.Contains(err.Error(), "another changelist") {
		t.Fatalf("restore overrode reassignment: %v", err)
	}
	if _, err = os.Stat(filepath.Join(f.root.HostPath, file.Ref.Path)); !os.IsNotExist(err) {
		t.Fatalf("blocked restore changed local file: %v", err)
	}
	f.native(t, []string{"reopen", "-c", "default"}, "tracked.txt")
	if _, err = f.fs.Restore(f.workspace, item.ID); err != nil {
		t.Fatal(err)
	}
	item, err = f.fs.Trash(f.workspace, file.Ref)
	if err != nil || item.Registration != nil {
		t.Fatalf("second Trash: %+v %v", item, err)
	}
	if err = os.WriteFile(f.p.trashRecordPath(f.workspace, item.ID), []byte("damaged record"), 0600); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: false})
	if _, err = f.fs.Restore(f.workspace, item.ID); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("silently bypassed corrupt P4 recovery: %v", err)
	}
}
