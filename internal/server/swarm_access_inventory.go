package server

import (
	"context"
	"slices"

	"github.com/brent/echo/internal/domain"
)

func (s *Server) sharedBots(a *domain.ExecutionAccess, bots []domain.Bot) []domain.Bot {
	if a == nil {
		return bots
	}
	return slices.DeleteFunc(bots, func(b domain.Bot) bool { return s.requireSharedBot(a, b.ID) != nil })
}
func (s *Server) sharedPlugins(a *domain.ExecutionAccess, plugins []domain.PluginManifest) []domain.PluginManifest {
	if a == nil {
		return plugins
	}
	return slices.DeleteFunc(plugins, func(p domain.PluginManifest) bool { return s.requireSharedPlugin(a, p.ID) != nil })
}
func (s *Server) sharedCommands(a *domain.ExecutionAccess, commands []domain.SlashCommand) []domain.SlashCommand {
	if a == nil {
		return commands
	}
	return slices.DeleteFunc(commands, func(c domain.SlashCommand) bool {
		return c.Action.PluginID != "" && s.requireSharedPlugin(a, c.Action.PluginID) != nil || c.Action.BotID != "" && s.requireSharedBot(a, c.Action.BotID) != nil
	})
}
func (s *Server) sharedGroups(ctx context.Context, groups []domain.BotGroup) []domain.BotGroup {
	a := contextAccess(ctx)
	if a == nil {
		return groups
	}
	out := []domain.BotGroup{}
	for _, g := range groups {
		n := len(g.Members)
		g.Members = slices.DeleteFunc(slices.Clone(g.Members), func(m domain.BotGroupMember) bool {
			return (m.NodeID == "" || m.NodeID == s.Swarm.Node().ID) && s.requireSharedBot(a, m.BotID) != nil
		})
		if len(g.Members) > 0 && (len(g.Members) == n || g.DeliveryMode == "one") {
			out = append(out, g)
		}
	}
	return out
}
