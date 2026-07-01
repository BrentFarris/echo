package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func TestWorkspaceSkillCreateSearchReadAndUpdate(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	created, err := upsertWorkspaceSkill(ctx, workspace, tools.WorkspaceSkillRecordRequest{
		Action:      "upsert",
		Folder:      "workspace",
		Name:        "file-database",
		Description: "How the cached workspace file database supports fast file lookup.",
		Triggers:    []string{"file search", "workspace database"},
		Body:        "# File database\n\nUse the cache after validating its freshness.",
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	if !created.Created || created.Skill == nil || created.Skill.Revision == "" {
		t.Fatalf("unexpected create response: %#v", created)
	}
	path := filepath.Join(root, ".echo", "skills", "file-database", "SKILL.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat skill: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != workspaceCacheFilePermission {
		t.Fatalf("expected skill permissions %o, got %o", workspaceCacheFilePermission, info.Mode().Perm())
	}

	search, err := searchWorkspaceSkills(ctx, workspace, tools.WorkspaceSkillSearchRequest{
		Query: "improve workspace file search database",
		Limit: 3,
	})
	if err != nil {
		t.Fatalf("search skills: %v", err)
	}
	if len(search.Skills) != 1 || search.Skills[0].ID != "workspace/file-database" {
		t.Fatalf("unexpected search results: %#v", search)
	}

	read, err := readWorkspaceSkill(ctx, workspace, "workspace/file-database")
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if read.Body != "# File database\n\nUse the cache after validating its freshness." {
		t.Fatalf("unexpected body: %q", read.Body)
	}

	if _, err := upsertWorkspaceSkill(ctx, workspace, tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: "workspace", Name: "file-database",
		Description: read.Description, Triggers: read.Triggers, Body: read.Body,
	}); !workspaceSkillSafeErrorCode(err, "skill_revision_required") {
		t.Fatalf("expected revision requirement, got %v", err)
	}
	if _, err := upsertWorkspaceSkill(ctx, workspace, tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: "workspace", Name: "file-database",
		Description: read.Description, Triggers: read.Triggers, Body: read.Body,
		ExpectedRevision: "stale",
	}); !workspaceSkillSafeErrorCode(err, "skill_revision_conflict") {
		t.Fatalf("expected revision conflict, got %v", err)
	}

	updated, err := upsertWorkspaceSkill(ctx, workspace, tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: "workspace", Name: "file-database",
		Description: read.Description, Triggers: read.Triggers,
		Body:             read.Body + "\n\n## Verification\n\nRun focused service tests.",
		ExpectedRevision: read.Revision,
	})
	if err != nil {
		t.Fatalf("update skill: %v", err)
	}
	if updated.Skill == nil || updated.Skill.Revision == read.Revision || updated.Created {
		t.Fatalf("unexpected update response: %#v", updated)
	}

	unchanged, err := upsertWorkspaceSkill(ctx, workspace, tools.WorkspaceSkillRecordRequest{
		Action: "upsert", Folder: "workspace", Name: "file-database",
		Description: updated.Skill.Description, Triggers: updated.Skill.Triggers, Body: updated.Skill.Body,
		ExpectedRevision: updated.Skill.Revision,
	})
	if err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	if !unchanged.Unchanged || unchanged.Skill == nil || unchanged.Skill.Revision != updated.Skill.Revision {
		t.Fatalf("unexpected no-op response: %#v", unchanged)
	}
}

func TestWorkspaceSkillCatalogRanksMetadataAndSpansFolders(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	workspace := Workspace{
		ID: "workspace-1",
		Folders: []WorkspaceFolder{
			{ID: "folder-1", Label: "app", Path: first},
			{ID: "folder-2", Label: "docs", Path: second},
		},
	}
	ctx := context.Background()
	for _, request := range []tools.WorkspaceSkillRecordRequest{
		{Action: "upsert", Folder: "app", Name: "chat-streaming", Description: "Chat completion stream lifecycle.", Triggers: []string{"streaming chat"}, Body: "# Chat streaming"},
		{Action: "upsert", Folder: "docs", Name: "file-database", Description: "Cached file index and lookup behavior.", Triggers: []string{"workspace file search"}, Body: "# File database"},
	} {
		if _, err := upsertWorkspaceSkill(ctx, workspace, request); err != nil {
			t.Fatalf("create %s: %v", request.Name, err)
		}
	}

	result, err := searchWorkspaceSkills(ctx, workspace, tools.WorkspaceSkillSearchRequest{
		Query: "speed up workspace file search",
		Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) == 0 || result.Skills[0].ID != "docs/file-database" {
		t.Fatalf("expected file database first, got %#v", result.Skills)
	}
	filtered, err := searchWorkspaceSkills(ctx, workspace, tools.WorkspaceSkillSearchRequest{
		Query:  "chat streaming",
		Folder: "app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Skills) != 1 || filtered.Skills[0].ID != "app/chat-streaming" {
		t.Fatalf("unexpected filtered results: %#v", filtered.Skills)
	}
}

func TestWorkspaceSkillCatalogSkipsMalformedAndOversizedFiles(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	malformedDir := filepath.Join(root, ".echo", "skills", "malformed")
	if err := os.MkdirAll(malformedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedDir, "SKILL.md"), []byte("not frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	oversizedDir := filepath.Join(root, ".echo", "skills", "oversized")
	if err := os.MkdirAll(oversizedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oversizedDir, "SKILL.md"), []byte(strings.Repeat("x", workspaceSkillMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := searchWorkspaceSkills(context.Background(), workspace, tools.WorkspaceSkillSearchRequest{Query: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Skills) != 0 || len(result.Warnings) != 2 {
		t.Fatalf("expected malformed skills to become warnings, got %#v", result)
	}
	if _, err := readWorkspaceSkill(context.Background(), workspace, "workspace/../outside"); !workspaceSkillSafeErrorCode(err, "invalid_arguments") {
		t.Fatalf("expected unsafe id rejection, got %v", err)
	}
}

func TestWorkspaceSkillToolWriteStaysOutOfProjectChangesAndFileSearch(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]any{
		"action":      "upsert",
		"folder":      "workspace",
		"name":        "hidden-skill",
		"description": "Durable guidance that remains in Echo's local cache.",
		"triggers":    []string{"hidden skill"},
		"body":        "# Hidden skill\n\nThis should not appear as a project file.",
	})
	if err != nil {
		t.Fatal(err)
	}
	execution := service.executeTrackedToolCall(context.Background(), workspace, llm.Settings{}, llm.ToolCall{
		ID: "call_skill",
		Function: llm.FunctionCall{
			Name:      "workspace_skill_record",
			Arguments: string(arguments),
		},
	}, WorkspaceChangeSource{Type: "chat"}, nil)
	if !execution.Result.Success || len(execution.Changes) != 0 {
		t.Fatalf("unexpected skill tool execution: %#v", execution)
	}
	if _, err := os.Stat(filepath.Join(root, ".echo", "skills", "hidden-skill", "SKILL.md")); err != nil {
		t.Fatalf("expected skill file: %v", err)
	}
	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if review.ChangeCount != 0 || review.FileCount != 0 {
		t.Fatalf("skill write leaked into project changes: %#v", review)
	}
	files, err := service.SearchWorkspaceFiles(workspaceID, "SKILL", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Entries) != 0 {
		t.Fatalf("skill file leaked into workspace search: %#v", files.Entries)
	}
}

func TestWorkspaceSkillRejectsSymlinkedSkillDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation can require elevated privileges on Windows")
	}
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureWorkspaceFolderCache(workspaceID, workspace.Folders[0]); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".echo", "skills", "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readWorkspaceSkill(context.Background(), workspace, "workspace/linked"); err == nil {
		t.Fatal("expected symlinked skill directory to be rejected")
	}
}

func TestWorkspaceSkillsPromptSurfacesMetadataWithoutBody(t *testing.T) {
	prompt := workspaceSkillsPrompt("Base prompt.", []tools.WorkspaceSkillSummary{{
		ID:          "workspace/file-database",
		Description: "Fast file lookup.",
		Triggers:    []string{"file search"},
	}}, true)
	for _, expected := range []string{"workspace/file-database", "Fast file lookup.", "file search", "workspace_skill_read", "workspace_skill_record"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to contain %q, got %q", expected, prompt)
		}
	}
	if strings.Contains(prompt, "secret skill body") {
		t.Fatalf("prompt unexpectedly included a skill body: %q", prompt)
	}
}

func TestChatSurfacesMatchingSkillMetadataWithoutBody(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Use the cached index."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	workspace, err := service.workspaceByID(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upsertWorkspaceSkill(context.Background(), workspace, tools.WorkspaceSkillRecordRequest{
		Action:      "upsert",
		Folder:      workspace.Folders[0].Label,
		Name:        "file-database",
		Description: "Cached workspace file search and indexing behavior.",
		Triggers:    []string{"workspace file search"},
		Body:        "# Private body marker\n\nImplementation detail that must not be preloaded.",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.SendChatMessage(workspaceID, "Improve workspace file search"); err != nil {
		t.Fatal(err)
	}
	waitForChatIdle(t, service, workspaceID)
	if len(captured.Messages) == 0 {
		t.Fatal("expected captured messages")
	}
	system := captured.Messages[0].Content
	for _, expected := range []string{"file-database", "Cached workspace file search and indexing behavior.", "workspace_skill_read"} {
		if !strings.Contains(system, expected) {
			t.Fatalf("expected system prompt to contain %q, got %q", expected, system)
		}
	}
	if strings.Contains(system, "Private body marker") {
		t.Fatalf("system prompt preloaded the skill body: %q", system)
	}
}

func TestChatSkillCheckpointBuffersAndFailsOpenAfterTwoReminders(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	var notesPath string
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			args := fmt.Sprintf(`{"path":%q,"oldText":"before\n","newText":"after\n"}`, notesPath)
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_edit","type":"function","function":{"name":"filesystem_edit_text","arguments":%q}}]}}]}`, args),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Hidden first completion."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		case 3:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Hidden second completion."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		case 4:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Visible final completion."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))
	notesPath = labeledTestPath(t, service, workspaceID, "notes.txt")

	if _, err := service.SendChatMessage(workspaceID, "Update notes"); err != nil {
		t.Fatal(err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if requestCount.Load() != 4 {
		t.Fatalf("expected mutation plus two reminders and fail-open, got %d requests", requestCount.Load())
	}
	assistant := session.Messages[len(session.Messages)-1].Content
	if !strings.Contains(assistant, "Visible final completion.") || !strings.Contains(assistant, workspaceSkillCheckpointWarning()) {
		t.Fatalf("expected final completion and warning, got %q", assistant)
	}
	if strings.Contains(assistant, "Hidden first completion.") || strings.Contains(assistant, "Hidden second completion.") {
		t.Fatalf("checkpoint attempts leaked into visible output: %q", assistant)
	}
}

func workspaceSkillSafeErrorCode(err error, code string) bool {
	var safe tools.SafeError
	return errors.As(err, &safe) && safe.Code == code
}
