package gotest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
)

func TestParseCoverageProfileMapsOnlyPackageProductionFiles(t *testing.T) {
	directory := t.TempDir()
	writeCoverageTestFile(t, filepath.Join(directory, "logic.go"), "package sample\nfunc Logic() int { return 1 }\n")
	writeCoverageTestFile(t, filepath.Join(directory, "logic_test.go"), "package sample\n")
	profile := filepath.Join(directory, "coverage.out")
	writeCoverageTestFile(t, profile, "mode: count\n"+
		"example.com/sample/logic.go:2.1,2.18 1 3\n"+
		"example.com/sample/logic.go:3.1,3.5 1 0\n"+
		"example.com/sample/logic.go:4.1,4.1 0 0\n"+
		"example.com/sample/logic_test.go:1.1,1.8 1 1\n"+
		"example.com/dependency/logic.go:40.1,40.4 1 1\n")

	mode, files, err := parseCoverageProfile(profile, directory, workspacefs.FileRef{RootID: "root", Path: "sample"})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "count" || len(files) != 1 || files[0].Ref.Path != "sample/logic.go" || len(files[0].Ranges) != 2 {
		t.Fatalf("coverage = mode %q files %#v", mode, files)
	}
	covered, uncovered := files[0].Ranges[0], files[0].Ranges[1]
	if covered.Start.Line != 1 || covered.Start.Character != 0 || covered.End.Character != 17 || covered.Count != 3 {
		t.Fatalf("covered range = %#v", covered)
	}
	if uncovered.Start.Line != 2 || uncovered.Count != 0 {
		t.Fatalf("uncovered range = %#v", uncovered)
	}
}

func TestParseCoverageProfileRejectsMalformedAndOversizedProfiles(t *testing.T) {
	directory := t.TempDir()
	writeCoverageTestFile(t, filepath.Join(directory, "logic.go"), "package sample\n")
	profile := filepath.Join(directory, "coverage.out")
	writeCoverageTestFile(t, profile, "mode: mystery\n")
	if _, _, err := parseCoverageProfile(profile, directory, workspacefs.FileRef{RootID: "root"}); err == nil {
		t.Fatal("unsupported coverage mode was accepted")
	}
	writeCoverageTestFile(t, profile, "mode: set\nnot a coverage record\n")
	if _, _, err := parseCoverageProfile(profile, directory, workspacefs.FileRef{RootID: "root"}); err == nil {
		t.Fatal("malformed coverage record was accepted")
	}
	if err := os.Truncate(profile, MaxCoverageProfileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseCoverageProfile(profile, directory, workspacefs.FileRef{RootID: "root"}); err == nil {
		t.Fatal("oversized coverage profile was accepted")
	}
}

func TestCoverageInvalidationUsesPackageFingerprint(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "logic.go")
	writeCoverageTestFile(t, path, "package sample\n")
	fingerprint, err := packageFingerprint(directory)
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan CoverageEvent, 2)
	service := &Service{
		coverage: map[string]coverageRecord{"workspace": {
			revision: 1, generation: "generation", packageDirectory: directory,
			packageRef: workspacefs.FileRef{RootID: "root", Path: "pkg"}, fingerprint: fingerprint,
		}},
		coverageNotify: func(event CoverageEvent) { events <- event },
	}
	change := workspacefs.Change{Op: "write", Ref: workspacefs.FileRef{RootID: "root", Path: "pkg/logic.go"}}
	service.HandleWorkspaceChanges("workspace", []workspacefs.Change{change})
	select {
	case event := <-events:
		t.Fatalf("unchanged fingerprint was invalidated: %#v", event)
	default:
	}
	writeCoverageTestFile(t, path, "package sample\n// changed\n")
	service.HandleWorkspaceChanges("workspace", []workspacefs.Change{change})
	event := <-events
	if event.State != "cleared" || event.Revision != 2 {
		t.Fatalf("invalidation event = %#v", event)
	}
}

func TestCoverageCompletionPublishesSnapshotAndCleansProfile(t *testing.T) {
	directory := t.TempDir()
	writeCoverageTestFile(t, filepath.Join(directory, "logic.go"), "package sample\nfunc Logic() int { return 1 }\n")
	writeCoverageTestFile(t, filepath.Join(directory, "logic_test.go"), "package sample\n")
	fingerprint, err := packageFingerprint(directory)
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(directory, ".echo-cover-test.out")
	writeCoverageTestFile(t, profile, "mode: set\nexample.com/sample/logic.go:2.1,2.38 1 1\n")
	events := make(chan CoverageEvent, 3)
	service := &Service{
		coverage: map[string]coverageRecord{}, latest: map[string]runRecord{},
		coverageNotify: func(event CoverageEvent) { events <- event },
	}
	packageRef := workspacefs.FileRef{RootID: "root", Path: "sample"}
	generation := service.beginCoverage("workspace", directory, fingerprint, packageRef)
	cleared := <-events
	if cleared.State != "cleared" || cleared.Revision != 1 {
		t.Fatalf("begin event = %#v", cleared)
	}
	service.finishCoverage("workspace", generation, profile, directory, fingerprint, packageRef, terminal.TaskResult{
		WorkspaceID: "workspace", SessionID: "session", ExitCode: 0, Status: "passed",
	})
	ready := <-events
	if ready.State != "ready" || ready.Coverage == nil || ready.Coverage.SessionID != "session" || ready.Revision != 2 {
		t.Fatalf("ready event = %#v", ready)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("coverage profile was not removed: %v", err)
	}
	encoded, err := json.Marshal(ready.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || strings.Contains(string(encoded), directory) {
		t.Fatalf("coverage response exposed host path: %s", encoded)
	}
}

func TestCoverageCompletionFailureCleansProfileAndKeepsCoverageCleared(t *testing.T) {
	directory := t.TempDir()
	writeCoverageTestFile(t, filepath.Join(directory, "logic.go"), "package sample\n")
	fingerprint, err := packageFingerprint(directory)
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(directory, ".echo-cover-failed.out")
	writeCoverageTestFile(t, profile, "mode: set\n")
	events := make(chan CoverageEvent, 2)
	service := &Service{
		coverage: map[string]coverageRecord{}, latest: map[string]runRecord{},
		coverageNotify: func(event CoverageEvent) { events <- event },
	}
	packageRef := workspacefs.FileRef{RootID: "root", Path: "sample"}
	generation := service.beginCoverage("workspace", directory, fingerprint, packageRef)
	<-events
	service.finishCoverage("workspace", generation, profile, directory, fingerprint, packageRef, terminal.TaskResult{
		WorkspaceID: "workspace", SessionID: "session", ExitCode: 1, Status: "failed",
	})
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("failed-run profile was not removed: %v", err)
	}
	record := service.coverage["workspace"]
	if record.snapshot != nil || record.generation != "" {
		t.Fatalf("failed run retained coverage: %#v", record)
	}
	select {
	case event := <-events:
		t.Fatalf("failed run published an unexpected event: %#v", event)
	default:
	}
}

func writeCoverageTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
