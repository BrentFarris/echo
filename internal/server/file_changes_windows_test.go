//go:build windows

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func TestNativeCommandFileChangesIncludePartialEffects(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("set ECHO2_NATIVE_TESTS=1")
	}
	s := testServer(t)
	root := t.TempDir()
	for _, name := range []string{"edited.txt", "deleted.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	collector := &fileChangeCollector{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), fileChangesKey{}, collector), 30*time.Second)
	defer cancel()
	bot := domain.Bot{ID: "file-changes", Roots: []string{root}}
	result, err := s.runTrackedProcess(ctx, bot, broker.ProcessRequest{BotID: bot.ID, Roots: bot.Roots, Directory: root, Command: "[IO.File]::WriteAllText([IO.Path]::Combine($env:ECHO_WORKSPACE, 'created.txt'), 'new'); [IO.File]::WriteAllText([IO.Path]::Combine($env:ECHO_WORKSPACE, 'edited.txt'), 'after'); [IO.File]::Delete([IO.Path]::Combine($env:ECHO_WORKSPACE, 'deleted.txt')); exit 7"}, nil)
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("%+v %v", result, err)
	}
	if collector.incomplete || len(collector.changes) != 3 {
		t.Fatalf("%+v", collector)
	}
	operations := map[string]string{}
	for _, change := range collector.changes {
		operations[filepath.Base(change.Path)] = change.Operation
	}
	if operations["created.txt"] != "created" || operations["edited.txt"] != "edited" || operations["deleted.txt"] != "deleted" {
		t.Fatal(operations)
	}
}
