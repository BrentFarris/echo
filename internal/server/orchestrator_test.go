package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/workspacegroups"
)

// orchestratorGateStreamer records prompts and holds every turn open until
// release is closed, so tests can keep a workspace busy.
type orchestratorGateStreamer struct {
	mu      sync.Mutex
	prompts []string
	systems []string
	release chan struct{}
}

func (f *orchestratorGateStreamer) StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.prompts = append(f.prompts, request.Messages[len(request.Messages)-1].Content)
	if len(request.Messages) > 0 {
		f.systems = append(f.systems, request.Messages[0].Content)
	}
	f.mu.Unlock()
	events := make(chan llm.StreamEvent, 2)
	go func() {
		defer close(events)
		select {
		case <-f.release:
			events <- llm.StreamEvent{Type: llm.EventToken, Content: "done"}
			events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
		case <-ctx.Done():
			events <- llm.StreamEvent{Type: llm.EventCanceled}
		}
	}()
	return &llm.Stream{ID: "orchestrator-gate", Events: events}
}

func (f *orchestratorGateStreamer) snapshot() (prompts, systems []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...), append([]string(nil), f.systems...)
}

func waitForWorkspaceIdle(t *testing.T, s *Server, workspaceID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !s.sessions.workspaceBusy(workspaceID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("workspace did not become idle")
}

func TestOrchestratorDispatchesReadyTaskToFirstFreeWorkspace(t *testing.T) {
	s, _ := newTestServer(t)
	workspaceA := createChatWorkspace(t, s, "orchestra-a")
	workspaceB := createChatWorkspace(t, s, "orchestra-b")
	fake := &orchestratorGateStreamer{release: make(chan struct{})}
	s.llm = fake

	group, err := s.workspaceGroups.Create("Release work", []string{workspaceA.ID, workspaceB.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.workspaceGroups.AddTask(group.ID, workspacegroups.PendingTask{
		Prompt: "Fix the flaky test", Model: "test-model", Ready: true,
	}); err != nil {
		t.Fatal(err)
	}

	s.orchestrator.dispatchReadyTasks()

	// The turn runs asynchronously; wait for the streamer to be called.
	deadline := time.Now().Add(5 * time.Second)
	var prompts, systems []string
	for {
		prompts, systems = fake.snapshot()
		if len(prompts) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	saved, ok, err := s.workspaceGroups.Get(group.ID)
	if err != nil || !ok {
		t.Fatalf("reload group: %v %v", ok, err)
	}
	if len(saved.PendingTasks) != 1 {
		t.Fatalf("unexpected tasks: %+v", saved.PendingTasks)
	}
	task := saved.PendingTasks[0]
	if task.DispatchedWorkspaceID != workspaceA.ID {
		t.Fatalf("task not dispatched to first workspace: %+v", task)
	}
	if task.DispatchedChatID == "" {
		t.Fatal("dispatched chat id not recorded")
	}

	// The dispatched chat must be a new tab running as Group Worker with the
	// task prompt, and the Group Worker system prompt must be applied.
	parent, err := s.sessions.get(workspaceA.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	tabCount := len(parent.tabs)
	parent.mu.Unlock()
	if tabCount != 2 {
		t.Fatalf("expected a new background tab, got %d tabs", tabCount)
	}
	if len(prompts) != 1 || prompts[0] != "Fix the flaky test" {
		t.Fatalf("unexpected prompts: %+v", prompts)
	}
	if len(systems) != 1 || !strings.Contains(systems[0], "squash") {
		t.Fatalf("group worker system prompt missing: %q", systems[0])
	}

	close(fake.release)
	waitForWorkspaceIdle(t, s, workspaceA.ID)
}

func TestOrchestratorSkipsBusyWorkspacesAndUnreadyTasks(t *testing.T) {
	s, _ := newTestServer(t)
	workspaceA := createChatWorkspace(t, s, "orchestra-busy-a")
	workspaceB := createChatWorkspace(t, s, "orchestra-busy-b")
	fake := &orchestratorGateStreamer{release: make(chan struct{})}
	s.llm = fake

	group, err := s.workspaceGroups.Create("Busy group", []string{workspaceA.ID, workspaceB.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.workspaceGroups.AddTask(group.ID, workspacegroups.PendingTask{Prompt: "first", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.workspaceGroups.AddTask(group.ID, workspacegroups.PendingTask{Prompt: "second", Ready: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.workspaceGroups.AddTask(group.ID, workspacegroups.PendingTask{Prompt: "not ready yet", Ready: false}); err != nil {
		t.Fatal(err)
	}

	s.orchestrator.dispatchReadyTasks()

	saved, _, err := s.workspaceGroups.Get(group.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatched := map[string]string{}
	for _, task := range saved.PendingTasks {
		dispatched[task.Prompt] = task.DispatchedWorkspaceID
	}
	if dispatched["first"] != workspaceA.ID {
		t.Fatalf("first task not dispatched to workspace A: %+v", dispatched)
	}
	if dispatched["second"] != workspaceB.ID {
		t.Fatalf("second task should go to the next free workspace: %+v", dispatched)
	}
	if dispatched["not ready yet"] != "" {
		t.Fatalf("unready task must not be dispatched: %+v", dispatched)
	}

	// Both workspaces are busy now; a further pass must not dispatch anything.
	if _, err := s.workspaceGroups.AddTask(group.ID, workspacegroups.PendingTask{Prompt: "third", Ready: true}); err != nil {
		t.Fatal(err)
	}
	s.orchestrator.dispatchReadyTasks()
	saved, _, err = s.workspaceGroups.Get(group.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range saved.PendingTasks {
		if task.Prompt == "third" && task.DispatchedWorkspaceID != "" {
			t.Fatalf("task dispatched to a busy workspace: %+v", task)
		}
	}

	// Stop the dispatch loop before releasing the gate so the turn-finish
	// kick cannot dispatch "third" after the assertions are done.
	s.orchestrator.stop()
	close(fake.release)
	waitForWorkspaceIdle(t, s, workspaceA.ID)
	waitForWorkspaceIdle(t, s, workspaceB.ID)
}

func TestOrchestratorDoesNothingWithoutLLM(t *testing.T) {
	s, _ := newTestServer(t)
	s.llm = nil
	workspace := createChatWorkspace(t, s, "orchestra-nollm")
	group, err := s.workspaceGroups.Create("No LLM", []string{workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.workspaceGroups.AddTask(group.ID, workspacegroups.PendingTask{Prompt: "task", Ready: true}); err != nil {
		t.Fatal(err)
	}
	s.orchestrator.dispatchReadyTasks()
	saved, _, err := s.workspaceGroups.Get(group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.PendingTasks[0].DispatchedWorkspaceID != "" {
		t.Fatalf("task dispatched without an LLM client: %+v", saved.PendingTasks[0])
	}
}

func TestGroupWorkerModeIsImmutableBuiltInWithWorkflow(t *testing.T) {
	mode, err := agentmodes.NewManager().Resolve(t.TempDir(), agentmodes.GroupWorkerID)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Name != "Group Worker" || !strings.Contains(mode.Prompt, "ask_user_questions") {
		t.Fatalf("unexpected group worker mode: %+v", mode)
	}
	if !strings.Contains(mode.Prompt, "squash") || !strings.Contains(mode.Prompt, "branch") {
		t.Fatalf("group worker workflow missing: %q", mode.Prompt)
	}
}
