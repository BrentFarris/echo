package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

func TestHeartbeatTickSkipsWhenNoEligibleCards(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	// No cards at all — tick should be a no-op (no panic, no scheduler call)
	service.heartbeatTick(workspaceID)
}

func TestHeartbeatTickSkipsWhenRunActive(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady},
	})

	// Pretend a run is active
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.chatMu.Lock()
	service.kanbanRuns[workspaceID] = cancel
	service.chatMu.Unlock()

	// tick should see the running flag and bail
	service.heartbeatTick(workspaceID)

	// Confirm no new run was started (kanbanAgents should be empty)
	if count := service.activeKanbanAgentCount(workspaceID); count != 0 {
		t.Fatalf("expected 0 active agents after skipped tick, got %d", count)
	}
}

func TestHeartbeatStartStop(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)

	cfg := HeartbeatConfig{Enabled: true, Interval: 50 * time.Millisecond}
	service.StartHeartbeat(workspaceID, cfg)

	// Verify handle exists
	service.chatMu.Lock()
	h := service.heartbeats[workspaceID]
	service.chatMu.Unlock()
	if h == nil {
		t.Fatal("expected heartbeat handle to be set")
	}

	service.StopHeartbeat(workspaceID)

	service.chatMu.Lock()
	h = service.heartbeats[workspaceID]
	service.chatMu.Unlock()
	if h != nil {
		t.Fatal("expected heartbeat handle to be cleared after stop")
	}
}

func TestHeartbeatStartReplacesExisting(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)

	cfg1 := HeartbeatConfig{Enabled: true, Interval: time.Hour}
	service.StartHeartbeat(workspaceID, cfg1)

	service.chatMu.Lock()
	h1 := service.heartbeats[workspaceID]
	service.chatMu.Unlock()

	cfg2 := HeartbeatConfig{Enabled: true, Interval: 50 * time.Millisecond}
	service.StartHeartbeat(workspaceID, cfg2)

	service.chatMu.Lock()
	h2 := service.heartbeats[workspaceID]
	service.chatMu.Unlock()
	if h1 == h2 {
		t.Fatal("expected new heartbeat handle to replace the old one")
	}

	service.StopHeartbeat(workspaceID)
}

func TestHeartbeatTickStartsSchedulerForEligibleCards(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Done."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady},
	})

	// heartbeatTick should start the scheduler which runs the card to completion
	service.heartbeatTick(workspaceID)

	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})
	if len(board.Done) != 1 || board.Done[0].ID != "card-1" {
		t.Fatalf("expected card to be done after heartbeat tick, got %#v", board)
	}
}

func TestHeartbeatTickWithTickerFiresAndExecutes(t *testing.T) {
	root := t.TempDir()
	var requestCount int
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		requestCount++
		var req llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		title := cardTitleFromRequest(t, req)
		writeSSE(t, w,
			fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":"Completed %s."}}]}`, title),
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "First", Description: "D", AcceptanceCriteria: []string{"D"}, Lane: KanbanLaneReady},
	})

	cfg := HeartbeatConfig{Enabled: true, Interval: 50 * time.Millisecond}
	service.StartHeartbeat(workspaceID, cfg)

	// Wait for the ticker to fire and the card to complete
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) >= 1
	})
	if len(board.Done) != 1 || board.Done[0].ID != "card-1" {
		t.Fatalf("expected card done via heartbeat ticker, got %#v", board)
	}

	service.StopHeartbeat(workspaceID)
}

func TestHeartbeatShutdownCancelsHeartbeats(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)

	cfg := HeartbeatConfig{Enabled: true, Interval: time.Hour}
	service.StartHeartbeat(workspaceID, cfg)

	// Verify heartbeat is running
	service.chatMu.Lock()
	if service.heartbeats[workspaceID] == nil {
		t.Fatal("expected heartbeat to be set")
	}
	service.chatMu.Unlock()

	service.Shutdown()

	// After shutdown, heartbeats should have been canceled.
	// The Shutdown method collects and cancels all heartbeat contexts before releasing chatMu,
	// so the goroutine backing the ticker should exit cleanly without panic.
}

// TestHeartbeatConfigPersistAndRestore verifies that StartHeartbeat persists the config
// and a newly constructed service reads it back via GetHeartbeatConfig (restart restoration).
func TestHeartbeatConfigPersistAndRestore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")

	// First service: add workspace, start heartbeat.
	service1 := NewSystemServiceWithStorePath(storePath)
	root := t.TempDir()
	ws := workspaceFromPath(root)
	service1.mu.Lock()
	service1.state.Workspaces = append(service1.state.Workspaces, ws)
	service1.state.ActiveWorkspaceID = ws.ID
	if err := service1.saveLocked(); err != nil {
		service1.mu.Unlock()
		t.Fatal(err)
	}
	service1.mu.Unlock()

	cfg := HeartbeatConfig{Enabled: true, Interval: 3 * time.Minute}
	service1.StartHeartbeat(ws.ID, cfg)

	// Verify the config is persisted in state.json.
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["heartbeatConfigs"]; !ok {
		t.Fatal("state.json missing heartbeatConfigs key")
	}

	// Simulate restart: create a new service with the same store path.
	// Do not call StopHeartbeat — in a real restart, the config persists on disk.
	service2 := NewSystemServiceWithStorePath(storePath)
	restoredCfg := service2.GetHeartbeatConfig(ws.ID)
	if !restoredCfg.Enabled {
		t.Fatal("expected restored config to be enabled")
	}
	if restoredCfg.Interval != 3*time.Minute {
		t.Fatalf("expected interval 3m, got %v", restoredCfg.Interval)
	}
}

// TestHeartbeatStopClearsPersistedConfig verifies that StopHeartbeat removes the
// persisted heartbeat config so a restart does not auto-resume.
func TestHeartbeatStopClearsPersistedConfig(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")

	service := NewSystemServiceWithStorePath(storePath)
	root := t.TempDir()
	ws := workspaceFromPath(root)
	service.mu.Lock()
	service.state.Workspaces = append(service.state.Workspaces, ws)
	service.state.ActiveWorkspaceID = ws.ID
	if err := service.saveLocked(); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.mu.Unlock()

	cfg := HeartbeatConfig{Enabled: true, Interval: 1 * time.Minute}
	service.StartHeartbeat(ws.ID, cfg)
	service.StopHeartbeat(ws.ID)

	// Verify config was removed from persisted state.
	restoredCfg := service.GetHeartbeatConfig(ws.ID)
	if restoredCfg.Enabled {
		t.Fatal("expected persisted config to be cleared after stop")
	}
}

// TestHeartbeatMultiWorkspaceIndependence verifies that heartbeats for different
// workspaces operate independently: starting one does not affect the other.
func TestHeartbeatMultiWorkspaceIndependence(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	root1 := t.TempDir()
	ws1 := workspaceFromPath(root1)
	root2 := t.TempDir()
	ws2 := workspaceFromPath(root2)

	service.mu.Lock()
	service.state.Workspaces = append(service.state.Workspaces, ws1, ws2)
	service.state.ActiveWorkspaceID = ws1.ID
	if err := service.saveLocked(); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.mu.Unlock()

	cfg1 := HeartbeatConfig{Enabled: true, Interval: 50 * time.Millisecond}
	cfg2 := HeartbeatConfig{Enabled: true, Interval: time.Hour}

	service.StartHeartbeat(ws1.ID, cfg1)
	service.StartHeartbeat(ws2.ID, cfg2)

	// Both should be active.
	service.chatMu.Lock()
	h1 := service.heartbeats[ws1.ID]
	h2 := service.heartbeats[ws2.ID]
	service.chatMu.Unlock()
	if h1 == nil {
		t.Fatal("expected heartbeat for ws1")
	}
	if h2 == nil {
		t.Fatal("expected heartbeat for ws2")
	}
	if h1 == h2 {
		t.Fatal("expected distinct heartbeat handles")
	}

	// Stopping ws1 should not affect ws2.
	service.StopHeartbeat(ws1.ID)

	service.chatMu.Lock()
	h1After := service.heartbeats[ws1.ID]
	h2After := service.heartbeats[ws2.ID]
	service.chatMu.Unlock()
	if h1After != nil {
		t.Fatal("expected ws1 heartbeat cleared")
	}
	if h2After == nil {
		t.Fatal("expected ws2 heartbeat to remain after stopping ws1")
	}

	service.StopHeartbeat(ws2.ID)
}

// TestHeartbeatTickSkipsWhenBudgetExhausted verifies that heartbeatTick does not
// start execution when the workspace is over its token budget.
func TestHeartbeatTickSkipsWhenBudgetExhausted(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady},
	})

	// Set a small budget and exhaust it.
	if err := service.SetTokenBudget(workspaceID, 100); err != nil {
		t.Fatalf("SetTokenBudget: %v", err)
	}
	_, err := service.RecordTokenUsage(workspaceID, 200)
	if err != nil {
		t.Fatalf("RecordTokenUsage: %v", err)
	}

	// Verify budget is exhausted.
	allowed, _, err := service.CheckTokenBudget(workspaceID)
	if err != nil {
		t.Fatalf("CheckTokenBudget: %v", err)
	}
	if allowed {
		t.Fatal("expected workspace to be over budget")
	}

	// Tick should skip due to budget and not start a run.
	service.heartbeatTick(workspaceID)

	if count := service.activeKanbanAgentCount(workspaceID); count != 0 {
		t.Fatalf("expected 0 active agents after skipped tick, got %d", count)
	}

	// Board should still show the card in Ready (not moved).
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Ready) != 1 || board.Ready[0].ID != "card-1" {
		t.Fatalf("expected card to remain in Ready, got %#v", board)
	}
}

// TestHeartbeatTickProceedsWhenNoBudget verifies that heartbeatTick proceeds
// normally when no token budget is configured for the workspace.
func TestHeartbeatTickProceedsWhenNoBudget(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Done."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady},
	})

	// No budget configured — tick should proceed.
	service.heartbeatTick(workspaceID)

	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})
	if len(board.Done) != 1 || board.Done[0].ID != "card-1" {
		t.Fatalf("expected card to be done after heartbeat tick, got %#v", board)
	}
}

// TestHeartbeatTickProceedsWhenBudgetWithinLimit verifies that heartbeatTick
// proceeds when the workspace has a budget but is still within the limit.
func TestHeartbeatTickProceedsWhenBudgetWithinLimit(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Done."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady},
	})

	// Set a generous budget.
	if err := service.SetTokenBudget(workspaceID, 10000); err != nil {
		t.Fatalf("SetTokenBudget: %v", err)
	}

	service.heartbeatTick(workspaceID)

	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})
	if len(board.Done) != 1 || board.Done[0].ID != "card-1" {
		t.Fatalf("expected card to be done after heartbeat tick with budget within limit, got %#v", board)
	}
}

// TestHeartbeatAutoStartOnInterval verifies that when the heartbeat is started
// with a short interval, it fires periodically and executes eligible cards on each tick.
func TestHeartbeatAutoStartOnInterval(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Done."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady},
	})

	cfg := HeartbeatConfig{Enabled: true, Interval: 50 * time.Millisecond}
	service.StartHeartbeat(workspaceID, cfg)

	// Wait for the ticker to fire and execute.
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) >= 1
	})
	if len(board.Done) != 1 || board.Done[0].ID != "card-1" {
		t.Fatalf("expected card done via auto-start heartbeat, got %#v", board)
	}

	service.StopHeartbeat(workspaceID)
}
