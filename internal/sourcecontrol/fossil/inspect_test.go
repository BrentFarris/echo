package fossil

import (
	"strings"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
)

func TestParseInspectionTimelineKeepsFossilMetadataAndIgnoresPhase(t *testing.T) {
	formatRecord := func(fields ...string) string {
		return strings.Join(fields, timelineFieldSeparator) + timelineRecordSeparator + "\n"
	}
	output := formatRecord(
		"abcdef0123456789", "abcdef0123", "*CURRENT* *LEAF*", "Åda", "2026-09-05 12:30:00",
		"trunk", "release, trunk", "Subject line\n\nMore detail",
	) + formatRecord("1234567890abcdef", "1234567890", "", "Grace", "2026-09-04 10:00:00", "feature", "", "Earlier")
	commits, err := parseInspectionTimeline(output)
	if err != nil || len(commits) != 2 {
		t.Fatalf("commits = %#v, %v", commits, err)
	}
	first := commits[0]
	if first.Hash != "abcdef0123456789" || first.ShortHash != "abcdef0123" || first.Author != "Åda" || first.Branch != "trunk" || first.Subject != "Subject line" || !strings.Contains(first.Message, "More detail") {
		t.Fatalf("first commit = %#v", first)
	}
	if len(first.Parents) != 0 {
		t.Fatalf("Fossil timeline phase was treated as parent metadata: %#v", first.Parents)
	}
	if len(first.Tags) != 2 || first.Refs[0] != "release" {
		t.Fatalf("tags/refs = %#v / %#v", first.Tags, first.Refs)
	}
	if _, err := parseInspectionTimeline("not a formatted timeline"); err == nil {
		t.Fatal("malformed timeline output was accepted")
	}
}

func TestTimelineInspectArgsUseNativeConstraintsAndWholeRepositoryDefault(t *testing.T) {
	whole := timelineInspectArgs(sourcecontrol.HistoryQuery{}, 4, 21)
	joined := strings.Join(whole, " ")
	if strings.Contains(joined, "ancestors") || strings.Contains(joined, "--branch") || !strings.Contains(joined, "--offset 4") || !strings.Contains(joined, "-n 21") || !strings.Contains(joined, " -q ") {
		t.Fatalf("whole-repository args = %#v", whole)
	}
	filtered := timelineInspectArgs(sourcecontrol.HistoryQuery{Revision: "abc123", Path: "src/main.go", Author: "ada"}, 0, 10)
	joined = strings.Join(filtered, " ")
	for _, expected := range []string{"--path ./src/main.go", "--for-user ada", "ancestors abc123"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("filtered args %q do not contain %q", joined, expected)
		}
	}
	before := strings.Join(timelineInspectArgs(sourcecontrol.HistoryQuery{Until: "2026-09-05T23:59:59"}, 0, 10), " ")
	if !strings.Contains(before, "before 2026-09-05T23:59:59") {
		t.Fatalf("upper date was not delegated to Fossil: %q", before)
	}
}

func TestFossilSearchScopesAndBounds(t *testing.T) {
	request, err := normalizeSearchRequest(sourcecontrol.RepositorySearchRequest{Query: " naïve query ", Scopes: []string{"docs", "forum", "wiki"}, Limit: 500, MaxOutputBytes: sourcecontrol.InspectionMaximumOutputMax + 1})
	if err != nil {
		t.Fatal(err)
	}
	if request.Query != "naïve query" || request.Limit != 100 || request.MaxOutputBytes != sourcecontrol.InspectionMaximumOutputMax {
		t.Fatalf("request = %#v", request)
	}
	joined := strings.Join(fossilSearchArgs(request), " ")
	for _, expected := range []string{"search", "--highlight 0", "-W 0", "-n 100", "--docs", "--forum", "--wiki", "naïve query"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("search args %q do not contain %q", joined, expected)
		}
	}
	defaults, err := normalizeSearchRequest(sourcecontrol.RepositorySearchRequest{Query: "fix"})
	if err != nil || len(defaults.Scopes) != 1 || defaults.Scopes[0] != "checkins" || defaults.Limit != 20 || defaults.MaxOutputBytes != sourcecontrol.InspectionDefaultOutputMax {
		t.Fatalf("defaults = %#v, %v", defaults, err)
	}
	if _, err := normalizeSearchRequest(sourcecontrol.RepositorySearchRequest{Query: "fix", Scopes: []string{"all", "tickets"}}); err == nil {
		t.Fatal("all combined with another search scope was accepted")
	}
}

func TestCappedBufferReportsOutputTruncation(t *testing.T) {
	buffer := cappedBuffer{limit: 4}
	if written, err := buffer.Write([]byte("abcdef")); err != nil || written != 6 || string(buffer.Bytes()) != "abcd" || !buffer.Truncated() {
		t.Fatalf("buffer=%q truncated=%v written=%d err=%v", buffer.Bytes(), buffer.Truncated(), written, err)
	}
}

func TestProtectedUnifiedPatchIsBoundedAndCountsChanges(t *testing.T) {
	patch, added, removed, truncated := boundedUnifiedFilePatch("old.txt", "new.txt", []byte("one\ntwo\n"), []byte("one\nthree\n"), true, true, 3, 32)
	if patch == "" || added != 1 || removed != 1 || !truncated || len(patch) > 32 {
		t.Fatalf("patch=%q added=%d removed=%d truncated=%v", patch, added, removed, truncated)
	}
	full, _, _, truncated := boundedUnifiedFilePatch("missing.txt", "missing.txt", nil, []byte("new\n"), false, true, 3, 4096)
	if truncated || !strings.Contains(full, "/dev/null") || !strings.Contains(full, "b/missing.txt") {
		t.Fatalf("addition patch = %q, truncated=%v", full, truncated)
	}
}

func TestRevisionInfoCollectsMergeParents(t *testing.T) {
	info := parseRevisionInfo(strings.Join([]string{
		"hash: abcdef012345 2026-09-05 10:00:00 UTC",
		"parent: 111111111111 2026-09-04 10:00:00 UTC",
		"merge-parent: 222222222222 2026-09-04 11:00:00 UTC",
		"tags: trunk, release",
	}, "\n"))
	if info.Hash != "abcdef012345" || len(info.Parents) != 2 || len(info.Tags) != 2 {
		t.Fatalf("revision info = %#v", info)
	}
}

func TestParseBriefDiffAcceptsDocumentedBareFilenames(t *testing.T) {
	files := parseBriefDiff("src/main.go\ndocs/file with spaces.md\n")
	if len(files) != 2 || files[0].Path != "docs/file with spaces.md" || files[1].Path != "src/main.go" || files[0].Status != "M" {
		t.Fatalf("files = %#v", files)
	}
}
