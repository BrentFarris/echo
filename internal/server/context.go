package server

import (
	"context"

	"github.com/brent/echo/internal/contextmanager"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func (s *Server) contextCompletion(key string) contextmanager.CompleteFunc {
	return func(ctx context.Context, e domain.Endpoint, g domain.Generation, messages []model.Message, tools []domain.Tool, emit func(model.Delta)) (model.Result, error) {
		return s.complete(ctx, e, g, messages, tools, key, emit)
	}
}

func (b *streamBuffer) reset() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.m.Content, b.m.Reasoning = "", ""
	b.m.Calls, b.m.Usage = nil, nil
	b.content, b.reasoning = "", ""
	// FinishMessage replaces the snapshot and removes old chunks transactionally;
	// the message remains running until the accepted attempt finishes.
	if b.err = b.s.Store.FinishMessage(b.m); b.err != nil {
		return b.err
	}
	b.s.mu.Lock()
	b.s.streams[b.m.ID] = b.m
	b.s.emitLocked("message", b.m.ChatID, b.m.RunID, b.m.BotID, b.m)
	b.s.mu.Unlock()
	return nil
}
