package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTaskTestService(t *testing.T) (*SystemService, string, string) {
	t.Helper()
	root := t.TempDir()
	workspacePath := filepath.Join(root, "project")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return service, state.ActiveWorkspaceID, workspacePath
}

func TestWorkspaceTaskCRUDAndStableFile(t *testing.T) {
	service, workspaceID, workspacePath := newTaskTestService(t)
	empty, err := service.LoadTaskBoard(workspaceID)
	if err != nil {
		t.Fatalf("load empty board: %v", err)
	}
	if len(empty.Tasks) != 0 || !strings.HasSuffix(empty.StoragePath, "/.echo/tasks.json") {
		t.Fatalf("unexpected empty board: %#v", empty)
	}

	board, err := service.CreateWorkspaceTask(workspaceID, TaskInput{
		Title:              "  Ship backlog  ",
		Details:            "  Keep it mergeable.  ",
		AcceptanceCriteria: []string{"  Stored on disk  ", ""},
		Priority:           "p0",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if len(board.Tasks) != 1 {
		t.Fatalf("expected one task, got %#v", board.Tasks)
	}
	task := board.Tasks[0]
	if task.Title != "Ship backlog" || task.Priority != "P0" || task.Details != "Keep it mergeable." {
		t.Fatalf("unexpected task: %#v", task)
	}

	board, err = service.UpdateWorkspaceTask(workspaceID, task.ID, TaskInput{
		Title:              "Ship task board",
		Details:            "Updated",
		AcceptanceCriteria: []string{"Works"},
		Priority:           "P1",
	}, task.UpdatedAt)
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	task = board.Tasks[0]
	if task.Title != "Ship task board" || task.Priority != "P1" {
		t.Fatalf("unexpected updated task: %#v", task)
	}
	if _, err := service.MoveWorkspaceTask(workspaceID, task.ID, "P2", "stale"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected stale update rejection, got %v", err)
	}

	board, err = service.MoveWorkspaceTask(workspaceID, task.ID, "P2", task.UpdatedAt)
	if err != nil {
		t.Fatalf("move task: %v", err)
	}
	task = board.Tasks[0]
	board, err = service.SetWorkspaceTaskCompleted(workspaceID, task.ID, true, task.UpdatedAt)
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	task = board.Tasks[0]
	if !task.Completed || task.CompletedAt == "" {
		t.Fatalf("expected completed task, got %#v", task)
	}
	activeData, err := os.ReadFile(filepath.Join(workspacePath, ".echo", workspaceTaskFile))
	if err != nil {
		t.Fatal(err)
	}
	doneData, err := os.ReadFile(filepath.Join(workspacePath, ".echo", workspaceTaskDoneFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(activeData), task.ID) || !strings.Contains(string(doneData), task.ID) {
		t.Fatalf("expected completed task only in %s", workspaceTaskDoneFile)
	}
	board, err = service.SetWorkspaceTaskCompleted(workspaceID, task.ID, false, task.UpdatedAt)
	if err != nil {
		t.Fatalf("reopen task: %v", err)
	}
	task = board.Tasks[0]
	if task.Completed || task.CompletedAt != "" {
		t.Fatalf("expected reopened task, got %#v", task)
	}
	activeData, _ = os.ReadFile(filepath.Join(workspacePath, ".echo", workspaceTaskFile))
	doneData, _ = os.ReadFile(filepath.Join(workspacePath, ".echo", workspaceTaskDoneFile))
	if !strings.Contains(string(activeData), task.ID) || strings.Contains(string(doneData), task.ID) {
		t.Fatalf("expected reopened task only in %s", workspaceTaskFile)
	}

	data, err := os.ReadFile(filepath.Join(workspacePath, ".echo", workspaceTaskFile))
	if err != nil {
		t.Fatalf("read task file: %v", err)
	}
	var stored struct {
		Version int                            `json:"version"`
		Tasks   map[string]storedWorkspaceTask `json:"tasks"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode task file: %v", err)
	}
	if stored.Version != 1 || len(stored.Tasks) != 1 || stored.Tasks[task.ID].Title != task.Title {
		t.Fatalf("unexpected stored file: %s", data)
	}

	board, err = service.DeleteWorkspaceTask(workspaceID, task.ID, task.UpdatedAt)
	if err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if len(board.Tasks) != 0 {
		t.Fatalf("expected empty board after delete, got %#v", board.Tasks)
	}
}

func TestWorkspaceTaskMutationPreservesExternalTasksAndMalformedFile(t *testing.T) {
	service, workspaceID, workspacePath := newTaskTestService(t)
	board, err := service.CreateWorkspaceTask(workspaceID, TaskInput{Title: "Original", Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	original := board.Tasks[0]
	path := filepath.Join(workspacePath, ".echo", workspaceTaskFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file workspaceTaskFileData
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(timeRFC3339Nano)
	file.Tasks["external-task"] = storedWorkspaceTask{
		Title:     "Added outside Echo",
		Priority:  "P2",
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, _ = json.MarshalIndent(file, "", "  ")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	board, err = service.MoveWorkspaceTask(workspaceID, original.ID, "P0", original.UpdatedAt)
	if err != nil {
		t.Fatalf("move after external addition: %v", err)
	}
	if len(board.Tasks) != 2 {
		t.Fatalf("expected external task to be preserved, got %#v", board.Tasks)
	}

	malformed := []byte("{ merge conflict")
	if err := os.WriteFile(path, malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWorkspaceTask(workspaceID, TaskInput{Title: "Must not overwrite", Priority: "P1"}); err == nil {
		t.Fatal("expected malformed task file to be rejected")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(malformed) {
		t.Fatalf("malformed file was overwritten: %q", after)
	}
}

func TestWorkspaceTaskGitIgnoredAndConversion(t *testing.T) {
	service, workspaceID, workspacePath := newTaskTestService(t)
	if err := os.WriteFile(filepath.Join(workspacePath, ".gitignore"), []byte(".echo/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	board, err := service.CreateWorkspaceTask(workspaceID, TaskInput{
		Title:              "Convert me",
		Details:            "Implement it",
		AcceptanceCriteria: []string{"It works"},
		Priority:           "P0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !board.GitIgnored {
		t.Fatal("expected ignored task file warning")
	}
	if !board.DoneGitIgnored {
		t.Fatal("expected ignored completed task file warning")
	}
	task := board.Tasks[0]
	conversion, err := service.CreateKanbanCardFromTask(workspaceID, task.ID, task.Title, task.Details, task.AcceptanceCriteria, task.UpdatedAt)
	if err != nil {
		t.Fatalf("convert task: %v", err)
	}
	if len(conversion.Kanban.Ready) != 1 || len(conversion.Tasks.Tasks) != 1 || !conversion.Tasks.Tasks[0].Completed {
		t.Fatalf("unexpected conversion: %#v", conversion)
	}
	activeData, _ := os.ReadFile(filepath.Join(workspacePath, ".echo", workspaceTaskFile))
	doneData, _ := os.ReadFile(filepath.Join(workspacePath, ".echo", workspaceTaskDoneFile))
	if strings.Contains(string(activeData), task.ID) || !strings.Contains(string(doneData), task.ID) {
		t.Fatal("converted task was not moved to the completed task file")
	}
}

func TestWorkspaceTaskConversionRunningFailureLeavesTaskOpen(t *testing.T) {
	service, workspaceID, _ := newTaskTestService(t)
	board, err := service.CreateWorkspaceTask(workspaceID, TaskInput{Title: "Wait", Priority: "P1"})
	if err != nil {
		t.Fatal(err)
	}
	task := board.Tasks[0]
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.chatMu.Lock()
	service.kanbanRuns[workspaceID] = cancel
	service.chatMu.Unlock()

	if _, err := service.CreateKanbanCardFromTask(workspaceID, task.ID, task.Title, "Details", []string{"Done"}, task.UpdatedAt); err == nil {
		t.Fatal("expected running conversion failure")
	}
	after, err := service.LoadTaskBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Tasks[0].Completed {
		t.Fatal("failed conversion completed the task")
	}
	kanban, _ := service.LoadKanbanBoard(workspaceID)
	if len(kanban.Ready) != 0 {
		t.Fatalf("failed conversion created a card: %#v", kanban.Ready)
	}
}

func TestWorkspaceTasksUseFirstAvailableFolder(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(first)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := state.ActiveWorkspaceID
	if _, err := service.AddWorkspaceFolder(workspaceID, second); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWorkspaceTask(workspaceID, TaskInput{Title: "Primary", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first, ".echo", workspaceTaskFile)); err != nil {
		t.Fatalf("expected first folder task file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, ".echo", workspaceTaskFile)); !os.IsNotExist(err) {
		t.Fatalf("expected no task file in second folder, got %v", err)
	}

	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateWorkspaceTask(workspaceID, TaskInput{Title: "Fallback", Priority: "P2"}); err != nil {
		t.Fatalf("create in first available folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, ".echo", workspaceTaskFile)); err != nil {
		t.Fatalf("expected fallback task file: %v", err)
	}
}

func TestWorkspaceTasksRejectSymlinkedEchoDirectory(t *testing.T) {
	service, workspaceID, workspacePath := newTaskTestService(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspacePath, ".echo")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := service.CreateWorkspaceTask(workspaceID, TaskInput{Title: "Unsafe", Priority: "P1"}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestWorkspaceTasksMigrateLegacyCompletedRecords(t *testing.T) {
	service, workspaceID, workspacePath := newTaskTestService(t)
	cache := filepath.Join(workspacePath, ".echo")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(timeRFC3339Nano)
	legacy := workspaceTaskFileData{
		Version: workspaceTaskSchema,
		Tasks: map[string]storedWorkspaceTask{
			"active": {
				Title:     "Active",
				Priority:  "P1",
				CreatedAt: now,
				UpdatedAt: now,
			},
			"done": {
				Title:       "Done",
				Priority:    "P2",
				Completed:   true,
				CreatedAt:   now,
				UpdatedAt:   now,
				CompletedAt: now,
			},
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, workspaceTaskFile), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	board, err := service.LoadTaskBoard(workspaceID)
	if err != nil {
		t.Fatalf("load and migrate tasks: %v", err)
	}
	if len(board.Tasks) != 2 {
		t.Fatalf("expected both migrated tasks, got %#v", board.Tasks)
	}
	activeData, _ := os.ReadFile(filepath.Join(cache, workspaceTaskFile))
	doneData, _ := os.ReadFile(filepath.Join(cache, workspaceTaskDoneFile))
	if strings.Contains(string(activeData), `"done"`) || !strings.Contains(string(doneData), `"done"`) {
		t.Fatalf("completed legacy task was not split correctly\nactive=%s\ndone=%s", activeData, doneData)
	}
}
