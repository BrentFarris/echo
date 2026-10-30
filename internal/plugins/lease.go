package plugins

import (
	"context"
	"errors"

	"github.com/brent/echo/internal/domain"
)

// Lease extends Save's immediate revocation to interactive app operations.
func (h *Host) Lease(ctx context.Context, bot domain.Bot, id, digest string) (context.Context, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, err := h.Get(id)
	if err != nil {
		return nil, nil, err
	}
	if !h.Enabled(bot, id) || m.Digest != digest {
		return nil, nil, errors.New("plugin is inactive or changed; reopen its view")
	}
	ctx, cancel := context.WithCancel(ctx)
	key := domain.ID()
	if h.active[id] == nil {
		h.active[id] = map[string]context.CancelFunc{}
	}
	h.active[id][key] = cancel
	return ctx, func() { cancel(); h.mu.Lock(); delete(h.active[id], key); h.mu.Unlock() }, nil
}
