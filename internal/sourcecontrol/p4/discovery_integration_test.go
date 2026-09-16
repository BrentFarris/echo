package p4

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

func TestP4IntegrationMultipleRootsShareClient(t *testing.T) {
	f := newFixture(t)
	one, two := filepath.Join(f.root.HostPath, "one"), filepath.Join(f.root.HostPath, "two")
	for _, path := range []string{one, two} {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	workspace, err := f.manager.Create(workspaces.CreateRequest{Name: "Two scopes", MainPath: one, Folders: []string{two}})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := f.fs.Roots(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := f.p.Settings(workspace.ID)
	for _, root := range roots {
		settings.Roots[root.ID] = Override{Server: f.connection.Server, User: f.connection.User, Client: f.connection.Client}
	}
	if err = f.p.Configure(workspace.ID, settings); err != nil {
		t.Fatal(err)
	}
	repositories, err := f.p.Repositories(context.Background(), workspace.ID)
	if err != nil || len(repositories) != 1 || len(repositories[0].Scopes) != 2 {
		t.Fatalf("scoped client grouping: %+v %v", repositories, err)
	}
	if _, err = f.p.Action(context.Background(), workspace.ID, repositories[0].ID, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		saved, err := f.fs.Save(workspace.ID, workspacefs.SaveRequest{Ref: workspacefs.FileRef{RootID: root.ID, Path: "new.txt"}, Content: "new\n", CreateOnly: true})
		if err != nil || saved.Registration != nil {
			t.Fatalf("root %s: %+v %v", root.ID, saved, err)
		}
	}
	status, err := f.p.Status(context.Background(), workspace.ID, repositories[0].ID)
	if err != nil || len(status.Groups[0].Changes) != 2 {
		t.Fatalf("multi-root status: %+v %v", status, err)
	}
}

func TestP4IntegrationConnectionChangeRetainsPendingIdentity(t *testing.T) {
	f := newFixture(t)
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.p.commandHook = func(args []string) error {
		if args[0] == "add" {
			return errors.New("failed registration")
		}
		return nil
	}
	saved, err := f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: workspacefs.FileRef{RootID: f.root.ID, Path: "queued.txt"}, Content: "keep\n", CreateOnly: true})
	if err != nil || saved.Registration == nil {
		t.Fatalf("expected queued file: %+v %v", saved, err)
	}
	f.p.commandHook = nil
	other := f.connection
	other.Client = "alternate"
	other.CommandCharset = "utf8"
	original := f.connection
	original.CommandCharset = "utf8"
	form, err := f.p.command(context.Background(), original, nil, false, "client", "-o", f.connection.Client)
	if err != nil {
		t.Fatal(err)
	}
	spec := replaceField(replaceField(string(form), "Client", other.Client), "View", "//depot/... //alternate/...")
	if _, err = f.p.command(context.Background(), other, []byte(spec), false, "client", "-i"); err != nil {
		t.Fatal(err)
	}
	settings := f.p.Settings(f.workspace)
	settings.Roots[f.root.ID] = Override{Server: other.Server, User: other.User, Client: other.Client}
	if err = f.p.Configure(f.workspace, settings); err != nil {
		t.Fatal(err)
	}
	p := New(f.fs, nil, f.p.dataDir)
	p.env = f.p.env
	t.Cleanup(p.Close)
	repos, err := p.Repositories(context.Background(), f.workspace)
	if err != nil || len(repos) != 2 {
		t.Fatalf("connection recovery identities: %+v %v", repos, err)
	}
	old, err := p.repo(context.Background(), f.workspace, f.r.id)
	if err != nil || !old.detached || old.connection.Client != f.connection.Client {
		t.Fatalf("old identity changed: %+v %v", old, err)
	}
	if p.Tracks(f.workspace, filepath.Join(f.root.HostPath, "queued.txt")) {
		t.Fatal("previous client still auto-tracks the new connection")
	}
	if len(p.pending(old)) != 1 {
		t.Fatal("pending record vanished after connection change")
	}
}

func TestNullClientRootPathsRemainScoped(t *testing.T) {
	r := &repository{roots: []workspacefs.Root{{ID: "one", HostPath: filepath.Join(t.TempDir(), "one")}, {ID: "two", HostPath: filepath.Join(t.TempDir(), "two")}}}
	for _, root := range r.roots {
		path := filepath.Join(root.HostPath, "dir", "file.txt")
		if got := r.absolute(r.relative(path)); got != path {
			t.Fatalf("roundtrip %s: %s", path, got)
		}
	}
	if r.absolute("roots/one/../../escape") != "" {
		t.Fatal("logical root traversal accepted")
	}
}

func TestP4IntegrationSlowRootDoesNotHideHealthyClient(t *testing.T) {
	f := newFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); _ = listener.Close() })
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		<-stop // Accept P4's connection without ever completing its handshake.
	}()
	unavailable := t.TempDir()
	workspace, err := f.manager.Create(workspaces.CreateRequest{Name: "Mixed P4 connections", MainPath: unavailable, Folders: []string{f.root.HostPath}})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := f.fs.Roots(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings := f.p.Settings(workspace.ID)
	for _, root := range roots {
		server := f.connection.Server
		if root.HostPath == unavailable {
			server = listener.Addr().String()
		}
		settings.Roots[root.ID] = Override{Server: server, User: f.connection.User, Client: f.connection.Client}
	}
	if err := f.p.Configure(workspace.ID, settings); err != nil {
		t.Fatal(err)
	}
	var commands atomic.Int64
	f.p.commandHook = func([]string) error { commands.Add(1); return nil }
	repositories, err := f.p.Repositories(context.Background(), workspace.ID)
	if err != nil || len(repositories) != 1 || len(repositories[0].Scopes) != 1 {
		t.Fatalf("healthy root was hidden: %+v %v", repositories, err)
	}
	before := commands.Load()
	if _, err := f.p.Repositories(context.Background(), workspace.ID); err != nil {
		t.Fatal(err)
	}
	if commands.Load() != before {
		t.Fatal("cached failed discovery invoked P4 again")
	}
}
