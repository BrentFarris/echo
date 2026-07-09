package services

import (
	"context"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"strings"
	"time"
)

// WatchdogConfig defines the configuration for a workspace's kanban watchdog.
type WatchdogConfig struct {
	Enabled  bool          `json:"enabled"`
	Interval time.Duration `json:"interval"` // e.g. 1m, 5m, 15m
}

// watchdogHandle manages a running watchdog timer for one workspace.
type watchdogHandle struct {
	ticker *time.Ticker
	cancel context.CancelFunc
}

// StartWatchdog starts (or restarts) the periodic watchdog for a workspace.
// It cancels any existing watchdog for the same workspace first and persists the config.
func (s *SystemService) StartWatchdog(workspaceID string, cfg WatchdogConfig) {
	// Persist the config
	s.mu.Lock()
	if s.state.WatchdogConfigs == nil {
		s.state.WatchdogConfigs = make(map[string]WatchdogConfig)
	}
	s.state.WatchdogConfigs[workspaceID] = cfg
	_ = s.saveLocked()
	s.mu.Unlock()

	s.chatMu.Lock()
	defer s.chatMu.Unlock()

	// Cancel and replace any existing watchdog for this workspace
	if h := s.watchdogs[workspaceID]; h != nil {
		h.cancel()
	}

	ticker := time.NewTicker(cfg.Interval)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.watchdogTick(workspaceID)
			}
		}
	}()

	s.watchdogs[workspaceID] = &watchdogHandle{ticker: ticker, cancel: cancel}

	// Emit watchdog started event
	s.emitWatchdogEvent(WatchdogEvent{
		WorkspaceID: workspaceID,
		Type:        "started",
		Message:     "Watchdog started with interval " + cfg.Interval.String(),
	})
}

// StopWatchdog stops the watchdog for a workspace and clears the persisted config.
func (s *SystemService) StopWatchdog(workspaceID string) {
	s.chatMu.Lock()
	if h := s.watchdogs[workspaceID]; h != nil {
		h.cancel()
		h.ticker.Stop()
		delete(s.watchdogs, workspaceID)
	}
	s.chatMu.Unlock()

	// Clear persisted config
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.WatchdogConfigs != nil {
		delete(s.state.WatchdogConfigs, workspaceID)
		_ = s.saveLocked()
	}

	// Emit watchdog stopped event
	s.emitWatchdogEvent(WatchdogEvent{
		WorkspaceID: workspaceID,
		Type:        "stopped",
		Message:     "Watchdog stopped",
	})
}

// GetWatchdogConfig returns the persisted watchdog config for a workspace.
func (s *SystemService) GetWatchdogConfig(workspaceID string) WatchdogConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.WatchdogConfigs == nil {
		return WatchdogConfig{}
	}
	return s.state.WatchdogConfigs[workspaceID]
}

// watchdogTick is called on each watchdog tick for a workspace.
// It filters Done cards where WatchdogChecked is false, runs verification,
// marks checked cards as WatchdogChecked = true, and generates repair cards
// when verification fails.
func (s *SystemService) watchdogTick(workspaceID string) {
	// Load the workspace
	workspace, err := s.workspaceByID(workspaceID)
	if err != nil {
		return
	}

	// Get changed paths from the workspace change review
	review := s.workspaceChangeReview(workspaceID)
	changedPaths := make([]string, 0, len(review.Files))
	for _, f := range review.Files {
		changedPaths = append(changedPaths, f.Path)
	}

	if len(changedPaths) == 0 {
		return
	}

	// Check budget before verification; skip if exceeded
	allowed, _, err := s.CheckTokenBudget(workspaceID)
	if err != nil || !allowed {
		s.emitHeartbeatEvent(HeartbeatEvent{
			WorkspaceID: workspaceID,
			Type:        "tick_no_budget",
			Message:     "Watchdog tick skipped — token budget exceeded",
		})
		return
	}

	s.mu.Lock()

	// Find Done cards where WatchdogChecked is false
	var uncheckedCardIDs []string
	for _, card := range s.state.KanbanCards {
		if card.WorkspaceID == workspaceID &&
			effectiveKanbanLane(card) == KanbanLaneDone &&
			!card.WatchdogChecked {
			uncheckedCardIDs = append(uncheckedCardIDs, card.ID)
		}
	}

	if len(uncheckedCardIDs) == 0 {
		s.mu.Unlock()
		return
	}

	// Run verification for unchecked cards
	ctx, cancel := context.WithTimeout(context.Background(), kanbanVerificationTimeout)
	defer cancel()

	report, err := s.runKanbanVerification(ctx, workspace, changedPaths)
	if err != nil {
		s.mu.Unlock()
		return
	}

	verificationFailed := !kanbanVerificationReportSucceeded(report)

	// Mark all unchecked Done cards as checked regardless of verification result
	for i := range s.state.KanbanCards {
		card := &s.state.KanbanCards[i]
		if card.WorkspaceID == workspaceID &&
			effectiveKanbanLane(*card) == KanbanLaneDone &&
			!card.WatchdogChecked {
			card.WatchdogChecked = true
			card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
				Type:      "message",
				Title:     kanbanVerificationProgressTitle(report, 0),
				Content:   kanbanVerificationReportText(report),
				Status:    card.Lane,
				Timestamp: time.Now(),
			})
		}
	}

	// Generate repair cards if verification failed and no agents are running
	var repairBoard KanbanBoard
	if verificationFailed {
		s.mu.Unlock()
		repairBoard = s.generateRepairCardsFromVerification(workspaceID, uncheckedCardIDs, report)
	} else {
		board := boardForWorkspace(workspaceID, s.state.KanbanCards)
		s.mu.Unlock()
		repairBoard = board
	}

	// Emit kanban event for each checked card
	for _, cardID := range uncheckedCardIDs {
		s.emitKanbanEvent(KanbanEvent{
			WorkspaceID: workspaceID,
			CardID:      cardID,
			Type:        "watchdog_checked",
			Board:       repairBoard,
		})
	}

	// Emit watchdog check_complete event
	s.emitWatchdogEvent(WatchdogEvent{
		WorkspaceID: workspaceID,
		Type:        "check_complete",
		Message:     fmt.Sprintf("Watchdog checked %d card(s); verification %s.", len(uncheckedCardIDs), map[bool]string{true: "failed", false: "passed"}[verificationFailed]),
	})
}

// generateRepairCardsFromVerification creates Ready repair cards linked to the
// failed Done cards when verification fails. It returns the updated board.
func (s *SystemService) generateRepairCardsFromVerification(workspaceID string, failedCardIDs []string, report kanbanVerificationReport) KanbanBoard {
	s.chatMu.Lock()
	if _, running := s.kanbanRuns[workspaceID]; running {
		s.chatMu.Unlock()
		return boardForWorkspace(workspaceID, s.state.KanbanCards)
	}
	s.chatMu.Unlock()

	s.mu.Lock()

	description := strings.TrimSpace(kanbanVerificationRepairPrompt(report))

	// Collect original card info and create repair cards
	type repairCardInfo struct {
		card     KanbanCard
		failedID string
	}
	var repairs []repairCardInfo

	for _, failedCardID := range failedCardIDs {
		// Find the original card to pull title
		var originalTitle string
		for _, card := range s.state.KanbanCards {
			if card.ID == failedCardID && card.WorkspaceID == workspaceID {
				originalTitle = card.Title
				break
			}
		}

		repairCard := KanbanCard{
			ID:                 fmt.Sprintf("card-%d", s.nextKanbanCardNumberLocked()),
			WorkspaceID:        workspaceID,
			Title:              fmt.Sprintf("Repair: %s", originalTitle),
			Description:        description,
			AcceptanceCriteria: []string{"Fix the verification failure and re-verify."},
			Dependencies:       []string{failedCardID},
			Lane:               KanbanLaneReady,
			Status:             KanbanLaneReady,
			ProgressTranscript: []KanbanProgressEntry{{
				Type:      "message",
				Title:     "Repair card created",
				Content:   fmt.Sprintf("Created by watchdog after verification failure of %s.", failedCardID),
				Status:    KanbanLaneReady,
				Timestamp: time.Now(),
			}},
		}

		s.state.KanbanCards = append(s.state.KanbanCards, repairCard)
		repairs = append(repairs, repairCardInfo{card: repairCard, failedID: failedCardID})
	}

	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()

	// Emit card_created events and watchdog repair_created for each repair card
	for _, info := range repairs {
		s.emitKanbanEvent(KanbanEvent{
			WorkspaceID: workspaceID,
			CardID:      info.card.ID,
			Type:        "card_created",
			Board:       board,
		})
		s.emitWatchdogEvent(WatchdogEvent{
			WorkspaceID: workspaceID,
			CardID:      info.card.ID,
			Type:        "repair_created",
			Message:     fmt.Sprintf("Repair card created for %s after verification failure.", info.failedID),
		})
	}

	return board
}

func (s *SystemService) emitWatchdogEvent(event WatchdogEvent) {
	s.emitRuntimeEvent(watchdogEventName, event)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, watchdogEventName, event)
	}
}
