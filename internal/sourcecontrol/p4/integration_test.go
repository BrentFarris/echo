package p4

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

type fixture struct {
	p          *Provider
	r          *repository
	fs         *workspacefs.Service
	root       workspacefs.Root
	workspace  string
	connection connection
	manager    *workspaces.Manager
	stopServer func()
}

func newFixture(t *testing.T) *fixture {
	return newFixtureWithUnicode(t, true)
}

func newFixtureWithUnicode(t *testing.T, unicode bool) *fixture {
	t.Helper()
	p4d := os.Getenv("ECHO_P4D")
	if p4d == "" {
		p4d = "p4d"
	}
	for _, binary := range []string{"p4", p4d} {
		if _, err := exec.LookPath(binary); err != nil {
			if os.Getenv("ECHO_REQUIRE_P4") == "1" {
				t.Fatal(err)
			}
			t.Skip("isolated P4 tests require p4 and p4d")
		}
	}
	dir := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(dir); err == nil {
		dir = canonical
	}
	root := filepath.Join(dir, "client ü")
	if !unicode {
		root = filepath.Join(dir, "client")
	}
	depot := filepath.Join(dir, "server")
	for _, path := range []string{root, depot} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().String()
	listener.Close()
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "P4") {
			env = append(env, entry)
		}
	}
	env = append(env, "P4CONFIG=.nonexistent-echo-test-config", "P4ENVIRO="+filepath.Join(dir, "env"), "P4TICKETS="+filepath.Join(dir, "tickets"), "P4TRUST="+filepath.Join(dir, "trust"), "P4PORT="+port, "P4CLIENT=echo-test", "P4USER=echo-test")
	if unicode {
		env = append(env, "P4CHARSET=utf8")
		initialize := exec.Command(p4d, "-r", depot, "-xi")
		initialize.Dir = depot
		initialize.Env = env
		hideWindow(initialize)
		if output, err := initialize.CombinedOutput(); err != nil {
			t.Fatalf("initialize Unicode server: %s %v", output, err)
		}
	} else {
		env = append(env, "P4CHARSET=none")
	}
	cmd := exec.Command(p4d, "-r", depot, "-p", port, "-L", filepath.Join(dir, "p4.log"), "-J", filepath.Join(dir, "journal"))
	cmd.Dir = depot
	cmd.Env = env
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", port, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("p4d startup timeout")
		}
		time.Sleep(25 * time.Millisecond)
	}
	dataPath := filepath.Join(dir, "config", "echo.json")
	manager := workspaces.NewManager(dataPath)
	workspace, err := manager.Create(workspaces.CreateRequest{Name: "P4 test", MainPath: root})
	if err != nil {
		t.Fatal(err)
	}
	filesystem := workspacefs.New(manager, dataPath)
	t.Cleanup(filesystem.Close)
	p := New(filesystem, nil, filepath.Join(dir, "config", "p4"))
	p.env = env
	t.Cleanup(p.Close)
	c := connection{Directory: root, Server: port, Client: "echo-test", User: "echo-test"}
	form := fmt.Sprintf("Client: echo-test\nOwner: echo-test\nRoot: %s\nOptions: noallwrite noclobber nocompress unlocked nomodtime normdir\nLineEnd: unix\nView:\n\t//depot/... //echo-test/...\n", root)
	if output, err := p.command(context.Background(), c, []byte(form), false, "client", "-i"); err != nil {
		t.Fatalf("client: %s %v", output, err)
	}
	for _, name := range []string{"tracked.txt", "delete.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("base\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.files(context.Background(), c, []string{"add"}, []string{filepath.Join(root, "tracked.txt"), filepath.Join(root, "delete.txt")}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.run(context.Background(), c, "submit", "-d", "seed fixture"); err != nil {
		t.Fatal(err)
	}
	roots, err := filesystem.Roots(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := p.Settings(workspace.ID)
	settings.Roots[roots[0].ID] = Override{Server: port, Client: c.Client, User: c.User}
	if err := p.Configure(workspace.ID, settings); err != nil {
		t.Fatal(err)
	}
	repos, err := p.Repositories(context.Background(), workspace.ID)
	if err != nil || len(repos) != 1 {
		t.Fatalf("discover: %+v %v %s", repos, err, p.Descriptor(context.Background(), workspace.ID).Diagnostic)
	}
	r, err := p.repo(context.Background(), workspace.ID, repos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	filesystem.SetMutationCoordinator(p)
	return &fixture{p: p, r: r, fs: filesystem, root: roots[0], workspace: workspace.ID, connection: c, manager: manager, stopServer: func() { _ = cmd.Process.Kill() }}
}
func (f *fixture) action(t *testing.T, request sourcecontrol.ActionRequest) sourcecontrol.ActionResult {
	t.Helper()
	result, err := f.p.Action(context.Background(), f.workspace, f.r.id, request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *fixture) status(t *testing.T) sourcecontrol.StatusSnapshot {
	t.Helper()
	result, err := f.p.Status(context.Background(), f.workspace, f.r.id)
	if err != nil || result.Stale {
		t.Fatalf("status: %+v %v", result, err)
	}
	return result
}

func TestP4IntegrationTrackingAndDiff(t *testing.T) {
	f := newFixture(t)
	status := f.status(t)
	if status.Groups[0].ID != "default" || len(status.Groups[0].Changes) != 0 {
		t.Fatalf("default: %+v", status)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	ref := workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"}
	before, err := f.fs.Read(f.workspace, ref)
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: ref, Content: "edited\n", ExpectedRevision: before.Revision})
	if err != nil || after.Registration != nil {
		t.Fatalf("save: %+v %v", after, err)
	}
	status = f.status(t)
	if len(status.Groups[0].Changes) != 1 {
		t.Fatalf("opened: %+v", status)
	}
	diff, err := f.p.Diff(context.Background(), f.workspace, f.r.id, sourcecontrol.DiffTarget{Kind: "change", Path: ref.Path, GroupID: "default"})
	if err != nil || diff.Original.Content != "base\n" || diff.Modified.Content != "edited\n" {
		t.Fatalf("diff: %+v %v", diff, err)
	}
	created := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "Empty numbered change"})
	status = f.status(t)
	if len(status.Groups[0].Changes) != 1 || status.Groups[1].ID != created.GroupID || len(status.Groups[1].Changes) != 0 {
		t.Fatalf("create swept Default: %+v", status)
	}
	if _, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: ref, Content: "edited twice\n", ExpectedRevision: after.Revision}); err != nil {
		t.Fatal(err)
	}
	if len(f.status(t).Groups[0].Changes) != 1 {
		t.Fatal("save changed existing changelist")
	}
	newRef := workspacefs.FileRef{RootID: f.root.ID, Path: "new ü.txt"}
	newFile, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: newRef, Content: "new\n", CreateOnly: true})
	if newFile.Registration != nil {
		t.Logf("registration: %+v", *newFile.Registration)
	}
	if err != nil || newFile.Registration != nil {
		t.Fatalf("add: %+v %v", newFile, err)
	}
	moved, err := f.fs.Rename(f.workspace, ref, "renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	status = f.status(t)
	if status.Groups[0].Changes[0].OldPath != "tracked.txt" {
		t.Fatalf("move: %+v", status)
	}
	if _, err := f.fs.Read(f.workspace, moved.Ref); err != nil {
		t.Fatal(err)
	}
	trash, err := f.fs.Trash(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "delete.txt"})
	if err != nil || trash.Registration != nil {
		t.Fatalf("trash: %+v %v", trash, err)
	}
	restored, err := f.fs.Restore(f.workspace, trash.ID)
	if err != nil || restored.Registration != nil {
		t.Fatalf("restore: %+v %v", restored, err)
	}
}

func TestP4IntegrationReconcileAndRecovery(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	ref := workspacefs.FileRef{RootID: f.root.ID, Path: "offline%#@ü.txt"}
	f.p.commandHook = func(args []string) error {
		if args[0] == "add" {
			return errors.New("simulated registration disconnect")
		}
		return nil
	}
	file, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: ref, Content: "local success\n", CreateOnly: true})
	if err != nil || file.Registration == nil || !file.Registration.Pending {
		t.Fatalf("save must succeed and queue: %+v %v", file, err)
	}
	f.p.commandHook = nil
	// A fresh provider loads the durable registration record, with no replay of
	// file contents. Explicit retry registers only that recorded path.
	p := New(f.fs, nil, f.p.dataDir)
	p.env = f.p.env
	t.Cleanup(p.Close)
	f.p = p
	f.fs.SetMutationCoordinator(p)
	repos, err := p.Repositories(context.Background(), f.workspace)
	if err != nil || len(repos) != 1 {
		t.Fatalf("restart: %+v %v", repos, err)
	}
	f.r, err = p.repo(context.Background(), f.workspace, repos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	status := f.status(t)
	if len(status.Groups[len(status.Groups)-1].Changes) != 1 {
		t.Fatalf("pending record disappeared: %+v", status)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "retry_registration", Paths: []string{ref.Path}})
	if len(f.p.pending(f.r)) != 0 {
		t.Fatal("registration not completed")
	}
	// A same-size external edit invalidates the content fingerprint.
	path := filepath.Join(f.root.HostPath, "external.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	preview := f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview", Paths: []string{"external.txt"}}).Preview
	if preview == nil || len(preview.Changes) != 1 || preview.Token == "" {
		t.Fatalf("preview: %+v", preview)
	}
	if err := os.WriteFile(path, []byte("two\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Action(context.Background(), f.workspace, f.r.id, sourcecontrol.ActionRequest{Action: "reconcile_apply", PreviewToken: preview.Token, Confirmed: true}); err == nil {
		t.Fatal("stale preview was applied")
	}
	preview = f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_preview", Paths: []string{"external.txt"}}).Preview
	f.action(t, sourcecontrol.ActionRequest{Action: "reconcile_apply", PreviewToken: preview.Token, Confirmed: true})
	if len(f.status(t).Groups[0].Changes) != 2 {
		t.Fatal("reviewed file not registered")
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "revert", Paths: []string{ref.Path}, GroupID: "default", Confirmed: true})
	if data, err := os.ReadFile(filepath.Join(f.root.HostPath, ref.Path)); err != nil || string(data) != "local success\n" {
		t.Fatalf("revert add removed content: %q %v", data, err)
	}
}

func TestP4IntegrationMovesAndCheckoutFailure(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	ref := workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"}
	before, err := f.fs.Read(f.workspace, ref)
	if err != nil {
		t.Fatal(err)
	}
	f.p.commandHook = func(args []string) error {
		if args[0] == "edit" {
			return errors.New("exclusive checkout denied")
		}
		return nil
	}
	if _, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: ref, ExpectedRevision: before.Revision, Content: "must not write\n"}); err == nil {
		t.Fatal("denied checkout wrote file")
	}
	if data, err := os.ReadFile(filepath.Join(f.root.HostPath, ref.Path)); err != nil || string(data) != "base\n" {
		t.Fatalf("checkout failure changed file: %q %v", data, err)
	}
	f.p.commandHook = nil
	first, err := f.fs.Rename(f.workspace, ref, "first.txt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.fs.Rename(f.workspace, first.Ref, "second.txt")
	if err != nil {
		t.Fatal(err)
	}
	status := f.status(t)
	if len(status.Groups[0].Changes) != 1 || status.Groups[0].Changes[0].OldPath != "tracked.txt" || status.Groups[0].Changes[0].Path != second.Ref.Path {
		t.Fatalf("repeated move: %+v", status)
	}
	diff, err := f.p.Diff(context.Background(), f.workspace, f.r.id, sourcecontrol.DiffTarget{Kind: "change", Path: second.Ref.Path})
	if err != nil || diff.Original.Content != "base\n" {
		t.Fatalf("move base: %+v %v", diff, err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "revert", Paths: []string{second.Ref.Path}, Confirmed: true})
	if _, err := f.fs.Read(f.workspace, ref); err != nil {
		t.Fatal(err)
	}
	_, err = f.fs.Rename(f.workspace, ref, "TRACKED.txt")
	if err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
}

func TestP4IntegrationBoundedStatusAndStaleSnapshot(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "checkout", Paths: []string{"tracked.txt"}})
	commands := [][]string{}
	f.p.commandHook = func(args []string) error {
		commands = append(commands, append([]string{}, args...))
		if args[0] == "reconcile" {
			t.Fatal("ordinary status reconciled")
		}
		return nil
	}
	status := f.status(t)
	if len(status.Groups[0].Changes) != 1 {
		t.Fatal("opened unchanged file hidden")
	}
	if len(commands) > 3 {
		t.Fatalf("unbounded status commands: %+v", commands)
	}
	f.p.commandHook = func(args []string) error { return errors.New("connection unavailable") }
	stale, err := f.p.Status(context.Background(), f.workspace, f.r.id)
	if err != nil || !stale.Stale || stale.TotalChangeCount != status.TotalChangeCount {
		t.Fatalf("stale: %+v %v", stale, err)
	}
	f.p.commandHook = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.p.run(ctx, f.connection, "info"); err == nil {
		t.Fatal("cancelled command succeeded")
	}
}

func TestP4IntegrationUnicodeDescription(t *testing.T) {
	f := newFixture(t)
	created := f.action(t, sourcecontrol.ActionRequest{Action: "create_change", Message: "Unicode description ü\nsecond line"})
	rows, err := f.p.run(context.Background(), f.connection, "change", "-o", created.GroupID)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0]["Description"], "description ü") {
		t.Fatalf("spec: %+v %v", rows, err)
	}
}
