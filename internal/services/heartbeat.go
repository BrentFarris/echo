package services

import (
	"context"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"time"
)

const heartbeatEventName = "echo:heartbeat:event"

// HeartbeatConfig defines the configuration for a workspace's kanban heartbeat.
type HeartbeatConfig struct {
	Enabled  bool          `json:"enabled"`
	Interval time.Duration `json:"interval"` // e.g. 1m, 5m, 15m
}

// heartbeatHandle manages a running heartbeat timer for one workspace.
type heartbeatHandle struct {
	ticker *time.Ticker
	cancel context.CancelFunc
}

// StartHeartbeat starts (or restarts) the periodic heartbeat for a workspace.
// It cancels any existing heartbeat for the same workspace first and persists the config.
func (s *SystemService) StartHeartbeat(workspaceID string, cfg HeartbeatConfig) {
	// Persist the config
	s.mu.Lock()
	if s.state.HeartbeatConfigs == nil {
		s.state.HeartbeatConfigs = make(map[string]HeartbeatConfig)
	}
	s.state.HeartbeatConfigs[workspaceID] = cfg
	_ = s.saveLocked()
	s.mu.Unlock()

	s.chatMu.Lock()
	defer s.chatMu.Unlock()

	// Cancel and replace any existing heartbeat for this workspace
	if h := s.heartbeats[workspaceID]; h != nil {
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
				s.heartbeatTick(workspaceID)
			}
		}
	}()

	s.heartbeats[workspaceID] = &heartbeatHandle{ticker: ticker, cancel: cancel}

	// Emit heartbeat started event
	s.emitHeartbeatEvent(HeartbeatEvent{
		WorkspaceID: workspaceID,
		Type:        "started",
		Message:     "Heartbeat started with interval " + cfg.Interval.String(),
	})
}

// StopHeartbeat stops the heartbeat for a workspace and clears the persisted config.
func (s *SystemService) StopHeartbeat(workspaceID string) {
	s.chatMu.Lock()
	if h := s.heartbeats[workspaceID]; h != nil {
		h.cancel()
		h.ticker.Stop()
		delete(s.heartbeats, workspaceID)
	}
	s.chatMu.Unlock()

	// Clear persisted config
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.HeartbeatConfigs != nil {
		delete(s.state.HeartbeatConfigs, workspaceID)
		_ = s.saveLocked()
	}

	// Emit heartbeat stopped event
	s.emitHeartbeatEvent(HeartbeatEvent{
		WorkspaceID: workspaceID,
		Type:        "stopped",
		Message:     "Heartbeat stopped",
	})
}

// GetHeartbeatConfig returns the persisted heartbeat config for a workspace.
func (s *SystemService) GetHeartbeatConfig(workspaceID string) HeartbeatConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.HeartbeatConfigs == nil {
		return HeartbeatConfig{}
	}
	return s.state.HeartbeatConfigs[workspaceID]
}

// heartbeatTick is called on each heartbeat for a workspace.
// It enforces liveness first (resetting or escalating stalled cards), then loads the board,
// checks eligibility via FindEligibleCards, skips if no eligible cards or a run is already active,
// and otherwise starts the scheduler.
func (s *SystemService) heartbeatTick(workspaceID string) {
	// Enforce liveness before eligibility check: reset or escalate stalled cards
	livenessCfg := s.GetLivenessConfig(workspaceID)
	s.EnforceLiveness(workspaceID, livenessCfg)

	// Check if a kanban run is already active for this workspace
	s.chatMu.Lock()
	_, running := s.kanbanRuns[workspaceID]
	s.chatMu.Unlock()
	if running {
		return
	}

	// Budget placeholder check: skip if the workspace is over budget or paused.
	// When a token budget is configured and exhausted, do not start execution.
	if allowed, _, err := s.CheckTokenBudget(workspaceID); err != nil || !allowed {
		s.emitHeartbeatEvent(HeartbeatEvent{
			WorkspaceID: workspaceID,
			Type:        "tick_no_budget",
			Message:     "Heartbeat tick: workspace over token budget",
		})
		return
	}

	// Load board and check eligibility
	s.mu.Lock()
	board := boardForWorkspace(workspaceID, s.state.KanbanCards)
	s.mu.Unlock()

	eligible := FindEligibleCards(board, defaultAgentLimit)
	if len(eligible) == 0 {
		s.emitHeartbeatEvent(HeartbeatEvent{
			WorkspaceID: workspaceID,
			Type:        "tick_no_eligible",
			Message:     "Heartbeat tick: no eligible cards",
		})
		return
	}

	s.StartKanbanExecution(workspaceID, defaultAgentLimit)
}

func (s *SystemService) emitHeartbeatEvent(event HeartbeatEvent) {
	s.emitRuntimeEvent(heartbeatEventName, event)
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, heartbeatEventName, event)
	}
}
