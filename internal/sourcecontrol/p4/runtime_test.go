package p4

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/mutation"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

func TestTrackingPersistenceAndCheckoutBeforeWrite(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	filesystem := editorfs.New(func(string) (domain.Bot, error) { return domain.Bot{ID: "bot", Roots: []string{root}}, nil }, data)
	defer filesystem.Close()
	opened, failCheckout, failAdd := false, true, false
	calls := []string{}
	runner := &scmexec.Runtime{Available: func(string, string) error { return nil }, Defaults: func(string, string) map[string]string {
		return map[string]string{"port": "test:1666", "user": "tester", "client": "fixture-client"}
	}}
	runner.Execute = func(_ context.Context, bot string, req scmexec.Request) (scmexec.Result, error) {
		if bot != "bot" {
			t.Errorf("lost bot scope: %s", bot)
		}
		command := ""
		for _, candidate := range []string{"info", "client", "where", "fstat", "edit", "add", "ignores", "opened", "sizes", "print"} {
			if slices.Contains(req.Command, candidate) {
				command = candidate
				break
			}
		}
		calls = append(calls, command)
		rows := record{}
		switch command {
		case "info":
			rows = record{"userName": "tester", "clientName": "fixture-client", "clientRoot": root}
		case "client":
			rows = record{"Client": "fixture-client", "Update": "today", "Root": root, "LineEnd": "unix"}
		case "where":
			rows = record{"depotFile": "//depot/file.txt", "clientFile": "//client/file.txt", "path": filepath.Join(root, "file.txt")}
		case "fstat":
			if !failAdd {
				rows = record{"depotFile": "//depot/file.txt", "clientFile": filepath.Join(root, "file.txt"), "haveRev": "1"}
				if opened {
					rows["action"] = "edit"
					rows["change"] = "19"
				}
			}
		case "opened":
			rows = record{"rev": "2"}
		case "sizes":
			rows = record{"fileSize": "5"}
		case "print":
			if !req.CaptureOutputFile || strings.TrimSpace(string(req.Input)) != "//depot/file.txt#2" {
				t.Fatalf("baseline must use the opened revision and contained file capture: %+v", req)
			}
			return scmexec.Result{Stdout: []byte("base\n")}, nil
		case "edit":
			if failCheckout {
				return scmexec.Result{}, errors.New("checkout denied")
			}
			opened = true
		case "add":
			if failAdd {
				return scmexec.Result{}, errors.New("registration offline")
			}
		case "ignores":
			return scmexec.Result{}, nil
		default:
			return scmexec.Result{}, errors.New("unexpected command")
		}
		out, _ := json.Marshal(rows)
		return scmexec.Result{Stdout: out}, nil
	}
	provider := New(filesystem, runner, data)
	defer provider.Close()
	ctx := context.Background()
	repos, err := provider.Repositories(ctx, "bot")
	if err != nil || len(repos) != 1 {
		t.Fatalf("discover: %v %v", repos, err)
	}
	id := repos[0].ID
	if !provider.Settings("bot").Repositories[id].Tracking {
		t.Fatal("first discovery did not enable tracking")
	}
	settings := provider.Settings("bot")
	entry := settings.Repositories[id]
	entry.Active = "19"
	settings.Repositories[id] = entry
	if err = provider.Configure("bot", settings); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "file.txt")
	_ = os.WriteFile(target, []byte("base"), 0600)
	applied := false
	op := mutation.Operation{WorkspaceID: "bot", Kind: "edit", Path: target}
	_, err = provider.Run(ctx, op, func() error { applied = true; return nil })
	if err == nil || applied {
		t.Fatal("checkout failure did not stop write", err)
	}
	failCheckout = false
	calls = nil
	_, err = provider.Run(ctx, op, func() error { calls = append(calls, "write"); return os.WriteFile(target, []byte("edited"), 0600) })
	if err != nil {
		t.Fatal(err)
	}
	if slices.Index(calls, "edit") < 0 || slices.Index(calls, "edit") > slices.Index(calls, "write") {
		t.Fatal("checkout did not precede write", calls)
	}
	diff, err := provider.Diff(ctx, "bot", id, sourcecontrol.DiffTarget{Kind: "change", Path: "file.txt", GroupID: "19"})
	if err != nil || diff.Original.Content != "base\n" || diff.Modified.Content != "edited" {
		t.Fatalf("opened baseline: %+v %v", diff, err)
	}
	// Successful local creation is retained even if native registration fails.
	failAdd = true
	addition := filepath.Join(root, "new.txt")
	result, err := provider.Run(ctx, mutation.Operation{WorkspaceID: "bot", Kind: "create", Path: addition}, func() error { return os.WriteFile(addition, []byte("new"), 0600) })
	if err != nil || !result.Pending || result.OperationID == "" {
		t.Fatalf("pending: %+v %v", result, err)
	}
	restarted := New(filesystem, runner, data)
	defer restarted.Close()
	states, err := restarted.discover(ctx, "bot")
	if err != nil || len(states) != 1 {
		t.Fatal(err)
	}
	found := false
	for _, record := range restarted.pending(states[0]) {
		if record.ID == result.OperationID && record.LocalApplied {
			found = true
		}
	}
	if !found {
		t.Fatal("registration journal lost across restart")
	}
	settings = restarted.Settings("bot")
	entry = settings.Repositories[id]
	entry.Tracking = false
	settings.Repositories[id] = entry
	if err = restarted.Configure("bot", settings); err != nil {
		t.Fatal(err)
	}
	again := New(filesystem, runner, data)
	defer again.Close()
	if _, err = again.Repositories(ctx, "bot"); err != nil {
		t.Fatal(err)
	}
	if again.Settings("bot").Repositories[id].Tracking {
		t.Fatal("discovery overrode remembered disable")
	}
}
