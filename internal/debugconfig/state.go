package debugconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/brent/echo/internal/appdata"
)

type SourceRef struct {
	RootID string `json:"rootId"`
	Path   string `json:"path"`
}

type SourceBreakpoint struct {
	ID           string    `json:"id"`
	Source       SourceRef `json:"source"`
	Line         int       `json:"line"`
	Column       int       `json:"column,omitempty"`
	Enabled      bool      `json:"enabled"`
	Condition    string    `json:"condition,omitempty"`
	HitCondition string    `json:"hitCondition,omitempty"`
	LogMessage   string    `json:"logMessage,omitempty"`
}

type FunctionBreakpoint struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Condition    string `json:"condition,omitempty"`
	HitCondition string `json:"hitCondition,omitempty"`
}
type InstructionBreakpoint struct {
	ID                   string `json:"id"`
	InstructionReference string `json:"instructionReference"`
	Offset               int    `json:"offset,omitempty"`
	Enabled              bool   `json:"enabled"`
	Condition            string `json:"condition,omitempty"`
	HitCondition         string `json:"hitCondition,omitempty"`
}
type DataBreakpoint struct {
	ID               string `json:"id"`
	DataID           string `json:"dataId"`
	Name             string `json:"name,omitempty"`
	AdapterProfileID string `json:"adapterProfileId,omitempty"`
	AccessType       string `json:"accessType,omitempty"`
	Enabled          bool   `json:"enabled"`
	Condition        string `json:"condition,omitempty"`
	HitCondition     string `json:"hitCondition,omitempty"`
}
type ExceptionBreakpoint struct {
	Filter    string `json:"filter"`
	Enabled   bool   `json:"enabled"`
	Condition string `json:"condition,omitempty"`
}
type Watch struct {
	ID         string `json:"id"`
	Expression string `json:"expression"`
	Enabled    bool   `json:"enabled"`
}

type State struct {
	Revision               uint64                  `json:"revision"`
	SelectedConfiguration  string                  `json:"selectedConfigurationId,omitempty"`
	SourceBreakpoints      []SourceBreakpoint      `json:"sourceBreakpoints,omitempty"`
	FunctionBreakpoints    []FunctionBreakpoint    `json:"functionBreakpoints,omitempty"`
	InstructionBreakpoints []InstructionBreakpoint `json:"instructionBreakpoints,omitempty"`
	DataBreakpoints        []DataBreakpoint        `json:"dataBreakpoints,omitempty"`
	ExceptionBreakpoints   []ExceptionBreakpoint   `json:"exceptionBreakpoints,omitempty"`
	Watches                []Watch                 `json:"watches,omitempty"`
}

func (state State) Normalized() State {
	state.SelectedConfiguration = strings.ToLower(strings.TrimSpace(state.SelectedConfiguration))
	for index := range state.SourceBreakpoints {
		bp := &state.SourceBreakpoints[index]
		bp.ID = strings.TrimSpace(bp.ID)
		bp.Source.RootID = strings.TrimSpace(bp.Source.RootID)
		bp.Source.Path = strings.Trim(strings.ReplaceAll(bp.Source.Path, "\\", "/"), "/")
		bp.Condition = strings.TrimSpace(bp.Condition)
		bp.HitCondition = strings.TrimSpace(bp.HitCondition)
	}
	for index := range state.FunctionBreakpoints {
		bp := &state.FunctionBreakpoints[index]
		bp.ID = strings.TrimSpace(bp.ID)
		bp.Name = strings.TrimSpace(bp.Name)
		bp.Condition = strings.TrimSpace(bp.Condition)
		bp.HitCondition = strings.TrimSpace(bp.HitCondition)
	}
	for index := range state.InstructionBreakpoints {
		bp := &state.InstructionBreakpoints[index]
		bp.ID = strings.TrimSpace(bp.ID)
		bp.InstructionReference = strings.TrimSpace(bp.InstructionReference)
		bp.Condition = strings.TrimSpace(bp.Condition)
		bp.HitCondition = strings.TrimSpace(bp.HitCondition)
	}
	for index := range state.DataBreakpoints {
		bp := &state.DataBreakpoints[index]
		bp.ID = strings.TrimSpace(bp.ID)
		bp.DataID = strings.TrimSpace(bp.DataID)
		bp.Name = strings.TrimSpace(bp.Name)
		bp.AdapterProfileID = strings.ToLower(strings.TrimSpace(bp.AdapterProfileID))
		bp.AccessType = strings.TrimSpace(bp.AccessType)
		bp.Condition = strings.TrimSpace(bp.Condition)
		bp.HitCondition = strings.TrimSpace(bp.HitCondition)
	}
	for index := range state.ExceptionBreakpoints {
		bp := &state.ExceptionBreakpoints[index]
		bp.Filter = strings.TrimSpace(bp.Filter)
		bp.Condition = strings.TrimSpace(bp.Condition)
	}
	for index := range state.Watches {
		watch := &state.Watches[index]
		watch.ID = strings.TrimSpace(watch.ID)
		watch.Expression = strings.TrimSpace(watch.Expression)
	}
	sort.SliceStable(state.SourceBreakpoints, func(i, j int) bool {
		a, b := state.SourceBreakpoints[i], state.SourceBreakpoints[j]
		if a.Source.RootID != b.Source.RootID {
			return a.Source.RootID < b.Source.RootID
		}
		if a.Source.Path != b.Source.Path {
			return a.Source.Path < b.Source.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return state
}

func (state State) Validate() error {
	state = state.Normalized()
	ids := map[string]bool{}
	for _, bp := range state.SourceBreakpoints {
		if err := reserveStateID(ids, bp.ID, "source breakpoint"); err != nil {
			return err
		}
		if bp.Source.RootID == "" || bp.Source.Path == "" || bp.Line < 1 {
			return fmt.Errorf("source breakpoint %q has an invalid source or line", bp.ID)
		}
		if bp.Column < 0 {
			return fmt.Errorf("source breakpoint %q has an invalid column", bp.ID)
		}
	}
	for _, bp := range state.FunctionBreakpoints {
		if err := reserveStateID(ids, bp.ID, "function breakpoint"); err != nil {
			return err
		}
		if bp.Name == "" {
			return fmt.Errorf("function breakpoint %q requires a name", bp.ID)
		}
	}
	for _, bp := range state.InstructionBreakpoints {
		if err := reserveStateID(ids, bp.ID, "instruction breakpoint"); err != nil {
			return err
		}
		if bp.InstructionReference == "" {
			return fmt.Errorf("instruction breakpoint %q requires an instruction reference", bp.ID)
		}
	}
	for _, bp := range state.DataBreakpoints {
		if err := reserveStateID(ids, bp.ID, "data breakpoint"); err != nil {
			return err
		}
		if bp.DataID == "" {
			return fmt.Errorf("data breakpoint %q requires a dataId", bp.ID)
		}
		switch bp.AccessType {
		case "", "read", "write", "readWrite":
		default:
			return fmt.Errorf("data breakpoint %q has invalid access type %q", bp.ID, bp.AccessType)
		}
	}
	filters := map[string]bool{}
	for _, bp := range state.ExceptionBreakpoints {
		if bp.Filter == "" || filters[bp.Filter] {
			return fmt.Errorf("exception breakpoint filters must be present and unique")
		}
		filters[bp.Filter] = true
	}
	for _, watch := range state.Watches {
		if err := reserveStateID(ids, watch.ID, "watch"); err != nil {
			return err
		}
		if watch.Expression == "" {
			return fmt.Errorf("watch %q expression cannot be empty", watch.ID)
		}
	}
	return nil
}

func reserveStateID(ids map[string]bool, id, kind string) error {
	if id == "" || ids[id] {
		return fmt.Errorf("%s ids must be present and unique", kind)
	}
	ids[id] = true
	return nil
}

type StateStore struct{ data *appdata.Store }

func NewStateStore(data *appdata.Store) *StateStore { return &StateStore{data: data} }

func (s *StateStore) Load(workspaceID string) (State, error) {
	file, err := s.data.Load()
	if err != nil {
		return State{}, err
	}
	raw := file.DebugState[strings.TrimSpace(workspaceID)]
	if len(raw) == 0 {
		return State{}, nil
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("parse workspace debug state: %w", err)
	}
	return state.Normalized(), state.Validate()
}

func (s *StateStore) Save(workspaceID string, expectedRevision uint64, state State) (State, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return State{}, fmt.Errorf("workspace id is required")
	}
	state = state.Normalized()
	if err := state.Validate(); err != nil {
		return State{}, err
	}
	var saved State
	err := s.data.Update(func(file *appdata.File) error {
		if file.DebugState == nil {
			file.DebugState = map[string]json.RawMessage{}
		}
		var current State
		if raw := file.DebugState[workspaceID]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &current); err != nil {
				return err
			}
		}
		if current.Revision != expectedRevision {
			return &RevisionConflict{Expected: expectedRevision, Actual: current.Revision}
		}
		state.Revision = current.Revision + 1
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		file.DebugState[workspaceID] = raw
		saved = state
		return nil
	})
	return saved, err
}

func (s *StateStore) Delete(workspaceID string) error {
	return s.data.Update(func(file *appdata.File) error { delete(file.DebugState, strings.TrimSpace(workspaceID)); return nil })
}

type RevisionConflict struct {
	Expected uint64
	Actual   uint64
}

func (e *RevisionConflict) Error() string {
	return fmt.Sprintf("debug state revision changed: expected %d, actual %d", e.Expected, e.Actual)
}
