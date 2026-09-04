package debugconfig

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/brent/echo/internal/appdata"
)

func TestStateStoreRevisionAndWorkspaceIsolation(t *testing.T) {
	store := NewStateStore(appdata.NewStore(filepath.Join(t.TempDir(), "echo.json")))
	initial := State{
		SourceBreakpoints: []SourceBreakpoint{{ID: "bp-1", Source: SourceRef{RootID: "root", Path: `cmd\main.go`}, Line: 12, Enabled: true}},
		DataBreakpoints:   []DataBreakpoint{{ID: "data-1", DataID: "stable-address", Name: "counter", AdapterProfileID: "delve", AccessType: "write", Enabled: true}},
		Watches:           []Watch{{ID: "watch-1", Expression: " counter ", Enabled: true}},
	}
	saved, err := store.Save("workspace-a", 0, initial)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.SourceBreakpoints[0].Source.Path != "cmd/main.go" || saved.Watches[0].Expression != "counter" {
		t.Fatalf("normalized state = %#v", saved)
	}
	if other, err := store.Load("workspace-b"); err != nil || other.Revision != 0 {
		t.Fatalf("other workspace = %#v, %v", other, err)
	}
	if _, err := store.Save("workspace-a", 0, saved); err == nil {
		t.Fatal("stale state update succeeded")
	} else {
		var conflict *RevisionConflict
		if !errors.As(err, &conflict) || conflict.Actual != 1 {
			t.Fatalf("revision error = %v", err)
		}
	}
	saved.Watches[0].Expression = "next"
	saved, err = store.Save("workspace-a", 1, saved)
	if err != nil || saved.Revision != 2 {
		t.Fatalf("second save = %#v, %v", saved, err)
	}
}

func TestStateValidationCoversEveryBreakpointKind(t *testing.T) {
	tests := []State{
		{FunctionBreakpoints: []FunctionBreakpoint{{ID: "f", Name: ""}}},
		{InstructionBreakpoints: []InstructionBreakpoint{{ID: "i", InstructionReference: ""}}},
		{DataBreakpoints: []DataBreakpoint{{ID: "d", DataID: "x", AccessType: "execute"}}},
		{ExceptionBreakpoints: []ExceptionBreakpoint{{Filter: "panic"}, {Filter: "panic"}}},
		{Watches: []Watch{{ID: "w", Expression: ""}}},
	}
	for index, state := range tests {
		if err := state.Validate(); err == nil {
			t.Fatalf("invalid state %d was accepted: %#v", index, state)
		}
	}
}
