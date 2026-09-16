package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
	"github.com/brent/echo/internal/workspaces"
)

type takeoverStreamer struct {
	server            *Server
	workspaceID, path string
	started           chan struct{}
	requests          atomic.Int32
	refreshed         atomic.Bool
}

func TestDesktopHoldExcludesGoalActiveTime(t *testing.T) {
	server, _ := newTestServer(t)
	w := createChatWorkspace(t, server, "goal-hold")
	parent, err := server.sessions.get(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := parent.resolveSurfaceTab("", chatSurfaceMain)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-10 * time.Second)
	s.active = &sessions.Turn{ID: "held-goal-turn", GoalID: "held-goal"}
	s.transcript.Goals = []sessions.GoalState{{ID: "held-goal", Status: sessions.GoalStatusActive, ActiveSince: &started}}
	s.setSandboxHold(true)
	goal := &s.transcript.Goals[0]
	if goal.ActiveSince != nil || goal.ActiveSeconds < 10 {
		t.Fatal("Goal clock continued during hold")
	}
	seconds := goal.ActiveSeconds
	s.setSandboxHold(true)
	if goal.ActiveSeconds != seconds {
		t.Fatal("duplicate hold notification counted time twice")
	}
	returned := time.Now()
	s.setSandboxHold(false)
	if goal.ActiveSince == nil || goal.ActiveSince.Before(returned) || goal.ActiveSeconds != seconds {
		t.Fatal("Goal did not resume from return time")
	}
	s.active = nil
}

type takeoverResearchStreamer struct {
	inner       *researchFlowStreamer
	server      *Server
	workspaceID string
	taken       atomic.Bool
	refreshed   atomic.Bool
	started     chan struct{}
}

func (f *takeoverResearchStreamer) StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream {
	isResearch := len(request.Messages) > 0 && request.Messages[0].Name == "echo-research-agent"
	if isResearch && f.taken.CompareAndSwap(false, true) {
		_, _ = f.server.sandbox.TakeUserControl(f.workspaceID, "research-owner", false)
		close(f.started)
	}
	if isResearch {
		for _, message := range request.Messages {
			if strings.Contains(message.Content, sandboxResumedGuidance) {
				f.refreshed.Store(true)
			}
		}
	}
	return f.inner.StreamChat(ctx, request)
}

func TestResearchWorkerRefreshesAfterDesktopTakeover(t *testing.T) {
	server, _ := newTestServer(t)
	w := createChatWorkspace(t, server, "research-takeover")
	w, err := server.workspaces.SetSandboxConfig(w.ID, workspaces.SandboxConfig{Enabled: true, CPULimit: 4, MemoryMiB: 6144, IdleTimeoutMinutes: 30})
	if err != nil {
		t.Fatal(err)
	}
	f := &takeoverResearchStreamer{server: server, workspaceID: w.ID, started: make(chan struct{}), inner: &researchFlowStreamer{workspaceLabel: normalizeWorkspaceFolderLabel(filepath.Base(w.MainPath))}}
	server.llm = f
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, w.ID)
	if err := conn.WriteJSON(map[string]any{"type": "chat_send", "workspaceId": w.ID, "message": "Research the workspace"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.started:
	case <-time.After(3 * time.Second):
		t.Fatal("research not started")
	}
	readUntilSessionEvent(t, conn, "execution_hold")
	_, _ = server.sandbox.ReleaseUserControl(w.ID, "research-owner")
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" || !f.refreshed.Load() {
		t.Fatalf("research failed to refresh: %+v", finished)
	}
	transcript := loadActiveTabTranscript(t, w)
	interrupted := false
	for _, turn := range transcript.Turns {
		for _, activity := range turn.ResearchTools {
			if activity.Status == "interrupted" {
				interrupted = true
			}
		}
	}
	if !interrupted {
		t.Fatal("stale research call was not interrupted")
	}
}

func TestHostEditsRequireRereadAfterTakeover(t *testing.T) {
	server, _ := newTestServer(t)
	w := createChatWorkspace(t, server, "reread")
	w, err := server.workspaces.SetSandboxConfig(w.ID, workspaces.SandboxConfig{Enabled: true, CPULimit: 4, MemoryMiB: 6144, IdleTimeoutMinutes: 30})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w.MainPath, "document.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := tools.ExecutionContext{Context: context.Background(), WorkspaceID: w.ID, WorkspacePath: w.MainPath, Sandbox: server.sandbox, SandboxEnabled: true, TurnID: "editing-turn"}
	_, _ = server.sandbox.TakeUserControl(w.ID, "owner", false)
	_, _ = server.sandbox.ReleaseUserControl(w.ID, "owner")
	generation, _ := server.sandbox.AIControl(w.ID)
	ctx.AIGeneration = &generation
	args := json.RawMessage(`{"path":"document.txt","oldText":"before","newText":"after"}`)
	result := server.tools.Execute(ctx, "filesystem_edit_text", args)
	if result.Error == nil || result.Error.Code != "file_context_stale" {
		t.Fatalf("stale file edit not rejected: %+v", result)
	}
	if result := server.tools.Execute(ctx, "filesystem_read_text", json.RawMessage(`{"path":"document.txt"}`)); !result.Success {
		t.Fatalf("reread failed: %+v", result)
	}
	if result := server.tools.Execute(ctx, "filesystem_edit_text", args); !result.Success {
		t.Fatalf("edit after reread failed: %+v", result)
	}
}

func (f *takeoverStreamer) StreamChat(ctx context.Context, request llm.ChatRequest) *llm.Stream {
	events := make(chan llm.StreamEvent, 4)
	if f.requests.Add(1) == 1 {
		_, _ = f.server.sandbox.TakeUserControl(f.workspaceID, "test-owner", false)
		close(f.started)
		for index, name := range []string{"one.txt", "two.txt"} {
			args, _ := json.Marshal(map[string]any{"path": f.path + "/" + name, "content": "stale"})
			event := researchToolCall(name, "filesystem_create_text", string(args))
			event.ToolCall.Index = index
			events <- event
		}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		for _, message := range request.Messages {
			if strings.Contains(message.Content, sandboxResumedGuidance) {
				f.refreshed.Store(true)
			}
		}
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Resumed with fresh context."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{Events: events}
}

func TestDesktopTakeoverDuringStreamingDiscardsBatchAndResumes(t *testing.T) {
	server, _ := newTestServer(t)
	w := createChatWorkspace(t, server, "takeover")
	var err error
	w, err = server.workspaces.SetSandboxConfig(w.ID, workspaces.SandboxConfig{Enabled: true, CPULimit: 4, MemoryMiB: 6144, IdleTimeoutMinutes: 30})
	if err != nil {
		t.Fatal(err)
	}
	f := &takeoverStreamer{server: server, workspaceID: w.ID, path: normalizeWorkspaceFolderLabel(filepath.Base(w.MainPath)), started: make(chan struct{})}
	server.llm = f
	conn := dialSharedClient(t, startWebSocketTestServer(t, server))
	subscribeChat(t, conn, w.ID)
	if err := conn.WriteJSON(map[string]any{"type": "chat_send", "workspaceId": w.ID, "message": "Create files"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	held := readUntilSessionEvent(t, conn, "execution_hold")
	if held["held"] != true {
		t.Fatalf("hold event: %+v", held)
	}
	if f.requests.Load() != 1 {
		t.Fatal("another model request started during hold")
	}
	if _, err := os.Stat(filepath.Join(w.MainPath, "one.txt")); !os.IsNotExist(err) {
		t.Fatal("tool ran during takeover")
	}
	if _, err := server.sandbox.ReleaseUserControl(w.ID, "test-owner"); err != nil {
		t.Fatal(err)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" || !f.refreshed.Load() {
		t.Fatalf("did not automatically resume with fresh context: %+v", finished)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(w.MainPath, name)); !os.IsNotExist(err) {
			t.Fatal("stale tool replayed after return")
		}
	}
	transcript := loadActiveTabTranscript(t, w)
	count := 0
	for _, turn := range transcript.Turns {
		for _, step := range turn.AssistantTurns {
			for _, tool := range step.Tools {
				count++
				if tool.Status != "interrupted" {
					t.Fatalf("unexecuted call not recorded as interrupted: %+v", tool)
				}
			}
		}
	}
	if count != 2 {
		t.Fatalf("lost pending calls: %d", count)
	}
}
