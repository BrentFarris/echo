package services

import (
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"time"
)

const livenessEventName = "echo:liveness:event"

// LivenessConfig defines configuration for stall detection and recovery.
type LivenessConfig struct {
	Enabled       bool          `json:"enabled"`
	StallTimeout  time.Duration `json:"stallTimeout"`   // duration without progress before a card is considered stalled; default 10m
	MaxAutoRetries int          `json:"maxAutoRetries"` // max automatic resets before escalation; default 3
	CheckInterval time.Duration `json:"checkInterval"`  // how often to run EnforceLiveness; default 1m
}

// LivenessEvent is emitted when liveness enforcement detects a stalled card
// and takes recovery action.
type LivenessEvent struct {
	WorkspaceID string       `json:"workspaceId"`
	Type        string       `json:"type"` // "stalled_reset", "stalled_escalated", "check_no_stalls"
	CardID      string       `json:"cardId,omitempty"`
	Message     string       `json:"message,omitempty"`
	Board       KanbanBoard  `json:"board,omitempty"`
}

// DefaultLivenessConfig returns a LivenessConfig with sensible defaults.
func DefaultLivenessConfig() LivenessConfig {
	return LivenessConfig{
		Enabled:        false,
		StallTimeout:   10 * time.Minute,
		MaxAutoRetries: 3,
		CheckInterval:  1 * time.Minute,
	}
}

// EnforceLiveness scans InProgress cards for a workspace, detects stalls,
// classifies recovery, and takes reset or escalation actions.
// It must be called with s.mu not held (it acquires the lock internally).
func (s *SystemService) EnforceLiveness(workspaceID string, cfg LivenessConfig) {
	if !cfg.Enabled {
		return
	}

	if cfg.StallTimeout <= 0 {
		cfg.StallTimeout = DefaultLivenessConfig().StallTimeout
	}
	if cfg.MaxAutoRetries <= 0 {
		cfg.MaxAutoRetries = DefaultLivenessConfig().MaxAutoRetries
	}

	now := time.Now()

	s.mu.Lock()

	// Collect indices of stalled in-progress cards for the workspace.
	type stalledIndex struct {
		index  int
		cardID string
	}
	var stalled []stalledIndex
	for i, card := range s.state.KanbanCards {
		if card.WorkspaceID != workspaceID {
			continue
		}
		if effectiveKanbanLane(card) != KanbanLaneInProgress {
			continue
		}
		if isStalledCard(card, now, cfg.StallTimeout) {
			stalled = append(stalled, stalledIndex{index: i, cardID: card.ID})
		}
	}

	if len(stalled) == 0 {
		s.mu.Unlock()
		s.emitLivenessEvent(LivenessEvent{
			WorkspaceID: workspaceID,
			Type:        "check_no_stalls",
			Message:     fmt.Sprintf("Liveness check: no stalled cards (timeout %s)", cfg.StallTimeout),
		})
		return
	}

	// Process each stalled card: classify and take action.
	for _, si := range stalled {
		card := &s.state.KanbanCards[si.index]
		stallDuration := time.Duration(0)
		if card.StalledAt != nil {
			stallDuration = now.Sub(*card.StalledAt)
		} else if len(card.ProgressTranscript) > 0 {
			// First-time detection: set StalledAt and compute duration from last progress.
			card.StalledAt = &now
			lastEntry := card.ProgressTranscript[len(card.ProgressTranscript)-1]
			stallDuration = now.Sub(lastEntry.Timestamp)
		}

		action, recoveryType := classifyRecovery(*card, cfg.MaxAutoRetries)
		switch action {
		case livenessActionReset:
			s.resetStalledCardLocked(card, recoveryType, now)
		case livenessActionEscalate:
			s.escalateStalledCardLocked(card, now)
		}

		// Emit event for this card.
		eventType := "stalled_reset"
		if card.AutoRetriesUsed > cfg.MaxAutoRetries {
			eventType = "stalled_escalated"
		}
		s.emitLivenessEvent(LivenessEvent{
			WorkspaceID: workspaceID,
			Type:        eventType,
			CardID:      card.ID,
			Message: fmt.Sprintf("Card %s stalled for %s; %s (retry %d/%d)",
				card.ID, stallDuration.Round(time.Second),
				recoveryTypeLabel(card.RecoveryType),
				card.AutoRetriesUsed, cfg.MaxAutoRetries),
		})
	}

	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()

	// Emit the final board with all changes.
	for _, si := range stalled {
		card := &s.state.KanbanCards[si.index]
		eventType := "stalled_reset"
		if card.AutoRetriesUsed > cfg.MaxAutoRetries {
			eventType = "stalled_escalated"
		}
		s.emitLivenessEvent(LivenessEvent{
			WorkspaceID: workspaceID,
			Type:        eventType + "_board",
			CardID:      card.ID,
			Board:       board,
		})
	}
}

// livenessAction represents the recovery action to take.
type livenessAction int

const (
	livenessActionReset    livenessAction = iota // reset card to Ready lane
	livenessActionEscalate                       // escalate to Blocked with event
)

// isStalledCard returns true when an InProgress card has not recorded progress
// within the stall timeout window.
func isStalledCard(card KanbanCard, now time.Time, stallTimeout time.Duration) bool {
	if len(card.ProgressTranscript) == 0 {
		// No progress entries at all — consider stalled immediately.
		return true
	}
	lastEntry := card.ProgressTranscript[len(card.ProgressTranscript)-1]
	elapsed := now.Sub(lastEntry.Timestamp)
	return elapsed >= stallTimeout
}

// classifyRecovery decides whether to reset the card or escalate based on
// how many auto-retries have been consumed.
func classifyRecovery(card KanbanCard, maxAutoRetries int) (livenessAction, string) {
	if card.AutoRetriesUsed < maxAutoRetries {
		return livenessActionReset, "auto-reset"
	}
	return livenessActionEscalate, "escalated"
}

// resetStalledCardLocked moves a stalled card back to Ready and increments
// its retry counter. Caller must hold s.mu.
func (s *SystemService) resetStalledCardLocked(card *KanbanCard, recoveryType string, now time.Time) {
	card.AutoRetriesUsed++
	card.RecoveryType = recoveryType
	card.Lane = KanbanLaneReady
	card.Status = KanbanLaneReady
	// Clear stalled marker so future executions start fresh.
	card.StalledAt = nil

	card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
		Type:      "status",
		Title:     "Auto-reset due to stall",
		Content:   fmt.Sprintf("Card was stalled and auto-reset to Ready (retry %d).", card.AutoRetriesUsed),
		Status:    KanbanLaneReady,
		Timestamp: now,
	})

	// Cancel the running agent for this card so it doesn't continue.
	key := kanbanAgentKey(card.WorkspaceID, card.ID)
	s.chatMu.Lock()
	if agent := s.kanbanAgents[key]; agent != nil {
		agent.cancel()
		delete(s.kanbanAgents, key)
	}
	s.chatMu.Unlock()
}

// escalateStalledCardLocked moves a stalled card to Blocked after exhausting
// retries. Caller must hold s.mu.
func (s *SystemService) escalateStalledCardLocked(card *KanbanCard, now time.Time) {
	card.RecoveryType = "escalated"
	card.Lane = KanbanLaneBlocked
	card.Status = KanbanLaneBlocked

	card.ProgressTranscript = append(card.ProgressTranscript, KanbanProgressEntry{
		Type:      "status",
		Title:     "Escalated due to repeated stalls",
		Content:   fmt.Sprintf("Card stalled %d times and was escalated to Blocked for manual review.", card.AutoRetriesUsed),
		Status:    KanbanLaneBlocked,
		Timestamp: now,
	})

	// Cancel the running agent.
	key := kanbanAgentKey(card.WorkspaceID, card.ID)
	s.chatMu.Lock()
	if agent := s.kanbanAgents[key]; agent != nil {
		agent.cancel()
		delete(s.kanbanAgents, key)
	}
	s.chatMu.Unlock()
}

func recoveryTypeLabel(recoveryType string) string {
	switch recoveryType {
	case "auto-reset":
		return "reset to Ready"
	case "escalated":
		return "escalated to Blocked"
	default:
		return recoveryType
	}
}

func (s *SystemService) emitLivenessEvent(event LivenessEvent) {
	s.emitRuntimeEvent(livenessEventName, event)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, livenessEventName, event)
	}
}

// GetLivenessConfig returns the persisted liveness config for a workspace.
func (s *SystemService) GetLivenessConfig(workspaceID string) LivenessConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LivenessConfigs == nil {
		return LivenessConfig{}
	}
	return s.state.LivenessConfigs[workspaceID]
}

// SetLivenessConfig persists the liveness config for a workspace.
func (s *SystemService) SetLivenessConfig(workspaceID string, cfg LivenessConfig) {
	s.mu.Lock()
	if s.state.LivenessConfigs == nil {
		s.state.LivenessConfigs = make(map[string]LivenessConfig)
	}
	s.state.LivenessConfigs[workspaceID] = cfg
	_ = s.saveLocked()
	s.mu.Unlock()
}

// ClearKanbanCardRecovery clears the liveness recovery metadata (autoRetriesUsed,
// recoveryType, stalledAt) for a card so it can execute fresh.
func (s *SystemService) ClearKanbanCardRecovery(workspaceID string, cardID string) (KanbanBoard, error) {
	if err := s.validateWorkspaceAvailable(workspaceID); err != nil {
		return KanbanBoard{}, err
	}

	s.mu.Lock()
	found := false
	for index := range s.state.KanbanCards {
		card := &s.state.KanbanCards[index]
		if card.WorkspaceID == workspaceID && card.ID == cardID {
			card.AutoRetriesUsed = 0
			card.RecoveryType = ""
			card.StalledAt = nil
			found = true
			break
		}
	}
	if !found {
		s.mu.Unlock()
		return KanbanBoard{}, fmt.Errorf("kanban card was not found")
	}
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()
	return board, nil
}
