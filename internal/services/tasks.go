package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/tools"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	taskEventName         = "echo:tasks:event"
	workspaceTaskFile     = "tasks.json"
	workspaceTaskDoneFile = "tasks_done.json"
	workspaceTaskSchema   = 1
)

type WorkspaceTask struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Details            string   `json:"details,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Priority           string   `json:"priority"`
	Completed          bool     `json:"completed"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
	CompletedAt        string   `json:"completedAt,omitempty"`
}

type TaskInput struct {
	Title              string   `json:"title"`
	Details            string   `json:"details,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Priority           string   `json:"priority"`
}

type TaskBoard struct {
	WorkspaceID     string          `json:"workspaceId"`
	StoragePath     string          `json:"storagePath"`
	DoneStoragePath string          `json:"doneStoragePath"`
	GitIgnored      bool            `json:"gitIgnored"`
	DoneGitIgnored  bool            `json:"doneGitIgnored"`
	Tasks           []WorkspaceTask `json:"tasks"`
}

type TaskEvent struct {
	WorkspaceID string    `json:"workspaceId"`
	TaskID      string    `json:"taskId,omitempty"`
	Type        string    `json:"type"`
	Board       TaskBoard `json:"board"`
}

type TaskKanbanConversion struct {
	Tasks  TaskBoard   `json:"tasks"`
	Kanban KanbanBoard `json:"kanban"`
}

type storedWorkspaceTask struct {
	Title              string   `json:"title"`
	Details            string   `json:"details,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Priority           string   `json:"priority"`
	Completed          bool     `json:"completed"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
	CompletedAt        string   `json:"completedAt,omitempty"`
}

type workspaceTaskFileData struct {
	Version int                            `json:"version"`
	Tasks   map[string]storedWorkspaceTask `json:"tasks"`
}

type workspaceTaskLocation struct {
	root            string
	activePath      string
	donePath        string
	displayPath     string
	doneDisplayPath string
}

type workspaceTasksProvider struct {
	service   *SystemService
	workspace Workspace
}

func (s *SystemService) workspaceTasksProvider(workspace Workspace) tools.WorkspaceTasksProvider {
	return workspaceTasksProvider{service: s, workspace: workspace}
}

func (p workspaceTasksProvider) ListWorkspaceTasks(ctx context.Context, request tools.WorkspaceTaskListRequest) (tools.WorkspaceTaskListResponse, error) {
	if err := ctx.Err(); err != nil {
		return tools.WorkspaceTaskListResponse{}, err
	}
	p.service.taskMu.Lock()
	board, err := p.service.loadTaskBoardForWorkspace(p.workspace)
	p.service.taskMu.Unlock()
	if err != nil {
		return tools.WorkspaceTaskListResponse{}, err
	}
	result := tools.WorkspaceTaskListResponse{StoragePath: board.StoragePath, Tasks: []tools.WorkspaceTask{}}
	for _, task := range board.Tasks {
		if !request.IncludeCompleted && task.Completed {
			continue
		}
		if request.Priority != "" && task.Priority != request.Priority {
			continue
		}
		result.Tasks = append(result.Tasks, toolWorkspaceTask(task))
	}
	return result, nil
}

func (p workspaceTasksProvider) CreateWorkspaceTask(ctx context.Context, request tools.WorkspaceTaskCreateRequest) (tools.WorkspaceTaskMutationResponse, error) {
	if err := ctx.Err(); err != nil {
		return tools.WorkspaceTaskMutationResponse{}, err
	}
	input := TaskInput{
		Title:              request.Title,
		Details:            request.Details,
		AcceptanceCriteria: request.AcceptanceCriteria,
		Priority:           request.Priority,
	}
	p.service.taskMu.Lock()
	board, created, err := p.service.createWorkspaceTask(p.workspace, input)
	p.service.taskMu.Unlock()
	if err != nil {
		return tools.WorkspaceTaskMutationResponse{}, err
	}
	p.service.emitTaskEvent(TaskEvent{WorkspaceID: p.workspace.ID, TaskID: created.ID, Type: "created", Board: board})
	tasks := make([]tools.WorkspaceTask, 0, len(board.Tasks))
	for _, task := range board.Tasks {
		tasks = append(tasks, toolWorkspaceTask(task))
	}
	return tools.WorkspaceTaskMutationResponse{Created: toolWorkspaceTask(created), Tasks: tasks}, nil
}

func (p workspaceTasksProvider) ConvertTaskToKanbanCard(ctx context.Context, request tools.WorkspaceTaskConvertRequest) (tools.WorkspaceTaskConversionResponse, error) {
	if err := ctx.Err(); err != nil {
		return tools.WorkspaceTaskConversionResponse{}, err
	}
	title := request.Title
	description := request.Description
	criteria := request.AcceptanceCriteria

	p.service.taskMu.Lock()
	_, active, done, err := readWorkspaceTaskFiles(p.workspace)
	if err != nil {
		p.service.taskMu.Unlock()
		return tools.WorkspaceTaskConversionResponse{}, err
	}
	task, found := active.Tasks[request.TaskID]
	if !found {
		p.service.taskMu.Unlock()
		if _, completed := done.Tasks[request.TaskID]; completed {
			return tools.WorkspaceTaskConversionResponse{}, fmt.Errorf("completed tasks cannot be converted to kanban cards")
		}
		return tools.WorkspaceTaskConversionResponse{}, fmt.Errorf("task was not found")
	}
	if err := requireTaskRevision(task, request.ExpectedUpdatedAt); err != nil {
		p.service.taskMu.Unlock()
		return tools.WorkspaceTaskConversionResponse{}, err
	}
	if title == "" {
		title = task.Title
	}
	if description == "" {
		description = task.Details
	}
	if len(criteria) == 0 {
		criteria = append([]string(nil), task.AcceptanceCriteria...)
	}
	p.service.taskMu.Unlock()

	conversion, err := p.service.CreateKanbanCardFromTask(p.workspace.ID, request.TaskID, title, description, criteria, request.ExpectedUpdatedAt)
	if err != nil {
		return tools.WorkspaceTaskConversionResponse{}, err
	}

	var toolTask *tools.WorkspaceTask
	for i := range conversion.Tasks.Tasks {
		if conversion.Tasks.Tasks[i].ID == request.TaskID {
			t := toolWorkspaceTask(conversion.Tasks.Tasks[i])
			toolTask = &t
			break
		}
	}

	tasks := make([]tools.WorkspaceTask, 0, len(conversion.Tasks.Tasks))
	for _, t := range conversion.Tasks.Tasks {
		tasks = append(tasks, toolWorkspaceTask(t))
	}

	return tools.WorkspaceTaskConversionResponse{
		TaskID:       request.TaskID,
		Task:         toolTask,
		KanbanCardID: conversion.Kanban.Ready[0].ID,
		Tasks:        tasks,
	}, nil
}

func toolWorkspaceTask(task WorkspaceTask) tools.WorkspaceTask {
	return tools.WorkspaceTask{
		ID:                 task.ID,
		Title:              task.Title,
		Details:            task.Details,
		AcceptanceCriteria: append([]string(nil), task.AcceptanceCriteria...),
		Priority:           task.Priority,
		Completed:          task.Completed,
		CreatedAt:          task.CreatedAt,
		UpdatedAt:          task.UpdatedAt,
		CompletedAt:        task.CompletedAt,
	}
}

func (s *SystemService) LoadTaskBoard(workspaceID string) (TaskBoard, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return TaskBoard{}, err
	}
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	return s.loadTaskBoardForWorkspace(workspace)
}

func (s *SystemService) CreateWorkspaceTask(workspaceID string, input TaskInput) (TaskBoard, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return TaskBoard{}, err
	}
	s.taskMu.Lock()
	board, created, err := s.createWorkspaceTask(workspace, input)
	s.taskMu.Unlock()
	if err != nil {
		return TaskBoard{}, err
	}
	s.emitTaskEvent(TaskEvent{WorkspaceID: workspaceID, TaskID: created.ID, Type: "created", Board: board})
	return board, nil
}

func (s *SystemService) UpdateWorkspaceTask(workspaceID string, taskID string, input TaskInput, expectedUpdatedAt string) (TaskBoard, error) {
	return s.mutateWorkspaceTask(workspaceID, taskID, expectedUpdatedAt, "updated", func(task *storedWorkspaceTask, now string) error {
		normalized, err := normalizeTaskInput(input)
		if err != nil {
			return err
		}
		task.Title = normalized.Title
		task.Details = normalized.Details
		task.AcceptanceCriteria = normalized.AcceptanceCriteria
		task.Priority = normalized.Priority
		task.UpdatedAt = now
		return nil
	})
}

func (s *SystemService) MoveWorkspaceTask(workspaceID string, taskID string, priority string, expectedUpdatedAt string) (TaskBoard, error) {
	priority = normalizeTaskPriority(priority)
	if priority == "" {
		return TaskBoard{}, fmt.Errorf("priority must be P0, P1, or P2")
	}
	return s.mutateWorkspaceTask(workspaceID, taskID, expectedUpdatedAt, "moved", func(task *storedWorkspaceTask, now string) error {
		task.Priority = priority
		task.UpdatedAt = now
		return nil
	})
}

func (s *SystemService) SetWorkspaceTaskCompleted(workspaceID string, taskID string, completed bool, expectedUpdatedAt string) (TaskBoard, error) {
	eventType := "completed"
	if !completed {
		eventType = "reopened"
	}
	return s.mutateWorkspaceTask(workspaceID, taskID, expectedUpdatedAt, eventType, func(task *storedWorkspaceTask, now string) error {
		task.Completed = completed
		task.UpdatedAt = now
		if completed {
			task.CompletedAt = now
		} else {
			task.CompletedAt = ""
		}
		return nil
	})
}

func (s *SystemService) DeleteWorkspaceTask(workspaceID string, taskID string, expectedUpdatedAt string) (TaskBoard, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return TaskBoard{}, err
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return TaskBoard{}, fmt.Errorf("task id is required")
	}
	s.taskMu.Lock()
	location, active, done, err := readWorkspaceTaskFiles(workspace)
	if err == nil {
		task, found := active.Tasks[taskID]
		if !found {
			task, found = done.Tasks[taskID]
		}
		if !found {
			err = fmt.Errorf("task was not found")
		} else if err = requireTaskRevision(task, expectedUpdatedAt); err == nil {
			delete(active.Tasks, taskID)
			delete(done.Tasks, taskID)
			err = writeWorkspaceTaskFiles(location, active, done, task.Completed)
		}
	}
	var board TaskBoard
	if err == nil {
		board, err = taskBoardFromData(workspace.ID, location, active, done)
	}
	s.taskMu.Unlock()
	if err != nil {
		return TaskBoard{}, err
	}
	s.emitTaskEvent(TaskEvent{WorkspaceID: workspaceID, TaskID: taskID, Type: "deleted", Board: board})
	return board, nil
}

func (s *SystemService) CreateKanbanCardFromTask(workspaceID string, taskID string, title string, description string, acceptanceCriteria []string, expectedUpdatedAt string) (TaskKanbanConversion, error) {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	criteria := normalizeTaskCriteria(acceptanceCriteria)
	if title == "" {
		return TaskKanbanConversion{}, fmt.Errorf("title is required")
	}
	if description == "" {
		return TaskKanbanConversion{}, fmt.Errorf("description is required")
	}
	if len(criteria) == 0 {
		return TaskKanbanConversion{}, fmt.Errorf("at least one acceptance criterion is required")
	}
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return TaskKanbanConversion{}, err
	}

	s.taskMu.Lock()
	location, active, done, err := readWorkspaceTaskFiles(workspace)
	if err != nil {
		s.taskMu.Unlock()
		return TaskKanbanConversion{}, err
	}
	task, found := active.Tasks[taskID]
	if !found {
		s.taskMu.Unlock()
		if _, completed := done.Tasks[taskID]; completed {
			return TaskKanbanConversion{}, fmt.Errorf("completed tasks cannot be converted to kanban cards")
		}
		return TaskKanbanConversion{}, fmt.Errorf("task was not found")
	}
	if err := requireTaskRevision(task, expectedUpdatedAt); err != nil {
		s.taskMu.Unlock()
		return TaskKanbanConversion{}, err
	}

	s.chatMu.Lock()
	if _, running := s.kanbanRuns[workspaceID]; running {
		s.chatMu.Unlock()
		s.taskMu.Unlock()
		return TaskKanbanConversion{}, fmt.Errorf("kanban cards cannot be created while agents are running")
	}
	s.mu.Lock()
	previousCount := len(s.state.KanbanCards)
	card := KanbanCard{
		ID:                 fmt.Sprintf("card-%d", s.nextKanbanCardNumberLocked()),
		WorkspaceID:        workspaceID,
		Title:              title,
		Description:        description,
		AcceptanceCriteria: criteria,
		Lane:               KanbanLaneReady,
		Status:             KanbanLaneReady,
		ProgressTranscript: []KanbanProgressEntry{{
			Type:    "message",
			Title:   "Card created",
			Content: "Converted from backlog task " + taskID + ".",
			Status:  KanbanLaneReady,
		}},
	}
	s.state.KanbanCards = append(s.state.KanbanCards, card)
	now := time.Now().UTC().Format(timeRFC3339Nano)
	task.Completed = true
	task.CompletedAt = now
	task.UpdatedAt = now
	delete(active.Tasks, taskID)
	done.Tasks[taskID] = task
	if err := writeWorkspaceTaskFiles(location, active, done, false); err != nil {
		s.state.KanbanCards = s.state.KanbanCards[:previousCount]
		s.mu.Unlock()
		s.chatMu.Unlock()
		s.taskMu.Unlock()
		return TaskKanbanConversion{}, err
	}
	kanban := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	s.chatMu.Unlock()
	board, err := taskBoardFromData(workspaceID, location, active, done)
	if err != nil {
		s.taskMu.Unlock()
		return TaskKanbanConversion{}, err
	}
	s.taskMu.Unlock()
	s.emitTaskEvent(TaskEvent{WorkspaceID: workspaceID, TaskID: taskID, Type: "completed", Board: board})
	s.emitKanbanEvent(KanbanEvent{WorkspaceID: workspaceID, CardID: card.ID, Type: "card_created", Board: kanban})
	return TaskKanbanConversion{Tasks: board, Kanban: kanban}, nil
}

func (s *SystemService) mutateWorkspaceTask(workspaceID string, taskID string, expectedUpdatedAt string, eventType string, mutate func(*storedWorkspaceTask, string) error) (TaskBoard, error) {
	workspace, _, err := s.workspaceAndSettings(workspaceID)
	if err != nil {
		return TaskBoard{}, err
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return TaskBoard{}, fmt.Errorf("task id is required")
	}
	s.taskMu.Lock()
	location, active, done, err := readWorkspaceTaskFiles(workspace)
	if err == nil {
		task, found := active.Tasks[taskID]
		if !found {
			task, found = done.Tasks[taskID]
		}
		if !found {
			err = fmt.Errorf("task was not found")
		} else if err = requireTaskRevision(task, expectedUpdatedAt); err == nil {
			err = mutate(&task, time.Now().UTC().Format(timeRFC3339Nano))
			if err == nil {
				delete(active.Tasks, taskID)
				delete(done.Tasks, taskID)
				if task.Completed {
					done.Tasks[taskID] = task
				} else {
					active.Tasks[taskID] = task
				}
				err = writeWorkspaceTaskFiles(location, active, done, !task.Completed)
			}
		}
	}
	var board TaskBoard
	if err == nil {
		board, err = taskBoardFromData(workspaceID, location, active, done)
	}
	s.taskMu.Unlock()
	if err != nil {
		return TaskBoard{}, err
	}
	s.emitTaskEvent(TaskEvent{WorkspaceID: workspaceID, TaskID: taskID, Type: eventType, Board: board})
	return board, nil
}

func (s *SystemService) createWorkspaceTask(workspace Workspace, input TaskInput) (TaskBoard, WorkspaceTask, error) {
	input, err := normalizeTaskInput(input)
	if err != nil {
		return TaskBoard{}, WorkspaceTask{}, err
	}
	location, active, done, err := readWorkspaceTaskFiles(workspace)
	if err != nil {
		return TaskBoard{}, WorkspaceTask{}, err
	}
	id, err := newWorkspaceTaskID()
	if err != nil {
		return TaskBoard{}, WorkspaceTask{}, err
	}
	now := time.Now().UTC().Format(timeRFC3339Nano)
	active.Tasks[id] = storedWorkspaceTask{
		Title:              input.Title,
		Details:            input.Details,
		AcceptanceCriteria: input.AcceptanceCriteria,
		Priority:           input.Priority,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := writeWorkspaceTaskFiles(location, active, done, true); err != nil {
		return TaskBoard{}, WorkspaceTask{}, err
	}
	board, err := taskBoardFromData(workspace.ID, location, active, done)
	if err != nil {
		return TaskBoard{}, WorkspaceTask{}, err
	}
	for _, task := range board.Tasks {
		if task.ID == id {
			return board, task, nil
		}
	}
	return TaskBoard{}, WorkspaceTask{}, fmt.Errorf("created task was not found")
}

func (s *SystemService) loadTaskBoardForWorkspace(workspace Workspace) (TaskBoard, error) {
	location, active, done, err := readWorkspaceTaskFiles(workspace)
	if err != nil {
		return TaskBoard{}, err
	}
	return taskBoardFromData(workspace.ID, location, active, done)
}

func normalizeTaskInput(input TaskInput) (TaskInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Details = strings.TrimSpace(input.Details)
	input.Priority = normalizeTaskPriority(input.Priority)
	input.AcceptanceCriteria = normalizeTaskCriteria(input.AcceptanceCriteria)
	if input.Title == "" {
		return TaskInput{}, fmt.Errorf("title is required")
	}
	if input.Priority == "" {
		return TaskInput{}, fmt.Errorf("priority must be P0, P1, or P2")
	}
	return input, nil
}

func normalizeTaskCriteria(criteria []string) []string {
	result := make([]string, 0, len(criteria))
	for _, criterion := range criteria {
		if criterion = strings.TrimSpace(criterion); criterion != "" {
			result = append(result, criterion)
		}
	}
	return result
}

func normalizeTaskPriority(priority string) string {
	switch strings.ToUpper(strings.TrimSpace(priority)) {
	case "P0":
		return "P0"
	case "P1":
		return "P1"
	case "P2":
		return "P2"
	default:
		return ""
	}
}

func requireTaskRevision(task storedWorkspaceTask, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return fmt.Errorf("expected updated timestamp is required")
	}
	if expected != task.UpdatedAt {
		return fmt.Errorf("task changed; refresh the backlog and try again")
	}
	return nil
}

func readWorkspaceTaskFiles(workspace Workspace) (workspaceTaskLocation, workspaceTaskFileData, workspaceTaskFileData, error) {
	location, err := resolveWorkspaceTaskLocation(workspace)
	if err != nil {
		return workspaceTaskLocation{}, workspaceTaskFileData{}, workspaceTaskFileData{}, err
	}
	active, err := readWorkspaceTaskFileAt(location.activePath)
	if err != nil {
		return location, workspaceTaskFileData{}, workspaceTaskFileData{}, err
	}
	done, err := readWorkspaceTaskFileAt(location.donePath)
	if err != nil {
		return location, workspaceTaskFileData{}, workspaceTaskFileData{}, err
	}
	normalizedActive, normalizedDone := splitWorkspaceTaskData(active, done)
	if !reflect.DeepEqual(active, normalizedActive) || !reflect.DeepEqual(done, normalizedDone) {
		activeFirst := taskFileHasNewIDs(normalizedActive, active)
		if err := writeWorkspaceTaskFiles(location, normalizedActive, normalizedDone, activeFirst); err != nil {
			return location, workspaceTaskFileData{}, workspaceTaskFileData{}, fmt.Errorf("migrate workspace task files: %w", err)
		}
	}
	return location, normalizedActive, normalizedDone, nil
}

func readWorkspaceTaskFileAt(path string) (workspaceTaskFileData, error) {
	data := workspaceTaskFileData{Version: workspaceTaskSchema, Tasks: map[string]storedWorkspaceTask{}}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return data, nil
		}
		return data, fmt.Errorf("stat workspace task file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return data, fmt.Errorf("workspace task file must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return data, fmt.Errorf("open workspace task file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4*1024*1024))
	if err := decoder.Decode(&data); err != nil {
		return data, fmt.Errorf("decode workspace task file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return data, fmt.Errorf("decode workspace task file: trailing data")
	}
	if data.Version != workspaceTaskSchema {
		return data, fmt.Errorf("unsupported workspace task file version %d", data.Version)
	}
	if data.Tasks == nil {
		data.Tasks = map[string]storedWorkspaceTask{}
	}
	for id, task := range data.Tasks {
		if err := validateStoredWorkspaceTask(id, task); err != nil {
			return data, err
		}
	}
	return data, nil
}

func splitWorkspaceTaskData(active workspaceTaskFileData, done workspaceTaskFileData) (workspaceTaskFileData, workspaceTaskFileData) {
	all := make(map[string]storedWorkspaceTask, len(active.Tasks)+len(done.Tasks))
	for id, task := range active.Tasks {
		all[id] = task
	}
	for id, task := range done.Tasks {
		existing, found := all[id]
		if !found || task.UpdatedAt >= existing.UpdatedAt {
			all[id] = task
		}
	}
	active = workspaceTaskFileData{Version: workspaceTaskSchema, Tasks: map[string]storedWorkspaceTask{}}
	done = workspaceTaskFileData{Version: workspaceTaskSchema, Tasks: map[string]storedWorkspaceTask{}}
	for id, task := range all {
		if task.Completed {
			done.Tasks[id] = task
		} else {
			active.Tasks[id] = task
		}
	}
	return active, done
}

func taskFileHasNewIDs(next workspaceTaskFileData, previous workspaceTaskFileData) bool {
	for id := range next.Tasks {
		if _, existed := previous.Tasks[id]; !existed {
			return true
		}
	}
	return false
}

func resolveWorkspaceTaskLocation(workspace Workspace) (workspaceTaskLocation, error) {
	for _, folder := range workspace.Folders {
		if folder.Missing || strings.TrimSpace(folder.Path) == "" {
			continue
		}
		root, err := workspaceFolderAbsolutePath(folder)
		if err != nil {
			continue
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		cacheRoot := filepath.Join(root, workspaceCacheDirName)
		if info, err := os.Lstat(cacheRoot); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return workspaceTaskLocation{}, fmt.Errorf("workspace .echo path must be a directory and not a symlink")
			}
		} else if !os.IsNotExist(err) {
			return workspaceTaskLocation{}, err
		}
		return workspaceTaskLocation{
			root:            root,
			activePath:      filepath.Join(cacheRoot, workspaceTaskFile),
			donePath:        filepath.Join(cacheRoot, workspaceTaskDoneFile),
			displayPath:     folder.Label + "/.echo/" + workspaceTaskFile,
			doneDisplayPath: folder.Label + "/.echo/" + workspaceTaskDoneFile,
		}, nil
	}
	return workspaceTaskLocation{}, fmt.Errorf("workspace has no available folder for backlog tasks")
}

func writeWorkspaceTaskFiles(location workspaceTaskLocation, active workspaceTaskFileData, done workspaceTaskFileData, activeFirst bool) error {
	firstPath, secondPath := location.donePath, location.activePath
	firstData, secondData := done, active
	if activeFirst {
		firstPath, secondPath = location.activePath, location.donePath
		firstData, secondData = active, done
	}
	previousFirst, readErr := os.ReadFile(firstPath)
	firstExisted := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read existing workspace task file: %w", readErr)
	}
	if err := writeWorkspaceTaskFileAt(location.root, firstPath, firstData); err != nil {
		return err
	}
	if err := writeWorkspaceTaskFileAt(location.root, secondPath, secondData); err != nil {
		if firstExisted {
			_ = writeWorkspaceTaskBytes(location.root, firstPath, previousFirst)
		} else {
			_ = os.Remove(firstPath)
		}
		return err
	}
	return nil
}

func writeWorkspaceTaskFileAt(root string, path string, data workspaceTaskFileData) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspace task file: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeWorkspaceTaskBytes(root, path, encoded)
}

func writeWorkspaceTaskBytes(root string, path string, encoded []byte) error {
	if err := ensureWorkspaceCacheDirectory(filepath.Dir(path), root); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tasks-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary workspace task file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return fmt.Errorf("set workspace task file permissions: %w", err)
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary workspace task file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary workspace task file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary workspace task file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace workspace task file: %w", err)
	}
	return nil
}

func validateStoredWorkspaceTask(id string, task storedWorkspaceTask) error {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("workspace task file contains an invalid task id")
	}
	if strings.TrimSpace(task.Title) == "" {
		return fmt.Errorf("workspace task %s has no title", id)
	}
	if normalizeTaskPriority(task.Priority) == "" {
		return fmt.Errorf("workspace task %s has invalid priority", id)
	}
	if _, err := time.Parse(time.RFC3339Nano, task.CreatedAt); err != nil {
		return fmt.Errorf("workspace task %s has invalid createdAt", id)
	}
	if _, err := time.Parse(time.RFC3339Nano, task.UpdatedAt); err != nil {
		return fmt.Errorf("workspace task %s has invalid updatedAt", id)
	}
	if task.Completed && task.CompletedAt == "" {
		return fmt.Errorf("workspace task %s is completed without completedAt", id)
	}
	if task.CompletedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, task.CompletedAt); err != nil {
			return fmt.Errorf("workspace task %s has invalid completedAt", id)
		}
	}
	return nil
}

func taskBoardFromData(workspaceID string, location workspaceTaskLocation, active workspaceTaskFileData, done workspaceTaskFileData) (TaskBoard, error) {
	tasks := make([]WorkspaceTask, 0, len(active.Tasks)+len(done.Tasks))
	all := make(map[string]storedWorkspaceTask, len(active.Tasks)+len(done.Tasks))
	for id, task := range active.Tasks {
		all[id] = task
	}
	for id, task := range done.Tasks {
		all[id] = task
	}
	for id, stored := range all {
		tasks = append(tasks, WorkspaceTask{
			ID:                 id,
			Title:              stored.Title,
			Details:            stored.Details,
			AcceptanceCriteria: append([]string(nil), stored.AcceptanceCriteria...),
			Priority:           stored.Priority,
			Completed:          stored.Completed,
			CreatedAt:          stored.CreatedAt,
			UpdatedAt:          stored.UpdatedAt,
			CompletedAt:        stored.CompletedAt,
		})
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt != tasks[j].CreatedAt {
			return tasks[i].CreatedAt > tasks[j].CreatedAt
		}
		return tasks[i].ID < tasks[j].ID
	})
	activeRelative := ".echo/" + workspaceTaskFile
	doneRelative := ".echo/" + workspaceTaskDoneFile
	ignored := map[string]bool{}
	if result, err := gitIgnoredWorkspacePaths(location.root, []string{activeRelative, doneRelative}); err == nil {
		ignored = result
	} else {
		ignored = rootGitignoreIgnoredPaths(location.root, []string{activeRelative, doneRelative})
	}
	return TaskBoard{
		WorkspaceID:     workspaceID,
		StoragePath:     location.displayPath,
		DoneStoragePath: location.doneDisplayPath,
		GitIgnored:      ignored[activeRelative],
		DoneGitIgnored:  ignored[doneRelative],
		Tasks:           tasks,
	}, nil
}

func newWorkspaceTaskID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func (s *SystemService) emitTaskEvent(event TaskEvent) {
	s.emitRuntimeEvent(taskEventName, event)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, taskEventName, event)
	}
}
