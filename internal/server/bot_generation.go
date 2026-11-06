package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/externalagent"
	"github.com/brent/echo/internal/model"
)

func (s *Server) requireModelAccess(access *domain.ExecutionAccess, bot domain.Bot, ep domain.Endpoint) error {
	if ep.External() && !s.executionFull(access) {
		return s.requireSharedCLI(access, bot, ep)
	}
	return nil
}

func (s *Server) requireSharedCLI(access *domain.ExecutionAccess, bot domain.Bot, ep domain.Endpoint) error {
	if !bot.SharesPinnedCLI() || bot.SharedCLIEndpointID != ep.ID {
		reason := fmt.Sprintf("The %q external agent on %s runs as the machine owner and requires Full access.", ep.Name, s.Swarm.Node().Name)
		return &accessError{"fullAccess", reason + " The owner can pin this connection in Settings → Bots → Edit and enable “Allow peers to run this bot’s CLI without Full access”, or select an HTTP model."}
	}
	if err := s.requireAccess(access, domain.AccessBots); err != nil {
		return err
	}
	return s.requireSharedBot(access, bot.ID)
}

func (s *Server) normalizeBotGeneration(b domain.Bot) (domain.Bot, error) {
	if b.SharedCLIEndpointID != "" && !b.SharesPinnedCLI() {
		return b, errors.New("sharing a CLI requires a shared bot with that specific connection pinned")
	}
	if b.ThinkingOverride != nil {
		v := strings.TrimSpace(*b.ThinkingOverride)
		b.ThinkingOverride = nil
		if v != "" {
			if err := model.ValidateSettings(domain.Endpoint{ReasoningEffort: v}); err != nil {
				return b, err
			}
			b.ThinkingOverride = &v
		}
	}
	if b.ModelOverride != nil {
		v := *b.ModelOverride
		v.EndpointID, v.Model = strings.TrimSpace(v.EndpointID), strings.TrimSpace(v.Model)
		var ep domain.Endpoint
		if v.EndpointID == "" || s.Store.Get("endpoints", v.EndpointID, &ep) != nil {
			return b, errors.New("choose an available model override connection on this machine")
		}
		if b.SharedCLIEndpointID != "" && !ep.External() {
			return b, errors.New("only a pinned external CLI connection can be shared this way")
		}
		if v.Model == "" && ep.Model == "" && !ep.External() {
			return b, errors.New("select a model name for the bot override")
		}
		if b.ThinkingOverride != nil && ep.External() {
			if err := externalagent.ValidateThinking(ep.Kind, *b.ThinkingOverride); err != nil {
				return b, err
			}
		}
		b.ModelOverride = &v
	}
	return b, nil
}

// Validate the same endpoint snapshot that the run or slot probe will use.
// Callers choose whether to apply live bot overrides or use captured settings.
func (s *Server) resolveGeneration(bot domain.Bot, g domain.Generation, access *domain.ExecutionAccess, query *slotQuery) (domain.Generation, domain.Endpoint, error) {
	var ep domain.Endpoint
	if g.EndpointID == "" || s.Store.Get("endpoints", g.EndpointID, &ep) != nil {
		return g, ep, fmt.Errorf("model connection %q is unavailable on %s. Configure and select a model", g.EndpointID, s.Swarm.Node().Name)
	}
	if err := s.requireModelAccess(access, bot, ep); err != nil {
		return g, ep, err
	}
	if g.Model == "" {
		g.Model = ep.Model
	}
	return g, ep, s.validateGeneration(bot, g, ep, query)
}
