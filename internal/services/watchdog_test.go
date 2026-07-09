package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/tools"
)

func TestWatchdogTickSkipsWhenNoEligibleCards(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	// No cards at all — tick should be a no-op (no panic)
	service.watchdogTick(workspaceID)
}

func TestWatchdogTickSkipsWhenNoDoneCards(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady},
	})
	service.watchdogTick(workspaceID)
}

func TestWatchdogTickSkipsWhenAllChecked(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Done checked", Description: "D", AcceptanceCriteria: []string{"D"}, Lane: KanbanLaneDone, WatchdogChecked: true},
	})
	service.watchdogTick(workspaceID)
}

func TestWatchdogTickSkipsWhenNoFileChanges(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Done unchecked", Description: "D", AcceptanceCriteria: []string{"D"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})
	// No file changes recorded — tick should skip verification
	service.watchdogTick(workspaceID)

	// Card should remain unchecked since there were no changes to verify
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Done) != 1 {
		t.Fatalf("expected 1 done card, got %d", len(board.Done))
	}
	if board.Done[0].WatchdogChecked {
		t.Fatal("expected WatchdogChecked to remain false when no file changes")
	}
}

func TestWatchdogTickMarksCheckedAndRunsVerification(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	// Create a Go file so verification is relevant
	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Done unchecked", Description: "D", AcceptanceCriteria: []string{"D"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	// Simulate a file change so verification has something to check
	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        root + "/go.mod",
		Operation:   "modified",
		Before:      &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 40, SHA256: "aaa"},
		After:       &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 50, SHA256: "bbb"},
	})
	service.fileChangeMu.Unlock()

	// Tick should find the unchecked card, run verification, and mark it checked
	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Done) != 1 {
		t.Fatalf("expected 1 done card, got %d", len(board.Done))
	}
	if !board.Done[0].WatchdogChecked {
		t.Fatal("expected WatchdogChecked to be true after tick")
	}
	// Should have a progress entry from the verification
	if len(board.Done[0].ProgressTranscript) < 1 {
		t.Fatalf("expected at least 1 progress entry (watchdog), got %d", len(board.Done[0].ProgressTranscript))
	}
}

func TestWatchdogTickMarksMultipleUncheckedCards(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	// Create a Go file so verification is relevant
	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Done unchecked A", Description: "A", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneDone, WatchdogChecked: false},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Done unchecked B", Description: "B", AcceptanceCriteria: []string{"B"}, Lane: KanbanLaneDone, WatchdogChecked: false},
		{ID: "card-3", WorkspaceID: workspaceID, Title: "Done already checked C", Description: "C", AcceptanceCriteria: []string{"C"}, Lane: KanbanLaneDone, WatchdogChecked: true},
	})

	// Simulate a file change
	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        root + "/go.mod",
		Operation:   "modified",
		Before:      &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 40, SHA256: "aaa"},
		After:       &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 50, SHA256: "bbb"},
	})
	service.fileChangeMu.Unlock()

	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Done) != 3 {
		t.Fatalf("expected 3 done cards, got %d", len(board.Done))
	}
	byID := make(map[string]KanbanCard, len(board.Done))
	for _, c := range board.Done {
		byID[c.ID] = c
	}
	if !byID["card-1"].WatchdogChecked {
		t.Error("expected card-1 to be checked")
	}
	if !byID["card-2"].WatchdogChecked {
		t.Error("expected card-2 to be checked")
	}
	if !byID["card-3"].WatchdogChecked {
		t.Error("expected card-3 to remain checked")
	}
}

func TestWatchdogTickSkipsNonDoneCards(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready unchecked", Description: "R", AcceptanceCriteria: []string{"R"}, Lane: KanbanLaneReady, WatchdogChecked: false},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "In Progress unchecked", Description: "IP", AcceptanceCriteria: []string{"IP"}, Lane: KanbanLaneInProgress, WatchdogChecked: false},
	})

	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        root + "/go.mod",
		Operation:   "modified",
	})
	service.fileChangeMu.Unlock()

	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Ready) != 1 || board.Ready[0].WatchdogChecked {
		t.Error("ready card should not be marked checked by watchdog")
	}
	if len(board.InProgress) != 1 || board.InProgress[0].WatchdogChecked {
		t.Error("in-progress card should not be marked checked by watchdog")
	}
}

func TestWatchdogStartStop(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)

	cfg := WatchdogConfig{Enabled: true, Interval: 50 * time.Millisecond}
	service.StartWatchdog(workspaceID, cfg)

	// Verify handle exists
	service.chatMu.Lock()
	h := service.watchdogs[workspaceID]
	service.chatMu.Unlock()
	if h == nil {
		t.Fatal("expected watchdog handle to be set")
	}

	service.StopWatchdog(workspaceID)

	service.chatMu.Lock()
	h = service.watchdogs[workspaceID]
	service.chatMu.Unlock()
	if h != nil {
		t.Fatal("expected watchdog handle to be cleared after stop")
	}
}

func TestWatchdogStartReplacesExisting(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)

	cfg1 := WatchdogConfig{Enabled: true, Interval: time.Hour}
	service.StartWatchdog(workspaceID, cfg1)

	service.chatMu.Lock()
	h1 := service.watchdogs[workspaceID]
	service.chatMu.Unlock()

	cfg2 := WatchdogConfig{Enabled: true, Interval: 50 * time.Millisecond}
	service.StartWatchdog(workspaceID, cfg2)

	service.chatMu.Lock()
	h2 := service.watchdogs[workspaceID]
	service.chatMu.Unlock()
	if h1 == h2 {
		t.Fatal("expected new watchdog handle to replace the old one")
	}

	service.StopWatchdog(workspaceID)
}

func TestWatchdogConfigPersistAndRestore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")

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

	cfg := WatchdogConfig{Enabled: true, Interval: 5 * time.Minute}
	service1.StartWatchdog(ws.ID, cfg)

	// Verify the config is persisted in state.json.
	if _, err := os.ReadFile(storePath); err != nil {
		t.Fatalf("read state.json: %v", err)
	}

	// Simulate restart
	service2 := NewSystemServiceWithStorePath(storePath)
	restoredCfg := service2.GetWatchdogConfig(ws.ID)
	if !restoredCfg.Enabled {
		t.Fatal("expected restored config to be enabled")
	}
	if restoredCfg.Interval != 5*time.Minute {
		t.Fatalf("expected interval 5m, got %v", restoredCfg.Interval)
	}
}

func TestWatchdogStopClearsPersistedConfig(t *testing.T) {
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

	cfg := WatchdogConfig{Enabled: true, Interval: 1 * time.Minute}
	service.StartWatchdog(ws.ID, cfg)
	service.StopWatchdog(ws.ID)

	restoredCfg := service.GetWatchdogConfig(ws.ID)
	if restoredCfg.Enabled {
		t.Fatal("expected persisted config to be cleared after stop")
	}
}

func TestWatchdogMultiWorkspaceIndependence(t *testing.T) {
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

	cfg1 := WatchdogConfig{Enabled: true, Interval: 50 * time.Millisecond}
	cfg2 := WatchdogConfig{Enabled: true, Interval: time.Hour}

	service.StartWatchdog(ws1.ID, cfg1)
	service.StartWatchdog(ws2.ID, cfg2)

	// Both should be active.
	service.chatMu.Lock()
	h1 := service.watchdogs[ws1.ID]
	h2 := service.watchdogs[ws2.ID]
	service.chatMu.Unlock()
	if h1 == nil {
		t.Fatal("expected watchdog for ws1")
	}
	if h2 == nil {
		t.Fatal("expected watchdog for ws2")
	}
	if h1 == h2 {
		t.Fatal("expected distinct watchdog handles")
	}

	// Stopping ws1 should not affect ws2.
	service.StopWatchdog(ws1.ID)

	service.chatMu.Lock()
	h1After := service.watchdogs[ws1.ID]
	h2After := service.watchdogs[ws2.ID]
	service.chatMu.Unlock()
	if h1After != nil {
		t.Fatal("expected ws1 watchdog cleared")
	}
	if h2After == nil {
		t.Fatal("expected ws2 watchdog to remain after stopping ws1")
	}

	service.StopWatchdog(ws2.ID)
}

func TestWatchdogTickSkipsWhenBudgetExceeded(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	// Create a Go file so verification would be relevant
	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Done unchecked", Description: "D", AcceptanceCriteria: []string{"D"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	// Simulate a file change with proper snapshots so review returns files
	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        root + "/go.mod",
		Operation:   "modified",
		Before:      &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 40, SHA256: "aaa"},
		After:       &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 50, SHA256: "bbb"},
	})
	service.fileChangeMu.Unlock()

	// Set budget to 0 used = limit → exceeded
	if err := service.SetTokenBudget(workspaceID, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordTokenUsage(workspaceID, 100); err != nil {
		t.Fatal(err)
	}

	// Subscribe to heartbeat events to capture tick_no_budget
	eventCh, unsub := SubscribeEvents(service, 64)
	defer unsub()

	// Tick should skip verification due to budget
	service.watchdogTick(workspaceID)

	// Wait briefly for event
	select {
	case evt := <-eventCh:
		if evt.Name != HeartbeatRuntimeEventName {
			t.Fatalf("expected heartbeat event, got %s", evt.Name)
		}
		hbEvt, ok := evt.Data.(HeartbeatEvent)
		if !ok {
			t.Fatal("expected HeartbeatEvent data")
		}
		if hbEvt.Type != "tick_no_budget" {
			t.Fatalf("expected tick_no_budget type, got %s", hbEvt.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected tick_no_budget heartbeat event")
	}

	// Card should remain unchecked since verification was skipped
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Done) != 1 {
		t.Fatalf("expected 1 done card, got %d", len(board.Done))
	}
	if board.Done[0].WatchdogChecked {
		t.Fatal("expected WatchdogChecked to remain false when budget exceeded")
	}
}

func TestWatchdogTickProceedsWhenBudgetOK(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Done unchecked", Description: "D", AcceptanceCriteria: []string{"D"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        root + "/go.mod",
		Operation:   "modified",
		Before:      &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 40, SHA256: "aaa"},
		After:       &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 50, SHA256: "bbb"},
	})
	service.fileChangeMu.Unlock()

	// Set a budget with room remaining
	if err := service.SetTokenBudget(workspaceID, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordTokenUsage(workspaceID, 100); err != nil {
		t.Fatal(err)
	}

	// Tick should proceed since budget is OK
	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Done) != 1 {
		t.Fatalf("expected 1 done card, got %d", len(board.Done))
	}
	if !board.Done[0].WatchdogChecked {
		t.Fatal("expected WatchdogChecked to be true when budget allows")
	}
}

func TestWatchdogTickProceedsWhenNoBudgetSet(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Done unchecked", Description: "D", AcceptanceCriteria: []string{"D"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        root + "/go.mod",
		Operation:   "modified",
		Before:      &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 40, SHA256: "aaa"},
		After:       &tools.FileSnapshot{Path: root + "/go.mod", Exists: true, Bytes: 50, SHA256: "bbb"},
	})
	service.fileChangeMu.Unlock()

	// No budget set at all — should proceed freely
	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Done) != 1 {
		t.Fatalf("expected 1 done card, got %d", len(board.Done))
	}
	if !board.Done[0].WatchdogChecked {
		t.Fatal("expected WatchdogChecked to be true when no budget is set")
	}
}

func TestGenerateRepairCardsCreatesReadyCardOnVerificationFailure(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	// Create a Go module with a failing test so verification fails
	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	failingTestPath := filepath.Join(root, "fail_test.go")
	if err := os.WriteFile(failingTestPath, []byte("package test\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"intentional failure\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Get the workspace folder label for constructing labeled paths
	service.mu.Lock()
	var folderLabel string
	for _, ws := range service.state.Workspaces {
		if ws.ID == workspaceID && len(ws.Folders) > 0 {
			folderLabel = ws.Folders[0].Label
			break
		}
	}
	service.mu.Unlock()
	if folderLabel == "" {
		t.Fatal("could not find workspace folder label")
	}

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Add feature X", Description: "Implement feature X", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	// Simulate a file change with workspace-labeled relative path
	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        folderLabel + "/fail_test.go",
		Operation:   "created",
		After:       &tools.FileSnapshot{Path: folderLabel + "/fail_test.go", Exists: true, Bytes: 80, SHA256: "new1"},
	})
	service.fileChangeMu.Unlock()

	// Tick should run verification (which fails), mark checked, and create repair card
	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}

	// Original card should be Done and checked
	if len(board.Done) != 1 || board.Done[0].ID != "card-1" {
		t.Fatalf("expected original card in Done, got %#v", board.Done)
	}
	if !board.Done[0].WatchdogChecked {
		t.Fatal("expected original card to be marked checked")
	}

	// A repair card should be in Ready
	if len(board.Ready) != 1 {
		t.Fatalf("expected 1 ready repair card, got %d", len(board.Ready))
	}
	repair := board.Ready[0]

	// Verify repair card metadata
	if !strings.HasPrefix(repair.Title, "Repair: ") || !strings.Contains(repair.Title, "Add feature X") {
		t.Fatalf("repair title should reference original, got %q", repair.Title)
	}
	if !strings.Contains(repair.Description, "Automatic verification failed") {
		t.Fatalf("repair description should contain failure report, got %q", repair.Description)
	}
	if len(repair.AcceptanceCriteria) != 1 {
		t.Fatalf("expected 1 acceptance criterion, got %d", len(repair.AcceptanceCriteria))
	}
	if len(repair.Dependencies) != 1 || repair.Dependencies[0] != "card-1" {
		t.Fatalf("repair card should depend on original card, got dependencies: %#v", repair.Dependencies)
	}
	if repair.Lane != KanbanLaneReady {
		t.Fatalf("repair card lane should be Ready, got %q", repair.Lane)
	}

	// Verify progress transcript mentions watchdog and original card ID
	if len(repair.ProgressTranscript) < 1 {
		t.Fatal("expected at least 1 progress entry on repair card")
	}
	if !strings.Contains(repair.ProgressTranscript[0].Content, "card-1") {
		t.Fatalf("progress should reference original card ID, got %q", repair.ProgressTranscript[0].Content)
	}
}

func TestGenerateRepairCardsSkipsWhenRunActive(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	failingTestPath := filepath.Join(root, "fail_test.go")
	if err := os.WriteFile(failingTestPath, []byte("package test\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"intentional\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Get folder label
	service.mu.Lock()
	var folderLabel string
	for _, ws := range service.state.Workspaces {
		if ws.ID == workspaceID && len(ws.Folders) > 0 {
			folderLabel = ws.Folders[0].Label
			break
		}
	}
	service.mu.Unlock()

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Add feature X", Description: "Implement feature X", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        folderLabel + "/fail_test.go",
		Operation:   "created",
		After:       &tools.FileSnapshot{Path: folderLabel + "/fail_test.go", Exists: true, Bytes: 80, SHA256: "new1"},
	})
	service.fileChangeMu.Unlock()

	// Pretend a run is active — repair cards should not be created
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.chatMu.Lock()
	service.kanbanRuns[workspaceID] = cancel
	service.chatMu.Unlock()

	_ = ctx // unused; we just need the cancel func to simulate a run

	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}

	// Original card marked checked but no repair card created
	if len(board.Done) != 1 {
		t.Fatalf("expected 1 done card, got %d", len(board.Done))
	}
	if !board.Done[0].WatchdogChecked {
		t.Fatal("expected original card to be marked checked even when run active")
	}
	if len(board.Ready) != 0 {
		t.Fatalf("expected no repair cards when run is active, got %d ready cards", len(board.Ready))
	}
}

func TestGenerateRepairCardsMultipleFailedCards(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	failingTestPath := filepath.Join(root, "fail_test.go")
	if err := os.WriteFile(failingTestPath, []byte("package test\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"intentional\") }\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Get folder label
	service.mu.Lock()
	var folderLabel string
	for _, ws := range service.state.Workspaces {
		if ws.ID == workspaceID && len(ws.Folders) > 0 {
			folderLabel = ws.Folders[0].Label
			break
		}
	}
	service.mu.Unlock()

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Feature A", Description: "A", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneDone, WatchdogChecked: false},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Feature B", Description: "B", AcceptanceCriteria: []string{"B"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        folderLabel + "/fail_test.go",
		Operation:   "created",
		After:       &tools.FileSnapshot{Path: folderLabel + "/fail_test.go", Exists: true, Bytes: 80, SHA256: "new1"},
	})
	service.fileChangeMu.Unlock()

	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}

	// Both original cards in Done
	if len(board.Done) != 2 {
		t.Fatalf("expected 2 done cards, got %d", len(board.Done))
	}

	// Two repair cards in Ready
	if len(board.Ready) != 2 {
		t.Fatalf("expected 2 ready repair cards, got %d", len(board.Ready))
	}

	// Each repair card should depend on its respective original
	repairDeps := make(map[string]string)
	for _, card := range board.Ready {
		if strings.HasPrefix(card.Title, "Repair: ") {
			if len(card.Dependencies) != 1 {
				t.Fatalf("repair card %s should have exactly 1 dependency", card.ID)
			}
			repairDeps[card.Dependencies[0]] = card.Title
		}
	}
	if _, ok := repairDeps["card-1"]; !ok {
		t.Fatal("expected repair card for card-1")
	}
	if _, ok := repairDeps["card-2"]; !ok {
		t.Fatal("expected repair card for card-2")
	}
}

func TestGenerateRepairCardsNoRepairOnVerificationPass(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))

	// Create a Go module with a passing test — verification should pass
	goModPath := filepath.Join(root, "go.mod")
	if err := os.WriteFile(goModPath, []byte("module example.com/test\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	passingTestPath := filepath.Join(root, "pass_test.go")
	if err := os.WriteFile(passingTestPath, []byte("package test\n\nimport \"testing\"\n\nfunc TestPass(t *testing.T) {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Get folder label
	service.mu.Lock()
	var folderLabel string
	for _, ws := range service.state.Workspaces {
		if ws.ID == workspaceID && len(ws.Folders) > 0 {
			folderLabel = ws.Folders[0].Label
			break
		}
	}
	service.mu.Unlock()

	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Add feature X", Description: "Implement feature X", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneDone, WatchdogChecked: false},
	})

	service.fileChangeMu.Lock()
	service.fileChanges[workspaceID] = append(service.fileChanges[workspaceID], trackedFileChange{
		ID:          "fc-1",
		WorkspaceID: workspaceID,
		Path:        folderLabel + "/pass_test.go",
		Operation:   "created",
		After:       &tools.FileSnapshot{Path: folderLabel + "/pass_test.go", Exists: true, Bytes: 50, SHA256: "new1"},
	})
	service.fileChangeMu.Unlock()

	service.watchdogTick(workspaceID)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}

	// Only the original Done card, no repair cards
	if len(board.Done) != 1 {
		t.Fatalf("expected 1 done card, got %d", len(board.Done))
	}
	if !board.Done[0].WatchdogChecked {
		t.Fatal("expected original card to be marked checked")
	}
	if len(board.Ready) != 0 {
		t.Fatalf("expected no repair cards when verification passed, got %d ready cards", len(board.Ready))
	}
}
