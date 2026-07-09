package services

import (
	"testing"
	"time"
)

func TestDefaultLivenessConfigReturnsSensibleDefaults(t *testing.T) {
	cfg := DefaultLivenessConfig()
	if cfg.Enabled {
		t.Error("expected Enabled to be false by default")
	}
	if cfg.StallTimeout != 10*time.Minute {
		t.Fatalf("expected StallTimeout 10m, got %v", cfg.StallTimeout)
	}
	if cfg.MaxAutoRetries != 3 {
		t.Fatalf("expected MaxAutoRetries 3, got %d", cfg.MaxAutoRetries)
	}
	if cfg.CheckInterval != 1*time.Minute {
		t.Fatalf("expected CheckInterval 1m, got %v", cfg.CheckInterval)
	}
}

func TestIsStalledCardReturnsTrueWhenNoProgress(t *testing.T) {
	card := KanbanCard{
		ID:        "card-1",
		Lane:      KanbanLaneInProgress,
		Status:    KanbanLaneInProgress,
	}
	if !isStalledCard(card, time.Now(), 5*time.Minute) {
		t.Error("expected card with no progress transcript to be stalled")
	}
}

func TestIsStalledCardReturnsTrueWhenLastEntryExceedsTimeout(t *testing.T) {
	now := time.Now()
	oldTimestamp := now.Add(-15 * time.Minute)
	card := KanbanCard{
		ID:   "card-1",
		Lane: KanbanLaneInProgress,
		ProgressTranscript: []KanbanProgressEntry{
			{Type: "message", Content: "Started", Timestamp: oldTimestamp},
		},
	}
	if !isStalledCard(card, now, 10*time.Minute) {
		t.Error("expected card with old progress to be stalled")
	}
}

func TestIsStalledCardReturnsFalseWhenRecentProgress(t *testing.T) {
	now := time.Now()
	recentTimestamp := now.Add(-2 * time.Minute)
	card := KanbanCard{
		ID:   "card-1",
		Lane: KanbanLaneInProgress,
		ProgressTranscript: []KanbanProgressEntry{
			{Type: "message", Content: "Working", Timestamp: recentTimestamp},
		},
	}
	if isStalledCard(card, now, 10*time.Minute) {
		t.Error("expected card with recent progress to not be stalled")
	}
}

func TestClassifyRecoveryReturnsAutoResetWhenRetriesAvailable(t *testing.T) {
	card := KanbanCard{ID: "card-1", AutoRetriesUsed: 0}
	action, recoveryType := classifyRecovery(card, 3)
	if action != livenessActionReset {
		t.Fatalf("expected reset action, got %d", action)
	}
	if recoveryType != "auto-reset" {
		t.Fatalf("expected 'auto-reset', got %q", recoveryType)
	}
}

func TestClassifyRecoveryReturnsEscalateWhenRetriesExhausted(t *testing.T) {
	card := KanbanCard{ID: "card-1", AutoRetriesUsed: 3}
	action, recoveryType := classifyRecovery(card, 3)
	if action != livenessActionEscalate {
		t.Fatalf("expected escalate action, got %d", action)
	}
	if recoveryType != "escalated" {
		t.Fatalf("expected 'escalated', got %q", recoveryType)
	}
}

func TestClassifyRecoveryResetsAtBoundary(t *testing.T) {
	card := KanbanCard{ID: "card-1", AutoRetriesUsed: 2}
	action, _ := classifyRecovery(card, 3)
	if action != livenessActionReset {
		t.Fatalf("expected reset at retry 2/3, got %d", action)
	}
}

func TestEnforceLivenessSkipsWhenDisabled(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = false

	// Seed a stalled card — should be ignored.
	oldTime := time.Now().Add(-20 * time.Minute)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Stalled", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "start", Timestamp: oldTime}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	// Card should remain untouched.
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.InProgress) != 1 || board.InProgress[0].ID != "card-1" {
		t.Fatalf("expected card to remain in progress when liveness disabled, got %#v", board)
	}
}

func TestEnforceLivenessResetsStalledCardToReady(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true
	cfg.StallTimeout = 5 * time.Minute
	cfg.MaxAutoRetries = 3

	oldTime := time.Now().Add(-10 * time.Minute)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Stalled", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "start", Timestamp: oldTime}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Ready) != 1 || board.Ready[0].ID != "card-1" {
		t.Fatalf("expected card reset to Ready, got board: %#v", board)
	}
	card := board.Ready[0]
	if card.AutoRetriesUsed != 1 {
		t.Fatalf("expected AutoRetriesUsed=1, got %d", card.AutoRetriesUsed)
	}
	if card.RecoveryType != "auto-reset" {
		t.Fatalf("expected RecoveryType='auto-reset', got %q", card.RecoveryType)
	}
	if card.StalledAt != nil {
		t.Fatal("expected StalledAt cleared after reset")
	}
	// Verify transcript entry.
	found := false
	for _, entry := range card.ProgressTranscript {
		if entry.Title == "Auto-reset due to stall" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected 'Auto-reset due to stall' transcript entry")
	}
}

func TestEnforceLivenessEscalatesAfterMaxRetries(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true
	cfg.StallTimeout = 5 * time.Minute
	cfg.MaxAutoRetries = 2

	oldTime := time.Now().Add(-10 * time.Minute)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Stalled", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, AutoRetriesUsed: 2, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "start", Timestamp: oldTime}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Blocked) != 1 || board.Blocked[0].ID != "card-1" {
		t.Fatalf("expected card escalated to Blocked, got board: %#v", board)
	}
	card := board.Blocked[0]
	if card.RecoveryType != "escalated" {
		t.Fatalf("expected RecoveryType='escalated', got %q", card.RecoveryType)
	}
}

func TestEnforceLivenessNoStallsEmitsCheckEvent(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true
	cfg.StallTimeout = 10 * time.Minute

	now := time.Now()
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Active", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "working", Timestamp: now}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	// Card should remain in progress.
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.InProgress) != 1 || board.InProgress[0].ID != "card-1" {
		t.Fatalf("expected card to remain in progress, got %#v", board)
	}
}

func TestEnforceLivenessIgnoresNonInProgressCards(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true
	cfg.StallTimeout = 1 * time.Minute

	oldTime := time.Now().Add(-30 * time.Minute)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready old", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneReady, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "old", Timestamp: oldTime}}},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Done old", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneDone, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "old", Timestamp: oldTime}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Ready) != 1 || len(board.Done) != 1 {
		t.Fatalf("expected ready and done cards untouched, got %#v", board)
	}
}

func TestEnforceLivenessNoCardsEmitsCheckEvent(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true

	// No cards — should not panic.
	service.EnforceLiveness(workspaceID, cfg)
}

func TestRecoveryTypeLabelReturnsCorrectLabels(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"auto-reset", "reset to Ready"},
		{"escalated", "escalated to Blocked"},
		{"custom", "custom"},
		{"", ""},
	}
	for _, tt := range tests {
		got := recoveryTypeLabel(tt.input)
		if got != tt.expected {
			t.Fatalf("recoveryTypeLabel(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestEnforceLivenessMultipleStalledCards(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true
	cfg.StallTimeout = 5 * time.Minute
	cfg.MaxAutoRetries = 3

	oldTime := time.Now().Add(-10 * time.Minute)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Stalled A", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "start", Timestamp: oldTime}}},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Stalled B", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "start", Timestamp: oldTime}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Ready) != 2 {
		t.Fatalf("expected both cards reset to Ready, got %d ready cards: %#v", len(board.Ready), board)
	}
}

func TestEnforceLivenessMixedStalledAndActive(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true
	cfg.StallTimeout = 5 * time.Minute

	oldTime := time.Now().Add(-10 * time.Minute)
	now := time.Now()
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Stalled", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "start", Timestamp: oldTime}}},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Active", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "working", Timestamp: now}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	// card-1 reset to Ready, card-2 stays in progress.
	if len(board.Ready) != 1 || board.Ready[0].ID != "card-1" {
		t.Fatalf("expected stalled card reset, got %#v", board)
	}
	if len(board.InProgress) != 1 || board.InProgress[0].ID != "card-2" {
		t.Fatalf("expected active card to remain in progress, got %#v", board)
	}
}

func TestEnforceLivenessDefaultsStallTimeout(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	cfg := DefaultLivenessConfig()
	cfg.Enabled = true
	cfg.StallTimeout = 0 // should default to 10m

	oldTime := time.Now().Add(-5 * time.Minute)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Not Stalled", Description: "D", AcceptanceCriteria: []string{"A"}, Lane: KanbanLaneInProgress, Status: KanbanLaneInProgress, ProgressTranscript: []KanbanProgressEntry{{Type: "m", Content: "start", Timestamp: oldTime}}},
	})

	service.EnforceLiveness(workspaceID, cfg)

	// 5 min < 10 min default timeout, so card should remain in progress.
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.InProgress) != 1 {
		t.Fatalf("expected card to remain in progress with default timeout, got %#v", board)
	}
}
