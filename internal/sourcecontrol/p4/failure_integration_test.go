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
)

func TestP4IntegrationExclusiveLockDeniesWrite(t *testing.T) {
	f := newFixture(t)
	f.native(t, []string{"edit", "-t", "text+l"}, "tracked.txt")
	if _, err := f.p.run(context.Background(), f.connection, "submit", "-d", "exclusive type fixture"); err != nil {
		t.Fatal(err)
	}
	otherRoot := filepath.Join(filepath.Dir(f.root.HostPath), "other-client")
	if err := os.Mkdir(otherRoot, 0755); err != nil {
		t.Fatal(err)
	}
	other := f.connection
	other.Client = "other-client"
	other.Directory = otherRoot
	other.CommandCharset = "utf8"
	form, err := f.p.command(context.Background(), f.connection, nil, false, "client", "-o", f.connection.Client)
	if err != nil {
		t.Fatal(err)
	}
	spec := replaceField(replaceField(replaceField(string(form), "Client", other.Client), "Root", otherRoot), "View", "//depot/... //other-client/...")
	if _, err = f.p.command(context.Background(), other, []byte(spec), false, "client", "-i"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(otherRoot, "tracked.txt")
	if _, err = f.p.files(context.Background(), other, []string{"sync"}, []string{file}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.p.files(context.Background(), other, []string{"edit"}, []string{file}); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	before, err := f.fs.Read(f.workspace, workspacefs.FileRef{RootID: f.root.ID, Path: "tracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.fs.Save(f.workspace, workspacefs.SaveRequest{Ref: before.Ref, ExpectedRevision: before.Revision, Content: "blocked\n"}); err == nil {
		t.Fatal("exclusive lock allowed write")
	}
	after, err := f.fs.Read(f.workspace, before.Ref)
	if err != nil || after.Content != before.Content {
		t.Fatalf("denial changed content: %+v %v", after, err)
	}
}

func TestP4IntegrationCredentialsAndTimeout(t *testing.T) {
	f := newFixture(t)
	// All credentials and configuration belong to this temporary p4d only.
	password := "Isolated-P4-Fixture-1234!"
	if _, err := f.p.command(context.Background(), f.connection, nil, false, "passwd", "-P", password); err != nil {
		t.Fatal(err)
	}
	f.p.env = append(f.p.env, "P4PASSWD="+password)
	if _, err := f.p.run(context.Background(), f.connection, "configure", "set", "security=2"); err != nil {
		t.Fatal(err)
	}
	filtered := []string{}
	for _, entry := range f.p.env {
		if !strings.HasPrefix(entry, "P4PASSWD=") {
			filtered = append(filtered, entry)
		}
	}
	f.p.env = filtered
	status, err := f.p.Status(context.Background(), f.workspace, f.r.id)
	if err != nil || !status.Stale || !strings.Contains(strings.ToLower(status.Diagnostic), "password") {
		t.Fatalf("missing credentials: %+v %v", status, err)
	}
	f.p.timeout = time.Nanosecond
	if _, err = f.p.run(context.Background(), f.connection, "info"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestP4IntegrationDiffUsesSyncedBaseAndRepresentations(t *testing.T) {
	f := newFixture(t)
	files := map[string][]byte{"bom.txt": append([]byte{239, 187, 191}, []byte("base\r\n")...), "binary.dat": {0, 1, 2}, "large.txt": []byte(strings.Repeat("x", int(workspacefs.MaxEditableBytes)+1))}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(f.root.HostPath, name), data, 0644); err != nil {
			t.Fatal(err)
		}
		f.native(t, []string{"add"}, name)
	}
	if _, err := f.p.run(context.Background(), f.connection, "submit", "-d", "representation fixture"); err != nil {
		t.Fatal(err)
	}
	// The fixture's client uses unix line endings. Force a sync so its text and
	// Unicode baseline represent the actual client, not pre-add local bytes.
	f.native(t, []string{"sync", "-f"}, "bom.txt")
	for name := range files {
		diff, err := f.p.Diff(context.Background(), f.workspace, f.r.id, sourcecontrol.DiffTarget{Kind: "change", Path: name})
		if err != nil {
			t.Fatal(err)
		}
		switch name {
		case "bom.txt":
			if diff.Kind != "text" || diff.Original.Content != diff.Modified.Content || diff.Original.HasBOM != diff.Modified.HasBOM {
				t.Fatalf("BOM/EOL diff: %+v", diff)
			}
		case "binary.dat":
			if diff.Kind != "binary" {
				t.Fatalf("binary: %+v", diff)
			}
		case "large.txt":
			if diff.Kind != "too-large" {
				t.Fatalf("size: %+v", diff)
			}
		}
	}
	f.native(t, []string{"edit"}, "tracked.txt")
	if err := os.WriteFile(filepath.Join(f.root.HostPath, "tracked.txt"), []byte("new head\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.run(context.Background(), f.connection, "submit", "-d", "new depot revision"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.runInput(context.Background(), f.connection, []string{"sync"}, []string{fileArg(filepath.Join(f.root.HostPath, "tracked.txt")) + "#1"}); err != nil {
		t.Fatal(err)
	}
	f.action(t, sourcecontrol.ActionRequest{Action: "set_tracking", Confirmed: true})
	f.save(t, "tracked.txt", "edit old revision\n")
	diff, err := f.p.Diff(context.Background(), f.workspace, f.r.id, sourcecontrol.DiffTarget{Kind: "change", Path: "tracked.txt"})
	if err != nil || diff.Original.Content != "base\n" {
		t.Fatalf("substituted head for synced base: %+v %v", diff, err)
	}
}
