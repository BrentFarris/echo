package services

import (
	"fmt"
)

// TokenBudget tracks token usage limits per workspace.
type TokenBudget struct {
	// Limit is the maximum tokens allowed. 0 means unlimited.
	Limit int64 `json:"limit"`
	// Used is the number of tokens consumed in the current period.
	Used int64 `json:"used"`
	// Paused indicates whether the workspace has been paused due to budget exhaustion.
	Paused bool `json:"paused"`
}

type TokenBudgetService struct {
	budgets map[string]TokenBudget // workspaceID -> TokenBudget
}

func newTokenBudgetService() *TokenBudgetService {
	return &TokenBudgetService{
		budgets: make(map[string]TokenBudget),
	}
}

// Set sets the token budget for a workspace.
func (s *SystemService) SetTokenBudget(workspaceID string, limit int64) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace id is required")
	}
	if limit < 0 {
		return fmt.Errorf("token budget limit must be non-negative")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !workspaceExists(s.state.Workspaces, workspaceID) {
		return fmt.Errorf("workspace was not found")
	}

	s.tokenBudget.budgets[workspaceID] = TokenBudget{
		Limit:  limit,
		Used:   0,
		Paused: false,
	}

	if err := s.saveLocked(); err != nil {
		return err
	}
	return nil
}

// Check returns whether a workspace is within its token budget.
// Returns (allowed, remaining) where allowed is false if the budget is exceeded or paused.
func (s *SystemService) CheckTokenBudget(workspaceID string) (bool, int64, error) {
	if workspaceID == "" {
		return false, 0, fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !workspaceExists(s.state.Workspaces, workspaceID) {
		return false, 0, fmt.Errorf("workspace was not found")
	}

	budget, ok := s.tokenBudget.budgets[workspaceID]
	if !ok || budget.Limit == 0 {
		// No budget set or unlimited
		return true, 0, nil
	}

	if budget.Paused {
		remaining := budget.Limit - budget.Used
		if remaining < 0 {
			remaining = 0
		}
		return false, remaining, nil
	}

	remaining := budget.Limit - budget.Used
	if remaining <= 0 {
		// Auto-pause when exceeded
		budget.Paused = true
		s.tokenBudget.budgets[workspaceID] = budget
		_ = s.saveLocked()
		return false, 0, nil
	}

	return true, remaining, nil
}

// Record adds consumed tokens to a workspace's usage.
// Returns the updated used count.
func (s *SystemService) RecordTokenUsage(workspaceID string, tokens int64) (int64, error) {
	if workspaceID == "" {
		return 0, fmt.Errorf("workspace id is required")
	}
	if tokens < 0 {
		return 0, fmt.Errorf("token count must be non-negative")
	}

	s.mu.Lock()

	if !workspaceExists(s.state.Workspaces, workspaceID) {
		return 0, fmt.Errorf("workspace was not found")
	}

	budget, ok := s.tokenBudget.budgets[workspaceID]
	if !ok || budget.Limit == 0 {
		// No budget configured; allow freely
		s.mu.Unlock()
		return 0, nil
	}

	budget.Used += tokens
	exceeded := false
	if budget.Used >= budget.Limit && !budget.Paused {
		budget.Paused = true
		exceeded = true
	}
	s.tokenBudget.budgets[workspaceID] = budget

	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()

	if exceeded {
		s.onBudgetExceeded(workspaceID)
	}
	return budget.Used, nil
}

// Reset resets the token usage counter for a workspace and unpauses it.
func (s *SystemService) ResetTokenBudget(workspaceID string) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !workspaceExists(s.state.Workspaces, workspaceID) {
		return fmt.Errorf("workspace was not found")
	}

	budget, ok := s.tokenBudget.budgets[workspaceID]
	if !ok {
		return nil // Nothing to reset
	}

	s.tokenBudget.budgets[workspaceID] = TokenBudget{
		Limit:  budget.Limit,
		Used:   0,
		Paused: false,
	}

	if err := s.saveLocked(); err != nil {
		return err
	}
	return nil
}

// GetTokenBudget returns the current budget for a workspace.
func (s *SystemService) GetTokenBudget(workspaceID string) (TokenBudget, error) {
	if workspaceID == "" {
		return TokenBudget{}, fmt.Errorf("workspace id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !workspaceExists(s.state.Workspaces, workspaceID) {
		return TokenBudget{}, fmt.Errorf("workspace was not found")
	}

	budget, ok := s.tokenBudget.budgets[workspaceID]
	if !ok {
		return TokenBudget{}, nil
	}
	return budget, nil
}

// onBudgetExceeded is called after RecordTokenUsage detects the workspace just crossed
// its token limit. It cancels active kanban runs and emits a budget_exceeded event.
func (s *SystemService) onBudgetExceeded(workspaceID string) {
	// Cancel any active kanban run for this workspace
	s.chatMu.Lock()
	cancelRun := s.kanbanRuns[workspaceID]
	s.chatMu.Unlock()
	if cancelRun != nil {
		cancelRun()
	}

	// Emit budget_exceeded heartbeat event
	s.emitHeartbeatEvent(HeartbeatEvent{
		WorkspaceID: workspaceID,
		Type:        "budget_exceeded",
		Message:     "Token budget exceeded — active kanban execution canceled",
	})
}
