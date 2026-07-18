package services

import "testing"

func TestWorkspaceFuzzyScoreMatchesOrderedCharacters(t *testing.T) {
	for _, test := range []struct {
		query     string
		candidate string
		matched   bool
	}{
		{query: "host_test", candidate: "host_render_test.go", matched: true},
		{query: "host_test", candidate: "host_entity_test.go", matched: true},
		{query: "hrte", candidate: "host_render_test.go", matched: true},
		{query: "src/hrt", candidate: `workspace\src\host_render_test.go`, matched: true},
		{query: "test_host", candidate: "host_render_test.go", matched: false},
		{query: "host_xyz", candidate: "host_render_test.go", matched: false},
	} {
		_, matched := workspaceFuzzyScore(test.query, test.candidate)
		if matched != test.matched {
			t.Fatalf("workspaceFuzzyScore(%q, %q) matched=%v, want %v", test.query, test.candidate, matched, test.matched)
		}
	}
}

func TestSortWorkspaceFileEntriesRanksExactAndNameMatchesFirst(t *testing.T) {
	entries := []WorkspaceFileEntry{
		{Name: "render_test.go", Path: "workspace/host/render_test.go", Kind: "file"},
		{Name: "host_render_test.go", Path: "workspace/pkg/host_render_test.go", Kind: "file"},
		{Name: "host_test.go", Path: "workspace/pkg/host_test.go", Kind: "file"},
		{Name: "host_entity_test.go", Path: "workspace/pkg/host_entity_test.go", Kind: "file"},
	}

	sortWorkspaceFileEntries(entries, "host_test")

	if entries[0].Name != "host_test.go" {
		t.Fatalf("expected closest filename first, got %#v", entries)
	}
	if entries[len(entries)-1].Name != "render_test.go" {
		t.Fatalf("expected path-only match after filename matches, got %#v", entries)
	}
}

func TestSortWorkspaceFileEntriesPrefersShallowerDuplicateName(t *testing.T) {
	entries := []WorkspaceFileEntry{
		{Name: "flash_war", Path: "flashwar/flashwar/src/game/flash_war", Kind: "directory"},
		{Name: "flash_war", Path: "flashwar/src/game/flash_war", Kind: "directory"},
	}

	sortWorkspaceFileEntries(entries, "flash_war")

	if got := entries[0].Path; got != "flashwar/src/game/flash_war" {
		t.Fatalf("expected shallower duplicate-name path first, got %q", got)
	}
}
