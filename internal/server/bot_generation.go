package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/externalagent"
	"github.com/brent/echo/internal/model"
)

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

// resolveGeneration expands endpoint defaults without applying live bot preferences.
func (s *Server) resolveGeneration(g domain.Generation) (domain.Generation, domain.Endpoint, error) {
	var ep domain.Endpoint
	if g.EndpointID == "" || s.Store.Get("endpoints", g.EndpointID, &ep) != nil {
		return g, ep, fmt.Errorf("model connection %q is unavailable; configure and select a model", g.EndpointID)
	}
	if g.Model == "" {
		g.Model = ep.Model
	}
	return g, ep, nil
}
