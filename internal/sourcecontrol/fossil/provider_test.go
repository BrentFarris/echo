package fossil

import (
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/scmexec"
)

func TestResetOnlyRediscoversUsedWorkspaces(t *testing.T) {
	scans := make(map[string]int)
	workspaces := &scmexec.Workspaces{Get: func(id string) (scmexec.Workspace, bool, error) {
		scans[id]++
		return scmexec.Workspace{}, true, nil
	}}
	fs := editorfs.New(func(id string) (domain.Bot, error) { return domain.Bot{ID: id}, nil }, t.TempDir())
	defer fs.Close()
	p := New(workspaces, fs, &scmexec.Runtime{}, t.TempDir())
	defer p.Close()
	ctx := t.Context()
	if err := p.ResetWorkspace(ctx, "idle"); err != nil || scans["idle"] != 0 {
		t.Fatalf("reset started discovery for an unopened workspace: scans=%d err=%v", scans["idle"], err)
	}
	if _, err := p.Repositories(ctx, "idle"); err != nil || scans["idle"] != 1 {
		t.Fatalf("first use did not discover repositories: scans=%d err=%v", scans["idle"], err)
	}
	if err := p.ResetWorkspace(ctx, "idle"); err != nil || scans["idle"] != 2 {
		t.Fatalf("reset did not rebuild a discovered workspace: scans=%d err=%v", scans["idle"], err)
	}
	if err := p.Subscribe(ctx, "watching"); err != nil {
		t.Fatal(err)
	}
	if err := p.ResetWorkspace(ctx, "watching"); err != nil || scans["watching"] != 1 {
		t.Fatalf("reset did not rebuild a subscribed workspace: scans=%d err=%v", scans["watching"], err)
	}
}
