package fossil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/sourcecontrol/checkpoint"
)

func TestCheckoutMarkersAreCrossPlatformAndDoNotIncludeArbitraryFossilFiles(t *testing.T) {
	for _, marker := range []string{".fslckout", "_FOSSIL_", ".fos"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, marker), []byte("checkout"), 0o600); err != nil {
				t.Fatal(err)
			}
			if !hasCheckoutMarker(root) {
				t.Fatalf("%s was not recognized on %s", marker, runtime.GOOS)
			}
		})
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "project.fossil"), []byte("repository"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hasCheckoutMarker(root) {
		t.Fatal("an arbitrary .fossil repository database is not a checkout marker")
	}
}

func TestParseInfoHandlesWindowsPathsSpacesAndUnicode(t *testing.T) {
	info := parseInfo("project-name: demo\nrepository: C:\\Users\\Ada Lovelace\\資料\\demo.fossil\nlocal-root: C:\\Users\\Ada Lovelace\\資料\\checkout\\\ncheckout: abcdef123456 2026-09-04 12:00:00 UTC\nparent: 0123456789ab 2026-09-03\ntags: trunk, release\n")
	if info.Repository != `C:\Users\Ada Lovelace\資料\demo.fossil` {
		t.Fatalf("repository = %q", info.Repository)
	}
	if info.LocalRoot != `C:\Users\Ada Lovelace\資料\checkout\` || info.Checkout != "abcdef123456" || info.Parent != "0123456789ab" || info.Branch != "trunk" {
		t.Fatalf("info = %#v", info)
	}
}

func TestParseClassifiedChangesAndExtras(t *testing.T) {
	changes := parseClassifiedChanges(strings.Join([]string{
		"EDITED      src/main.go",
		"ADDED       docs/new file.md",
		"DELETED     old.txt",
		"MISSING     gone.txt",
		"RENAMED     old name.txt -> 新しい name.txt",
		"EDITED      original.txt  ->  renamed and edited.txt",
		"CONFLICT    merge.txt",
		"UPDATED_BY_MERGE merged.txt",
		"EXTRA       scratch notes.txt",
	}, "\r\n"))
	if len(changes) != 9 {
		t.Fatalf("changes = %#v", changes)
	}
	if changes[4].oldPath != "old name.txt" || changes[4].path != "新しい name.txt" || changes[4].kind != "renamed" {
		t.Fatalf("rename = %#v", changes[4])
	}
	if changes[5].code != "RENAMED" || changes[5].oldPath != "original.txt" || changes[5].path != "renamed and edited.txt" || changes[5].kind != "renamed" {
		t.Fatalf("edited rename = %#v", changes[5])
	}
	if changes[6].group != "conflicts" || changes[8].group != "untracked" {
		t.Fatalf("groups = %#v", changes)
	}
	extras := parseExtras("loose file.txt\n資料/notes.md\n")
	if len(extras) != 2 || extras[1].path != "資料/notes.md" {
		t.Fatalf("extras = %#v", extras)
	}
}

func TestTimelinePhaseIsNotTreatedAsAParent(t *testing.T) {
	separator := "\x1f"
	output := strings.Join([]string{
		"abcdef012345" + separator + "*CURRENT* *LEAF*" + separator + "Ada" + separator + "2026-09-04T12:00:00Z" + separator + "trunk" + separator + "release" + separator + "Ship it",
		"123456789abc" + separator + "" + separator + "Grace" + separator + "2026-09-03T12:00:00Z" + separator + "feature" + separator + "" + separator + "Earlier",
	}, "\n")
	commits := parseTimeline(output, separator)
	if len(commits) != 2 {
		t.Fatalf("commits = %#v", commits)
	}
	if len(commits[0].Parents) != 0 {
		t.Fatalf("timeline phase was parsed as parents: %#v", commits[0].Parents)
	}
	joined := strings.Join(commits[0].Refs, " ")
	if !strings.Contains(joined, "*CURRENT*") || !strings.Contains(joined, "trunk") || commits[0].Subject != "Ship it" {
		t.Fatalf("commit = %#v", commits[0])
	}
}

func TestPathValidationAndRedaction(t *testing.T) {
	root := t.TempDir()
	state := repositoryState{
		root: root, repository: filepath.Join(root, "visible", "repository.data"),
		scopes: []repositoryScope{{Scope: sourceControlScope("root", "project", "visible"), rootPath: filepath.Join(root, "visible")}},
	}
	if !state.pathAllowed("visible/src/main.go") {
		t.Fatal("visible file should be allowed")
	}
	for _, path := range []string{"outside.txt", "visible/.git/config", "visible/.fslckout", "visible/.echo/workspace.json", "visible/repository.data", "../escape"} {
		if state.pathAllowed(path) {
			t.Fatalf("protected or out-of-scope path was allowed: %s", path)
		}
	}
	message := sanitizeOutput("failed https://user:secret@example.test/repo?access_token=query-secret at "+root+strings.Repeat("x", 10<<10), root)
	if strings.Contains(message, "secret") || strings.Contains(strings.ToLower(message), strings.ToLower(root)) || len(message) > (8<<10)+len("…") {
		t.Fatalf("sensitive output was not redacted: %q", message)
	}
}

func TestSandboxCheckoutFailureHasActionableDiagnostic(t *testing.T) {
	message := "database error: unable to open database file"
	if code := classifyCommandError(message, true); code != "fossil_checkout_unavailable_in_sandbox" {
		t.Fatalf("sandbox checkout error code = %q", code)
	}
	if !strings.Contains(sandboxCheckoutDiagnostic, "disable the sandbox") || !strings.Contains(sandboxCheckoutDiagnostic, "workspace folder") {
		t.Fatalf("sandbox diagnostic is not actionable: %q", sandboxCheckoutDiagnostic)
	}
}

func TestProtectedEntryAssociationFollowsLaterRenames(t *testing.T) {
	entries := map[string]checkpoint.FileState{
		pathIdentity("src/original.txt"): {Path: "src/original.txt", Kind: "modified"},
	}
	entry, ok := protectedEntryForRecord(entries, statusRecord{code: "RENAMED", kind: "renamed", oldPath: "src/original.txt", path: "src/later.txt"})
	if !ok || entry.Path != "src/original.txt" {
		t.Fatalf("later rename was not associated with protection: %#v, %v", entry, ok)
	}
	if _, ok := protectedEntryForRecord(entries, statusRecord{code: "EXTRA", kind: "untracked", path: "src/later.txt"}); ok {
		t.Fatal("an unrelated extra was associated with protection")
	}

	renamed := []checkpoint.FileState{{Path: "src/new.txt", OldPath: "src/old.txt", Kind: "renamed"}}
	if err := validateCheckpointEntries(renamed); err != nil {
		t.Fatalf("one protected rename was rejected: %v", err)
	}
	if err := validateCheckpointEntries(append(renamed, checkpoint.FileState{Path: "src/old.txt", Kind: "added"})); err == nil {
		t.Fatal("overlapping protected rename endpoints were accepted")
	}
}

func TestProtectedRenameAcrossWorkspaceScopeFailsBeforeCapture(t *testing.T) {
	root := t.TempDir()
	state := &repositoryState{
		root: root,
		scopes: []repositoryScope{{
			Scope: sourceControlScope("root", "project", "visible"), rootPath: filepath.Join(root, "visible"),
		}},
	}
	_, _, err := (&Provider{}).captureAffectedStates(state,
		[]checkpoint.FileState{{Path: "visible/original.txt", Exists: true}},
		[]statusRecord{{code: "RENAMED", kind: "renamed", oldPath: "visible/original.txt", path: "hidden/later.txt"}},
	)
	var sourceErr *sourcecontrol.Error
	if !errors.As(err, &sourceErr) || sourceErr.Code != "protected_changes_hidden_rename" {
		t.Fatalf("cross-scope protected rename error = %v", err)
	}
}

func sourceControlScope(rootID, label, prefix string) sourcecontrol.Scope {
	return sourcecontrol.Scope{RootID: rootID, RootLabel: label, RepoPrefix: prefix}
}
