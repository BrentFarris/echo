// Package workspacegroups manages named groups of workspaces plus the queued
// pending tasks that the orchestrator dispatches to a free workspace in the
// group. Groups are persisted as a single JSON payload inside the shared Echo
// app data file (echo.json) so they never clobber settings or the workspace
// registry.
package workspacegroups

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/appdata"
)

// PendingTask is a prompt queued on a workspace group. It is dispatched by the
// server's orchestrator to the first workspace in the group that is ready.
type PendingTask struct {
	ID                    string     `json:"id"`
	Prompt                string     `json:"prompt"`
	Model                 string     `json:"model,omitempty"`
	ReasoningEffort       string     `json:"reasoningEffort,omitempty"`
	Temperature           *float64   `json:"temperature,omitempty"`
	Ready                 bool       `json:"ready"`
	CreatedAt             time.Time  `json:"createdAt"`
	DispatchedWorkspaceID string     `json:"dispatchedWorkspaceId,omitempty"`
	DispatchedChatID      string     `json:"dispatchedChatId,omitempty"`
	DispatchedAt          *time.Time `json:"dispatchedAt,omitempty"`
}

// WorkspaceGroup is a named set of workspaces with its own pending-task queue.
type WorkspaceGroup struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	WorkspaceIDs []string      `json:"workspaceIds"`
	PendingTasks []PendingTask `json:"pendingTasks,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
	UpdatedAt    time.Time     `json:"updatedAt"`
}

// Store reads and writes workspace groups inside the shared app data file.
type Store struct {
	data *appdata.Store
}

func NewStore(data *appdata.Store) *Store {
	return &Store{data: data}
}

func (s *Store) load() ([]WorkspaceGroup, error) {
	f, err := s.data.Load()
	if err != nil {
		return nil, err
	}
	if len(f.WorkspaceGroups) == 0 {
		return nil, nil
	}
	var groups []WorkspaceGroup
	if err := json.Unmarshal(f.WorkspaceGroups, &groups); err != nil {
		return nil, fmt.Errorf("parse workspace groups: %w", err)
	}
	return groups, nil
}

func (s *Store) save(groups []WorkspaceGroup) error {
	if groups == nil {
		groups = []WorkspaceGroup{}
	}
	raw, err := json.Marshal(groups)
	if err != nil {
		return fmt.Errorf("marshal workspace groups: %w", err)
	}
	return s.data.Update(func(f *appdata.File) error {
		f.WorkspaceGroups = raw
		return nil
	})
}

// List returns all workspace groups ordered by creation time.
func (s *Store) List() ([]WorkspaceGroup, error) {
	groups, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].CreatedAt.Before(groups[j].CreatedAt)
	})
	return groups, nil
}

// Get returns a single group by ID.
func (s *Store) Get(id string) (WorkspaceGroup, bool, error) {
	groups, err := s.List()
	if err != nil {
		return WorkspaceGroup{}, false, err
	}
	for _, group := range groups {
		if group.ID == id {
			return group, true, nil
		}
	}
	return WorkspaceGroup{}, false, nil
}

// Create adds a new group. Name must be non-empty and at least one workspace
// is required.
func (s *Store) Create(name string, workspaceIDs []string) (WorkspaceGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return WorkspaceGroup{}, fmt.Errorf("workspace group name is required")
	}
	clean := cleanWorkspaceIDs(workspaceIDs)
	if len(clean) == 0 {
		return WorkspaceGroup{}, fmt.Errorf("a workspace group requires at least one workspace")
	}
	now := time.Now().UTC()
	group := WorkspaceGroup{
		ID:           newID(),
		Name:         name,
		WorkspaceIDs: clean,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	groups, err := s.List()
	if err != nil {
		return WorkspaceGroup{}, err
	}
	groups = append(groups, group)
	if err := s.save(groups); err != nil {
		return WorkspaceGroup{}, err
	}
	return group, nil
}

// Update mutates a group's name and/or workspace membership. Nil name leaves
// the name unchanged; a nil workspaceIDs slice leaves membership unchanged.
func (s *Store) Update(id string, name *string, workspaceIDs []string) (WorkspaceGroup, error) {
	groups, err := s.List()
	if err != nil {
		return WorkspaceGroup{}, err
	}
	for i := range groups {
		if groups[i].ID != id {
			continue
		}
		if name != nil {
			trimmed := strings.TrimSpace(*name)
			if trimmed == "" {
				return WorkspaceGroup{}, fmt.Errorf("workspace group name is required")
			}
			groups[i].Name = trimmed
		}
		if workspaceIDs != nil {
			clean := cleanWorkspaceIDs(workspaceIDs)
			if len(clean) == 0 {
				return WorkspaceGroup{}, fmt.Errorf("a workspace group requires at least one workspace")
			}
			groups[i].WorkspaceIDs = clean
		}
		groups[i].UpdatedAt = time.Now().UTC()
		if err := s.save(groups); err != nil {
			return WorkspaceGroup{}, err
		}
		return groups[i], nil
	}
	return WorkspaceGroup{}, fmt.Errorf("workspace group %q not found", id)
}

// Delete removes a group and its pending tasks.
func (s *Store) Delete(id string) error {
	groups, err := s.List()
	if err != nil {
		return err
	}
	next := make([]WorkspaceGroup, 0, len(groups))
	found := false
	for _, group := range groups {
		if group.ID == id {
			found = true
			continue
		}
		next = append(next, group)
	}
	if !found {
		return fmt.Errorf("workspace group %q not found", id)
	}
	return s.save(next)
}

// AddTask appends a pending task to a group's queue.
func (s *Store) AddTask(groupID string, task PendingTask) (WorkspaceGroup, error) {
	groups, err := s.List()
	if err != nil {
		return WorkspaceGroup{}, err
	}
	for i := range groups {
		if groups[i].ID != groupID {
			continue
		}
		if strings.TrimSpace(task.Prompt) == "" {
			return WorkspaceGroup{}, fmt.Errorf("task prompt is required")
		}
		task.ID = newID()
		task.Prompt = strings.TrimSpace(task.Prompt)
		task.CreatedAt = time.Now().UTC()
		if groups[i].PendingTasks == nil {
			groups[i].PendingTasks = []PendingTask{}
		}
		groups[i].PendingTasks = append(groups[i].PendingTasks, task)
		groups[i].UpdatedAt = time.Now().UTC()
		if err := s.save(groups); err != nil {
			return WorkspaceGroup{}, err
		}
		return groups[i], nil
	}
	return WorkspaceGroup{}, fmt.Errorf("workspace group %q not found", groupID)
}

// UpdateTask mutates an existing pending task via the apply callback.
func (s *Store) UpdateTask(groupID, taskID string, apply func(*PendingTask)) (WorkspaceGroup, error) {
	groups, err := s.List()
	if err != nil {
		return WorkspaceGroup{}, err
	}
	for gi := range groups {
		if groups[gi].ID != groupID {
			continue
		}
		for ti := range groups[gi].PendingTasks {
			if groups[gi].PendingTasks[ti].ID != taskID {
				continue
			}
			apply(&groups[gi].PendingTasks[ti])
			groups[gi].UpdatedAt = time.Now().UTC()
			if err := s.save(groups); err != nil {
				return WorkspaceGroup{}, err
			}
			return groups[gi], nil
		}
		return WorkspaceGroup{}, fmt.Errorf("pending task %q not found", taskID)
	}
	return WorkspaceGroup{}, fmt.Errorf("workspace group %q not found", groupID)
}

// DeleteTask removes a pending task from a group's queue.
func (s *Store) DeleteTask(groupID, taskID string) (WorkspaceGroup, error) {
	groups, err := s.List()
	if err != nil {
		return WorkspaceGroup{}, err
	}
	for i := range groups {
		if groups[i].ID != groupID {
			continue
		}
		next := make([]PendingTask, 0, len(groups[i].PendingTasks))
		found := false
		for _, task := range groups[i].PendingTasks {
			if task.ID == taskID {
				found = true
				continue
			}
			next = append(next, task)
		}
		if !found {
			return WorkspaceGroup{}, fmt.Errorf("pending task %q not found", taskID)
		}
		groups[i].PendingTasks = next
		groups[i].UpdatedAt = time.Now().UTC()
		if err := s.save(groups); err != nil {
			return WorkspaceGroup{}, err
		}
		return groups[i], nil
	}
	return WorkspaceGroup{}, fmt.Errorf("workspace group %q not found", groupID)
}

// MarkDispatched records that a pending task was picked up by a workspace.
func (s *Store) MarkDispatched(groupID, taskID, workspaceID, chatID string) error {
	_, err := s.UpdateTask(groupID, taskID, func(task *PendingTask) {
		now := time.Now().UTC()
		task.DispatchedWorkspaceID = workspaceID
		task.DispatchedChatID = chatID
		task.DispatchedAt = &now
	})
	return err
}

// RemoveWorkspace strips a workspace from every group. Groups left without
// any workspaces are deleted along with their pending tasks. It returns the
// IDs of the groups that were removed.
func (s *Store) RemoveWorkspace(workspaceID string) ([]string, error) {
	groups, err := s.load()
	if err != nil {
		return nil, err
	}
	next := make([]WorkspaceGroup, 0, len(groups))
	removed := []string{}
	changed := false
	for _, group := range groups {
		kept := make([]string, 0, len(group.WorkspaceIDs))
		for _, id := range group.WorkspaceIDs {
			if id != workspaceID {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(group.WorkspaceIDs) {
			next = append(next, group)
			continue
		}
		changed = true
		if len(kept) == 0 {
			removed = append(removed, group.ID)
			continue
		}
		group.WorkspaceIDs = kept
		group.UpdatedAt = time.Now().UTC()
		next = append(next, group)
	}
	if !changed {
		return nil, nil
	}
	if err := s.save(next); err != nil {
		return nil, err
	}
	return removed, nil
}

func cleanWorkspaceIDs(ids []string) []string {
	clean := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	return clean
}

func newID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("wg-%d", time.Now().UnixNano())
	}
	return "wg-" + hex.EncodeToString(value[:])
}
