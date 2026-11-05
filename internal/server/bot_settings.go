package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/brent/echo/internal/domain"
)

// Keep input-only fields separate from the saved bot. RawMessage distinguishes
// an omitted limit (preserve it) from zero (unlimited) and null (invalid).
type botSaveInput struct {
	domain.Bot
	Limit              json.RawMessage `json:"maxParallelPrompts"`
	EnableMultitasking *bool           `json:"enableMultitasking"`
}

func (s *Server) saveBotInput(raw json.RawMessage, scope *domain.ExecutionScope) (domain.Bot, error) {
	var target struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &target); err != nil {
		return domain.Bot{}, err
	}
	s.mu.Lock()
	var input botSaveInput
	if target.ID != "" {
		b, err := s.Bot(target.ID)
		if err != nil && (scope != nil || !errors.Is(err, sql.ErrNoRows)) {
			s.mu.Unlock()
			return b, err
		}
		input.Bot = b
		// The settings API replaces an explicit plugin map. Tool calls merge
		// individual selections. normalizeBot preserves an omitted map.
		if scope == nil {
			input.Plugins = nil
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.mu.Unlock()
		return domain.Bot{}, err
	}
	b := input.Bot
	switch {
	case input.Limit != nil:
		var limit *int
		if err := json.Unmarshal(input.Limit, &limit); err != nil || limit == nil {
			s.mu.Unlock()
			return b, errors.New("maxParallelPrompts must be a nonnegative whole number")
		}
		b.MaxParallelPrompts = limit
	case input.EnableMultitasking != nil:
		limit := 1
		if *input.EnableMultitasking {
			limit = 0
		}
		b.MaxParallelPrompts = &limit
	}
	return s.updateBotLocked(b, scope)
}

func sameBotScope(a, b domain.Bot) bool {
	if a.ID != b.ID || a.Handle != b.Handle || a.Name != b.Name || a.Prompt != b.Prompt || !slices.Equal(a.Roots, b.Roots) {
		return false
	}
	for id := range a.Plugins {
		if a.PluginEnabled(id) != b.PluginEnabled(id) {
			return false
		}
	}
	for id := range b.Plugins {
		if a.PluginEnabled(id) != b.PluginEnabled(id) {
			return false
		}
	}
	return true
}

func (s *Server) saveBotLocked(b domain.Bot) (domain.Bot, error) {
	// Scope changes can release mu during cancellation. Check the shared
	// namespace here, immediately before committing the new handle.
	if err := s.checkGroupHandle(b.Handle, ""); err != nil {
		return b, err
	}
	b.Revision = 1
	if old, err := s.Bot(b.ID); err == nil {
		b.Revision = old.Revision
		if !sameBotScope(old, b) {
			b.Revision++
		}
	}
	err := s.Store.Put("bots", b.ID, b)
	if err == nil {
		s.admitLocked(b.ID)
	}
	return b, err
}

// updateBot is the user-facing save path. Only execution-scope changes revoke
// work; scheduler preferences take effect under the same lock as admission.
func (s *Server) updateBot(b domain.Bot, scope *domain.ExecutionScope) (domain.Bot, error) {
	s.mu.Lock()
	return s.updateBotLocked(b, scope)
}

// updateBotLocked releases mu, including while waiting for revoked work.
func (s *Server) updateBotLocked(b domain.Bot, scope *domain.ExecutionScope) (domain.Bot, error) {
	b, err := s.normalizeBot(b)
	if err != nil {
		s.mu.Unlock()
		return b, err
	}
	if s.revoking[b.ID] > 0 {
		s.mu.Unlock()
		return b, errors.New("bot settings are changing; retry after saving finishes")
	}
	old, lookupErr := s.Bot(b.ID)
	if lookupErr != nil || sameBotScope(old, b) {
		b, err = s.saveBotLocked(b)
		s.mu.Unlock()
		return b, err
	}
	if scope != nil {
		if d := s.dispatches[scope.DispatchID]; d != nil && d.wake[b.ID] != nil {
			s.mu.Unlock()
			return b, errors.New("manage this active bot from a separate Echo chat")
		}
	}
	// Reserve the update before dropping the lock to wait for cancelled work.
	// Other settings writes fail promptly instead of waiting inside a run that
	// this update might itself be stopping.
	s.revoking[b.ID]++
	s.mu.Unlock()
	finish := s.stopBot(b.ID)
	defer finish()
	s.mu.Lock()
	b, err = s.saveBotLocked(b)
	s.revoking[b.ID]--
	s.mu.Unlock()
	if err == nil && s.SourceControl != nil {
		s.EditorFS.Invalidate(b.ID)
		s.EditorWatch.Refresh(b.ID)
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		// Each provider resets even when another provider is unavailable.
		_ = s.SourceControl.ResetWorkspace(ctx, b.ID)
		s.editorBroadcast(b.ID, map[string]any{"type": "source_control_resync_required", "botId": b.ID})
	}
	return b, err
}
