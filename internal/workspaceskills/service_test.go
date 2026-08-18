package workspaceskills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/tools"
)

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	return New([]tools.WorkspaceRoot{{Label: "workspace", Path: root}}), root
}

func TestCreateSearchReadUpdateWorkspaceSkill(t *testing.T) {
	service, root := testService(t)
	ctx := context.Background()
	created, err := service.Upsert(ctx, tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: "workspace", Name: "file-database",
		Description: "How the workspace file database supports lookup.",
		Triggers:    []string{"file search", "File Search"}, Body: "# File database\n\nValidate cache freshness.",
	})
	if err != nil || !created.Created || created.Skill == nil || created.Skill.Revision == "" {
		t.Fatalf("create: result=%#v err=%v", created, err)
	}
	path := filepath.Join(root, ".echo", "skills", "file-database", FileName)
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	search, err := service.SearchWorkspaceSkills(ctx, tools.WorkspaceSkillSearchRequest{Query: "improve workspace file search", Limit: 3})
	if err != nil || len(search.Skills) != 1 || search.Skills[0].ID != "workspace/file-database" {
		t.Fatalf("search: result=%#v err=%v", search, err)
	}
	read, err := service.ReadWorkspaceSkill(ctx, tools.WorkspaceSkillReadRequest{ID: "workspace/file-database"})
	if err != nil || read.Body != "# File database\n\nValidate cache freshness." || len(read.Triggers) != 1 {
		t.Fatalf("read: result=%#v err=%v", read, err)
	}
	if _, err := service.Upsert(ctx, tools.WorkspaceSkillRecordRequest{Action: "upsert", Folder: "workspace", Name: "file-database", Description: read.Description, Body: read.Body}); safeCode(err) != "skill_revision_required" {
		t.Fatalf("expected revision requirement, got %v", err)
	}
	updated, err := service.Upsert(ctx, tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: "workspace", Name: "file-database", Description: read.Description,
		Triggers: read.Triggers, Body: read.Body + "\n\n## Verification\n\nRun focused tests.", ExpectedRevision: read.Revision,
	})
	if err != nil || updated.Skill == nil || updated.Skill.Revision == read.Revision {
		t.Fatalf("update: result=%#v err=%v", updated, err)
	}
}

func TestCatalogSpansFoldersAndSkipsMalformedSkills(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	service := New([]tools.WorkspaceRoot{{Label: "app", Path: first}, {Label: "docs", Path: second}})
	for _, request := range []tools.WorkspaceSkillRecordRequest{
		{Action: "upsert", Folder: "app", Name: "chat-streaming", Description: "Chat completion stream lifecycle.", Triggers: []string{"streaming chat"}, Body: "# Chat streaming"},
		{Action: "upsert", Folder: "docs", Name: "file-database", Description: "Cached file index and lookup behavior.", Triggers: []string{"workspace file search"}, Body: "# File database"},
	} {
		if _, err := service.Upsert(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	malformed := filepath.Join(first, ".echo", "skills", "malformed")
	if err := os.MkdirAll(malformed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, FileName), []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := service.SearchWorkspaceSkills(context.Background(), tools.WorkspaceSkillSearchRequest{Query: "workspace file search"})
	if err != nil || len(result.Skills) == 0 || result.Skills[0].ID != "docs/file-database" || len(result.Warnings) != 1 {
		t.Fatalf("catalog: result=%#v err=%v", result, err)
	}
	filtered, err := service.SearchWorkspaceSkills(context.Background(), tools.WorkspaceSkillSearchRequest{Query: "chat streaming", Folder: "app"})
	if err != nil || len(filtered.Skills) != 1 || filtered.Skills[0].ID != "app/chat-streaming" {
		t.Fatalf("filtered: result=%#v err=%v", filtered, err)
	}
}

func TestCreateUniqueSerializesConcurrentNameCollisions(t *testing.T) {
	service, _ := testService(t)
	const count = 8
	names := make(chan string, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.CreateUnique(context.Background(), tools.WorkspaceSkillRecordRequest{
				Action: "upsert", Folder: "workspace", Name: "chat-streaming",
				Description: "Reusable chat streaming guidance.", Body: "# Chat streaming",
			})
			if err != nil {
				errs <- err
				return
			}
			names <- result.Skill.Name
		}()
	}
	wg.Wait()
	close(names)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got := make([]string, 0, count)
	for name := range names {
		got = append(got, name)
	}
	sort.Strings(got)
	if len(got) != count || got[0] != "chat-streaming" || got[len(got)-1] != "chat-streaming-8" {
		t.Fatalf("unique names = %v", got)
	}
}

func TestRejectsUnsafeIdentityAndSymlink(t *testing.T) {
	service, root := testService(t)
	if _, err := service.ReadWorkspaceSkill(context.Background(), tools.WorkspaceSkillReadRequest{ID: "workspace/../outside"}); safeCode(err) != "invalid_arguments" {
		t.Fatalf("unsafe ID: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.MkdirAll(filepath.Join(root, ".echo", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".echo", "skills", "linked")); err != nil {
		t.Skip(err)
	}
	_, err := service.ReadWorkspaceSkill(context.Background(), tools.WorkspaceSkillReadRequest{ID: "workspace/linked"})
	if err == nil || !strings.Contains(err.Error(), "regular directory") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func safeCode(err error) string {
	var safe tools.SafeError
	if errors.As(err, &safe) {
		return safe.Code
	}
	return ""
}
