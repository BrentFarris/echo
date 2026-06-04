package services

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/tools"
)

func TestWorkspaceChangeReviewTracksChatToolChanges(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_edit_text","arguments":"{\"path\":\"notes.txt\",\"oldText\":\"before\\n\",\"newText\":\"after\\n\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Updated notes."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))

	if _, err := service.SendChatMessage(workspaceID, "Update notes"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if review.FileCount != 1 || review.ChangeCount != 1 {
		t.Fatalf("expected one reviewed change, got %#v", review)
	}
	file := review.Files[0]
	if file.Path != "notes.txt" || file.Operation != tools.FileChangeEdited || !strings.Contains(file.Diff, "-before") || !strings.Contains(file.Diff, "+after") {
		t.Fatalf("unexpected reviewed file: %#v", file)
	}
	if len(file.Sources) != 1 || file.Sources[0].Type != "chat" || file.Sources[0].ToolName != "filesystem_edit_text" {
		t.Fatalf("unexpected sources: %#v", file.Sources)
	}
}

func TestWorkspaceChangeReviewDoesNotTrackDeniedPlanModeTool(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_create_text","arguments":"{\"path\":\"blocked.txt\",\"content\":\"nope\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"I cannot edit in plan mode."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Create a file", true); err != nil {
		t.Fatalf("send plan-mode chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if review.FileCount != 0 || review.ChangeCount != 0 {
		t.Fatalf("expected no plan-mode changes, got %#v", review)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("plan-mode tool should not create file, stat error: %v", err)
	}
}

func TestWorkspaceChangeReviewTracksKanbanToolChanges(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_create_text","arguments":"{\"path\":\"feature.txt\",\"content\":\"done\\n\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Implemented feature."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Create feature file", Description: "Write a file", AcceptanceCriteria: []string{"feature.txt exists"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start kanban: %v", err)
	}
	waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})

	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if review.FileCount != 1 || review.Files[0].Path != "feature.txt" || review.Files[0].Operation != tools.FileChangeCreated {
		t.Fatalf("unexpected kanban review: %#v", review)
	}
	source := review.Files[0].Sources[0]
	if source.Type != "kanban" || source.CardID != "card-1" || source.CardTitle != "Create feature file" {
		t.Fatalf("unexpected kanban source: %#v", source)
	}
}

func TestWorkspaceChangeReviewSkipsWorkspaceGitignoredFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\nignored/\n!important.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	service.recordToolFileChanges(workspaceID, WorkspaceChangeSource{Type: "kanban", CardID: "card-1", ToolName: "filesystem_create_text"}, []tools.FileChange{
		textCreateChange("visible.txt", "visible\n"),
		textCreateChange("debug.log", "ignored log\n"),
		textCreateChange("ignored/generated.txt", "ignored directory\n"),
		textCreateChange("important.log", "negated\n"),
	})

	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if review.FileCount != 2 || review.ChangeCount != 2 {
		t.Fatalf("expected only non-ignored changes, got %#v", review)
	}
	files := map[string]WorkspaceChangedFile{}
	for _, file := range review.Files {
		files[file.Path] = file
	}
	if files["visible.txt"].Operation != tools.FileChangeCreated || files["important.log"].Operation != tools.FileChangeCreated {
		t.Fatalf("expected visible and negated files in review, got %#v", review.Files)
	}
	if _, ok := files["debug.log"]; ok {
		t.Fatalf("expected ignored log file to be skipped, got %#v", review.Files)
	}
	if _, ok := files["ignored/generated.txt"]; ok {
		t.Fatalf("expected ignored directory file to be skipped, got %#v", review.Files)
	}
}

func TestWorkspaceChangeReviewTracksInlineToolChangesAndAffectedPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_edit_text","arguments":"{\"path\":\"notes.txt\",\"oldText\":\"before\\n\",\"newText\":\"after\\n\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			writeSSE(t, w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))

	response, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{
		RequestID:        "inline-1",
		FilePath:         "notes.txt",
		Prompt:           "Change before to after.",
		CursorToken:      "before",
		CursorLineText:   "before",
		FocusSubstring:   "before\n",
		ContextSubstring: "before\n",
	})
	if err != nil {
		t.Fatalf("submit inline prompt: %v", err)
	}
	if strings.Join(response.AffectedPaths, ",") != "notes.txt" {
		t.Fatalf("expected affected path from tracker, got %#v", response.AffectedPaths)
	}
	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if review.FileCount != 1 || review.Files[0].Sources[0].Type != "inline" || review.Files[0].Sources[0].RequestID != "inline-1" {
		t.Fatalf("unexpected inline review: %#v", review)
	}
}

func TestWorkspaceChangeReviewCanBeClearedAndIsRuntimeOnly(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	service.recordToolFileChanges(workspaceID, WorkspaceChangeSource{Type: "chat", ToolName: "filesystem_create_text"}, []tools.FileChange{{
		Path:      "new.txt",
		Operation: tools.FileChangeCreated,
		After: &tools.FileSnapshot{
			Path:          "new.txt",
			Exists:        true,
			Bytes:         int64(len("hello\n")),
			SHA256:        "hash",
			Text:          "hello\n",
			TextAvailable: true,
		},
	}})

	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if review.FileCount != 1 || review.ChangeCount != 1 {
		t.Fatalf("expected seeded change, got %#v", review)
	}
	cleared, err := service.ClearWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("clear review: %v", err)
	}
	if cleared.FileCount != 0 || cleared.ChangeCount != 0 {
		t.Fatalf("expected cleared review, got %#v", cleared)
	}

	service.recordToolFileChanges(workspaceID, WorkspaceChangeSource{Type: "chat", ToolName: "filesystem_create_text"}, []tools.FileChange{{
		Path:      "new.txt",
		Operation: tools.FileChangeCreated,
		After: &tools.FileSnapshot{
			Path:          "new.txt",
			Exists:        true,
			Bytes:         int64(len("hello\n")),
			SHA256:        "hash",
			Text:          "hello\n",
			TextAvailable: true,
		},
	}})
	reloaded := NewSystemServiceWithStorePath(storePath)
	runtimeOnly, err := reloaded.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load reloaded review: %v", err)
	}
	if runtimeOnly.FileCount != 0 || runtimeOnly.ChangeCount != 0 {
		data, _ := json.Marshal(runtimeOnly)
		t.Fatalf("expected runtime-only review after reload, got %s", data)
	}
}

func TestWorkspaceChangeReviewConsolidatesNetFileDiff(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	root := t.TempDir()
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	service.recordToolFileChanges(workspaceID, WorkspaceChangeSource{Type: "chat", ToolName: "filesystem_create_text"}, []tools.FileChange{{
		Path:      "notes.txt",
		Operation: tools.FileChangeCreated,
		After: &tools.FileSnapshot{
			Path:          "notes.txt",
			Exists:        true,
			Bytes:         int64(len("first\n")),
			SHA256:        "first",
			Text:          "first\n",
			TextAvailable: true,
		},
	}})
	service.recordToolFileChanges(workspaceID, WorkspaceChangeSource{Type: "kanban", CardID: "card-1", ToolName: "filesystem_edit_text"}, []tools.FileChange{{
		Path:      "notes.txt",
		Operation: tools.FileChangeEdited,
		Before: &tools.FileSnapshot{
			Path:          "notes.txt",
			Exists:        true,
			Bytes:         int64(len("first\n")),
			SHA256:        "first",
			Text:          "first\n",
			TextAvailable: true,
		},
		After: &tools.FileSnapshot{
			Path:          "notes.txt",
			Exists:        true,
			Bytes:         int64(len("second\n")),
			SHA256:        "second",
			Text:          "second\n",
			TextAvailable: true,
		},
	}})

	review, err := service.LoadWorkspaceChangeReview(workspaceID)
	if err != nil {
		t.Fatalf("load review: %v", err)
	}
	if review.FileCount != 1 || review.ChangeCount != 2 {
		t.Fatalf("expected consolidated file with two changes, got %#v", review)
	}
	file := review.Files[0]
	if file.Operation != tools.FileChangeCreated || !strings.Contains(file.Diff, "+second") || len(file.Sources) != 2 {
		t.Fatalf("unexpected consolidated file: %#v", file)
	}
}

func textCreateChange(path string, content string) tools.FileChange {
	return tools.FileChange{
		Path:      path,
		Operation: tools.FileChangeCreated,
		After: &tools.FileSnapshot{
			Path:          path,
			Exists:        true,
			Bytes:         int64(len(content)),
			SHA256:        path + "-hash",
			Text:          content,
			TextAvailable: true,
		},
	}
}
