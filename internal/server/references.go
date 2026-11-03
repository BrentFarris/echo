package server

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func (s *Server) fileReferences(ctx context.Context, bots []domain.Bot, query string) ([]broker.Entry, broker.ReferenceStatus, error) {
	status := broker.ReferenceStatus{}
	allBots, err := s.Bots()
	if err != nil {
		return nil, status, err
	}
	entries := map[string]broker.Entry{}
	for _, bot := range bots {
		roots := append([]string{}, bot.Roots...)
		direct := len(roots) == 0 && filepath.IsAbs(query)
		if direct {
			roots = []string{filepath.Dir(query)}
		} else if len(roots) == 0 {
			for _, other := range allBots {
				roots = append(roots, other.Roots...)
			}
		}
		seen := map[string]bool{}
		for _, root := range roots {
			if seen[root] {
				continue
			}
			seen[root] = true
			var found []broker.Entry
			var scopeStatus broker.ReferenceStatus
			if direct {
				// Unrestricted references outside known workspaces stay shallow;
				// a drive/home directory must never create a whole-disk index.
				found, err = s.Files.DirectReferences(ctx, bot, root, query)
			} else {
				found, scopeStatus, err = s.References.Search(ctx, bot, root, query)
			}
			if ctx.Err() != nil {
				return nil, status, ctx.Err()
			}
			status.Indexing = status.Indexing || scopeStatus.Indexing
			status.Incomplete = status.Incomplete || scopeStatus.Incomplete || err != nil
			for _, entry := range found {
				key := strings.ToLower(entry.Path)
				old, ok := entries[key]
				allowed := old.AllowedBots
				if !slices.Contains(allowed, bot.ID) {
					allowed = append(allowed, bot.ID)
				}
				if ok && old.Score >= entry.Score {
					entry = old
				}
				entry.AllowedBots = allowed
				entries[key] = entry
			}
		}
	}
	out := make([]broker.Entry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	broker.SortReferences(out)
	if len(out) > broker.ReferenceLimit {
		out = out[:broker.ReferenceLimit]
	}
	// Label access independently of which root supplied the hit. A shared folder
	// can be accessible to a bot even when it wasn't in that bot's top results.
	allowed := map[string][]string{}
	for _, entry := range out {
		allowed[entry.Path] = entry.AllowedBots
	}
	for _, bot := range bots {
		unverified := []broker.Entry{}
		for _, entry := range out {
			if !slices.Contains(entry.AllowedBots, bot.ID) {
				unverified = append(unverified, entry)
			}
		}
		for _, entry := range s.Files.FilterReferences(ctx, bot, unverified) {
			allowed[entry.Path] = append(allowed[entry.Path], bot.ID)
		}
	}
	valid := []broker.Entry{}
	for _, entry := range out {
		entry.AllowedBots = allowed[entry.Path]
		if len(entry.AllowedBots) > 0 {
			valid = append(valid, entry)
		}
	}
	return valid, status, ctx.Err()
}
