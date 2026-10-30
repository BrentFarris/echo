package server

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/brent/echo/internal/domain"
)

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
