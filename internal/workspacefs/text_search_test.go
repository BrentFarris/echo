package workspacefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTextReplacementDollarTokens(t *testing.T) {
	matches := findTextMatches("camera", contentRevision([]byte("camera")), regexp.MustCompile("camera"), "$$&:$&:$$", 1)
	if len(matches) != 1 || matches[0].replacement != "$&:camera:$" {
		t.Fatalf("unexpected dollar expansion: %#v", matches)
	}
}

func waitForTextSearch(t *testing.T, service *Service, workspaceID string, request TextSearchRequest) TextSearchResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, err := service.SearchText(context.Background(), workspaceID, request)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Indexing {
			return result
		}
		if time.Now().After(deadline) {
			t.Fatal("workspace text search index did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTextSearchOptionsGlobsAndReplacementPreview(t *testing.T) {
	service, workspaceID, rootPath, _ := newTestService(t)
	t.Cleanup(service.Close)
	if err := os.MkdirAll(filepath.Join(rootPath, "src", "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"src/main.go":             "var cameraPosition = 1\nvar CAMERAPOSITION = 2\nvar cameraPositions = 3\n",
		"src/generated/output.go": "var cameraPosition = 4\n",
		"src/notes.txt":           "cameraPosition\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(rootPath, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootPath, "src", "binary.go"), []byte{'x', 0, 'y'}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := waitForTextSearch(t, service, workspaceID, TextSearchRequest{
		Query: "camera(Position)", Replacement: "lens$1-$&-$$", Regex: true, WholeWord: true,
		Include: []string{"**/*.go"}, Exclude: []string{"**/generated/**"},
	})
	if result.MatchCount != 2 || len(result.Files) != 1 || result.Files[0].Ref.Path != "src/main.go" {
		t.Fatalf("unexpected text search result: %#v", result)
	}
	if result.Files[0].Matches[0].ReplacementPreview != "lensPosition-cameraPosition-$" {
		t.Fatalf("unexpected replacement preview: %#v", result.Files[0].Matches[0])
	}
	if result.Files[0].Matches[0].Line != 1 || result.Files[0].Matches[0].Column != 5 {
		t.Fatalf("unexpected match location: %#v", result.Files[0].Matches[0])
	}

	caseSensitive := waitForTextSearch(t, service, workspaceID, TextSearchRequest{
		Query: "cameraPosition", CaseSensitive: true, WholeWord: true, Include: []string{"*.go"},
	})
	if caseSensitive.MatchCount != 2 {
		t.Fatalf("case-sensitive whole-word search returned %d matches", caseSensitive.MatchCount)
	}
}

func TestTextSearchUsesUnsavedOverlay(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	t.Cleanup(service.Close)
	ref := FileRef{RootID: root.ID, Path: "main.go"}
	if err := os.WriteFile(filepath.Join(rootPath, "main.go"), []byte("package main\n// saved text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Read(workspaceID, ref)
	if err != nil {
		t.Fatal(err)
	}
	result := waitForTextSearch(t, service, workspaceID, TextSearchRequest{
		Query:    "unsaved text",
		Overlays: []TextSearchOverlay{{Ref: ref, Revision: snapshot.Revision, Content: "package main\n// unsaved text\n"}},
	})
	if result.MatchCount != 1 || !result.Files[0].Overlay || result.Files[0].Revision != snapshot.Revision {
		t.Fatalf("overlay was not authoritative: %#v", result)
	}
}

func TestTextReplaceMatchFileAllAndDirtyOverlay(t *testing.T) {
	service, workspaceID, rootPath, root := newTestService(t)
	t.Cleanup(service.Close)
	refs := []FileRef{{RootID: root.ID, Path: "one.txt"}, {RootID: root.ID, Path: "two.txt"}}
	for index, ref := range refs {
		content := "alpha alpha\n"
		if index == 1 {
			content = "alpha\n"
		}
		if err := os.WriteFile(filepath.Join(rootPath, ref.Path), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	searchRequest := TextSearchRequest{Query: "alpha", Replacement: "beta"}
	result := waitForTextSearch(t, service, workspaceID, searchRequest)
	first := result.Files[0]
	response, err := service.ReplaceText(context.Background(), workspaceID, TextReplaceRequest{
		Search: searchRequest, Scope: "match",
		Targets: []TextReplaceTarget{{Ref: first.Ref, Revision: first.Revision, ContentRevision: first.ContentRevision, MatchIDs: []string{first.Matches[0].ID}}},
	})
	if err != nil || len(response.Updated) != 1 {
		t.Fatalf("replace one: %#v %v", response, err)
	}
	one, _ := os.ReadFile(filepath.Join(rootPath, "one.txt"))
	if string(one) != "beta alpha\n" {
		t.Fatalf("replace one wrote %q", one)
	}
	result = waitForTextSearch(t, service, workspaceID, searchRequest)
	var oneResult TextSearchFileResult
	for _, file := range result.Files {
		if file.Ref.Path == "one.txt" {
			oneResult = file
		}
	}
	response, err = service.ReplaceText(context.Background(), workspaceID, TextReplaceRequest{
		Search: searchRequest, Scope: "file",
		Targets: []TextReplaceTarget{{Ref: oneResult.Ref, Revision: oneResult.Revision, ContentRevision: oneResult.ContentRevision}},
	})
	if err != nil || len(response.Updated) != 1 {
		t.Fatalf("replace file: %#v %v", response, err)
	}
	one, _ = os.ReadFile(filepath.Join(rootPath, "one.txt"))
	if string(one) != "beta beta\n" {
		t.Fatalf("replace file wrote %q", one)
	}

	secondSnapshot, err := service.Read(workspaceID, refs[1])
	if err != nil {
		t.Fatal(err)
	}
	overlay := TextSearchOverlay{
		Ref: refs[1], Revision: secondSnapshot.Revision,
		Content: "// unrelated dirty edit\nalpha\n", HasBOM: secondSnapshot.HasBOM,
	}
	searchRequest.Overlays = []TextSearchOverlay{overlay}
	result = waitForTextSearch(t, service, workspaceID, searchRequest)
	targets := make([]TextReplaceTarget, 0, len(result.Files))
	for _, file := range result.Files {
		targets = append(targets, TextReplaceTarget{Ref: file.Ref, Revision: file.Revision, ContentRevision: file.ContentRevision})
	}
	response, err = service.ReplaceText(context.Background(), workspaceID, TextReplaceRequest{
		Search: searchRequest, Scope: "all", Targets: targets,
	})
	if err != nil || len(response.Updated) != 1 {
		t.Fatalf("replace all: %#v %v", response, err)
	}
	two, _ := os.ReadFile(filepath.Join(rootPath, "two.txt"))
	if string(two) != "// unrelated dirty edit\nbeta\n" {
		t.Fatalf("dirty overlay was not saved with replacement: %q", two)
	}
	foundOverlayContent := false
	for _, update := range response.Updated {
		if update.Ref.Path == "two.txt" && strings.Contains(update.Content, "unrelated dirty edit") {
			foundOverlayContent = true
		}
	}
	if !foundOverlayContent {
		t.Fatalf("replace response omitted updated overlay content: %#v", response)
	}
}

func TestTextSearchCapsResultsSkipsOversizedFilesAndCancels(t *testing.T) {
	service, workspaceID, rootPath, _ := newTestService(t)
	t.Cleanup(service.Close)
	if err := os.WriteFile(filepath.Join(rootPath, "many.txt"), []byte(strings.Repeat("x ", maximumTextSearchMatches+5)), 0o644); err != nil {
		t.Fatal(err)
	}
	oversized, err := os.Create(filepath.Join(rootPath, "oversized.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := oversized.Truncate(MaxEditableBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := oversized.Close(); err != nil {
		t.Fatal(err)
	}
	skipped := waitForTextSearch(t, service, workspaceID, TextSearchRequest{Query: "oversized"})
	if skipped.MatchCount != 0 || skipped.FilesSkipped == 0 {
		t.Fatalf("oversized text file was not skipped: %#v", skipped)
	}
	result := waitForTextSearch(t, service, workspaceID, TextSearchRequest{Query: "x"})
	if result.MatchCount != maximumTextSearchMatches || !result.Truncated {
		t.Fatalf("result cap was not applied: matches=%d truncated=%v", result.MatchCount, result.Truncated)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.SearchText(cancelled, workspaceID, TextSearchRequest{Query: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search returned %v", err)
	}
}

func TestTextReplacePreflightsAllRevisions(t *testing.T) {
	service, workspaceID, rootPath, _ := newTestService(t)
	t.Cleanup(service.Close)
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte("alpha\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	request := TextSearchRequest{Query: "alpha", Replacement: "beta"}
	result := waitForTextSearch(t, service, workspaceID, request)
	targets := make([]TextReplaceTarget, 0, len(result.Files))
	for _, file := range result.Files {
		targets = append(targets, TextReplaceTarget{Ref: file.Ref, Revision: file.Revision, ContentRevision: file.ContentRevision})
	}
	if err := os.WriteFile(filepath.Join(rootPath, "two.txt"), []byte("external alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := service.ReplaceText(context.Background(), workspaceID, TextReplaceRequest{Search: request, Scope: "all", Targets: targets})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected search conflict, got %v", err)
	}
	one, _ := os.ReadFile(filepath.Join(rootPath, "one.txt"))
	if string(one) != "alpha\n" {
		t.Fatalf("preflight conflict allowed an earlier write: %q", one)
	}
}
