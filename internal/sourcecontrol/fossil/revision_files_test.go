package fossil

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

func TestRevisionFilesDeltaManifest(t *testing.T) {
	artifacts := map[string]string{
		"base":  "F old\\sname.txt hash1\nF removed.txt hash2\nF executable hash3\nF inherited hash4 w ancient\n",
		"delta": "B base\nF old\\sname.txt\nF new\\sname.txt hash1 w old\\sname.txt\nF removed.txt\nF added hash5\nF executable hash3 x\n",
	}
	p := New(nil, nil, &scmexec.Runtime{Execute: func(_ context.Context, _ string, request scmexec.Request) (scmexec.Result, error) {
		return scmexec.Result{Stdout: []byte(artifacts[request.Command[2]])}, nil
	}}, t.TempDir())
	defer p.Close()
	state := &repositoryState{workspaceID: "test", root: t.TempDir()}
	before, err := p.revisionEntries(t.Context(), state, "base", 0)
	if err != nil {
		t.Fatal(err)
	}
	after, err := p.revisionEntries(t.Context(), state, "delta", 0)
	if err != nil {
		t.Fatal(err)
	}
	if after["inherited"].oldPath != "" {
		t.Fatal("baseline rename annotation leaked into later revision")
	}
	want := []sourcecontrol.RevisionFile{{Path: "added", Status: "A"}, {Path: "executable", Status: "M"}, {Path: "new name.txt", OldPath: "old name.txt", Status: "R"}, {Path: "removed.txt", Status: "D"}}
	if actual := changedRevisionFiles(before, after); !reflect.DeepEqual(actual, want) {
		t.Fatalf("files: %+v", actual)
	}
	artifacts["base"] = "B delta\n"
	if _, err := p.revisionEntries(t.Context(), state, "delta", 0); err == nil {
		t.Fatal("accepted cyclic baseline")
	}
}

func TestInspectionTimelineNormalizesUTC(t *testing.T) {
	record := strings.Join([]string{strings.Repeat("a", 64), "aaaaaaaaaa", "", "author", "2026-11-05 21:00:00", "trunk", "", "message"}, timelineFieldSeparator) + timelineRecordSeparator
	commits, err := parseInspectionTimeline(record)
	if err != nil || len(commits) != 1 || commits[0].AuthoredAt != "2026-11-05T21:00:00Z" || len(commits[0].Hash) != 64 {
		t.Fatalf("timeline: %+v %v", commits, err)
	}
}
