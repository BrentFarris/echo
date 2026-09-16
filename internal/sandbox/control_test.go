package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTakeoverHoldsNewActionsAndInvalidatesOldGeneration(t *testing.T) {
	m, w, _ := newSandboxManagerForTest(t, &fakeEngine{})
	old, err := m.WaitForAI(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.RecordFileRead(w.ID, "turn", "file", old)
	if _, err := m.TakeUserControl(w.ID, "owner", false); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(m.AdmitAIAction(w.ID, old), ErrAIActionInterrupted) {
		t.Fatal("old action admitted during takeover")
	}
	waiting := make(chan uint64, 1)
	go func() { generation, _ := m.WaitForAI(context.Background(), w.ID); waiting <- generation }()
	select {
	case <-waiting:
		t.Fatal("wait did not hold")
	case <-time.After(20 * time.Millisecond):
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.WaitForAI(canceled, w.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop did not cancel hold: %v", err)
	}
	if _, err := m.ReleaseUserControl(w.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	select {
	case generation := <-waiting:
		if generation <= old {
			t.Fatal("ownership generation did not change")
		}
		if m.AdmitAIAction(w.ID, generation) != nil {
			t.Fatal("new action not admitted after return")
		}
		if !errors.Is(m.AdmitAIAction(w.ID, old), ErrAIActionInterrupted) {
			t.Fatal("stale action replayed after return")
		}
		if m.FileReadIsFresh(w.ID, "turn", "file", generation) {
			t.Fatal("old file observation retained")
		}
		m.RecordFileRead(w.ID, "turn", "file", generation)
		if !m.FileReadIsFresh(w.ID, "turn", "file", generation) || m.FileReadIsFresh(w.ID, "other-turn", "file", generation) {
			t.Fatal("file refresh did not belong to the reading turn")
		}
	case <-time.After(time.Second):
		t.Fatal("return did not notify waiting task")
	}
}

func TestReturnedControlRequiresFreshGUIContext(t *testing.T) {
	m, w, _ := newSandboxManagerForTest(t, &fakeEngine{})
	_, _ = m.TakeUserControl(w.ID, "owner", false)
	_, _ = m.ReleaseUserControl(w.ID, "owner")
	if err := m.DesktopAction(context.Background(), w.ID, "turn", DesktopActionRequest{}); ErrorCode(err) != "desktop_context_stale" {
		t.Fatalf("stale desktop action: %v", err)
	}
	if _, _, err := m.DesktopScreenshot(context.Background(), w.ID, "turn"); err != nil {
		t.Fatal(err)
	}
	if err := m.DesktopAction(context.Background(), w.ID, "turn", DesktopActionRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BrowserCall(context.Background(), w.ID, "turn", "click", nil); ErrorCode(err) != "desktop_context_stale" {
		t.Fatalf("stale browser action: %v", err)
	}
	if _, err := m.BrowserCall(context.Background(), w.ID, "turn", "snapshot", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BrowserCall(context.Background(), w.ID, "turn", "click", nil); err != nil {
		t.Fatal(err)
	}
}

func TestDelayedGUIActionCannotCrossOwnershipGeneration(t *testing.T) {
	m, w, _ := newSandboxManagerForTest(t, &fakeEngine{})
	oldContext := WithAIControlGeneration(context.Background(), 0)
	_, _ = m.TakeUserControl(w.ID, "owner", false)
	_, _ = m.ReleaseUserControl(w.ID, "owner")
	if _, err := m.BrowserCall(oldContext, w.ID, "turn", "snapshot", nil); !errors.Is(err, ErrAIActionInterrupted) {
		t.Fatalf("delayed GUI action was admitted: %v", err)
	}
}

type continuingCommandEngine struct {
	fakeEngine
	entered, released chan struct{}
}

func (e *continuingCommandEngine) Exec(ctx context.Context, _ MachineState, _ ExecRequest) (ExecResult, error) {
	close(e.entered)
	select {
	case <-e.released:
		return ExecResult{}, nil
	case <-ctx.Done():
		return ExecResult{}, ctx.Err()
	}
}
func TestTakeoverLeavesAdmittedNonGUICommandsRunning(t *testing.T) {
	e := &continuingCommandEngine{entered: make(chan struct{}), released: make(chan struct{})}
	m, w, _ := newSandboxManagerForTest(t, &e.fakeEngine)
	m.engine = e
	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(context.Background(), w.ID, ExecRequest{Command: []string{"long-build"}})
		done <- err
	}()
	<-e.entered
	_, _ = m.TakeUserControl(w.ID, "owner", false)
	select {
	case err := <-done:
		t.Fatalf("takeover stopped the command: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// Interactive terminals use Start/OpenPTY directly, outside AI admission.
	if err := m.Start(context.Background(), w.ID); err != nil {
		t.Fatalf("user execution blocked: %v", err)
	}
	close(e.released)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
