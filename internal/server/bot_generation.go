package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/externalagent"
	"github.com/brent/echo/internal/model"
)

func (s *Server) requireModelAccess(access *domain.ExecutionAccess, ep domain.Endpoint) error {
	if ep.External() && !s.executionFull(access) {
		reason := fmt.Sprintf("The %q external agent on %s runs as the machine owner and requires Full access.", ep.Name, s.Swarm.Node().Name)
		return &accessError{"fullAccess", reason + " Select an HTTP model connection to use this bot with shared access."}
	}
	return nil
}

func (s *Server) normalizeBotGeneration(b domain.Bot) (domain.Bot, error) {
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
	if err := s.requireModelAccess(access, ep); err != nil {
		return g, ep, err
	}
	if g.Model == "" {
		g.Model = ep.Model
	}
	return g, ep, s.validateGeneration(bot, g, ep, query)
}
