package server

import (
	"net/http"
	"slices"
	"strings"

	"github.com/brent/echo/internal/domain"
)

func (s *Server) authorizeSwarmHTTP(r *http.Request, pattern string) error {
	a := s.requestAccess(r)
	if a == nil {
		return nil
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	parts := strings.Split(path, "/")
	if path == "swarm/access" || strings.HasPrefix(path, "swarm/access/") || strings.HasPrefix(path, "swarm/invitations") || strings.HasSuffix(path, "/access") && strings.HasPrefix(path, "swarm/peers/") {
		return denied("ownerSession")
	}
	if pattern == "" {
		if s.executionFull(a) {
			return nil
		}
		return denied("unclassifiedOperation")
	}
	bot := ""
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "bots" {
			bot = parts[i+1]
			break
		}
	}
	if bot == "" {
		bot = r.URL.Query().Get("botId")
	}
	if bot != "" {
		if err := s.requireSharedBot(a, bot); err != nil {
			return err
		}
	}
	if (strings.HasPrefix(path, "development") && r.Method != "GET") || path == "hosting/password" || path == "external-agents/reset" || path == "swarm/peers" && r.Method == "POST" {
		if err := s.requireFull(a); err != nil {
			return err
		}
	}
	if (path == "plugins/swarm" || path == "plugins/hosted-server") && r.Method == "PATCH" {
		if err := s.requireAccess(a, domain.AccessMachine); err != nil {
			return err
		}
	}
	if parts[0] == "schedules" || path == "missed/review" {
		if err := s.requireSharedPlugin(a, "calendar"); err != nil {
			return err
		}
	}
	if parts[0] == "hosting" {
		if err := s.requireSharedPlugin(a, "hosted-server"); err != nil {
			return err
		}
	}
	if parts[0] == "plugins" && len(parts) > 1 {
		if err := s.requireSharedPlugin(a, parts[1]); err != nil {
			return err
		}
		if len(parts) > 2 && (parts[2] == "views" || parts[2] == "network") && bot == "" {
			if err := s.requireSharedBot(a, "echo"); err != nil {
				return err
			}
		}
	}
	permissions, full := swarmRouteAccess(pattern)
	if permissions == nil && !full {
		if s.executionFull(a) {
			return nil
		}
		return denied("unclassifiedOperation")
	}
	if full {
		if err := s.requireFull(a); err != nil {
			return err
		}
	}
	for _, permission := range permissions {
		if err := s.requireAccess(a, permission); err != nil {
			return err
		}
	}
	// Executables used by direct editor operations need both filesystem grants.
	if slices.Contains(permissions, domain.AccessExecute) {
		if err := s.requireAccess(a, domain.AccessRead); err != nil {
			return err
		}
		if err := s.requireAccess(a, domain.AccessWrite); err != nil {
			return err
		}
		if bot != "" {
			b, err := s.Bot(bot)
			if err != nil {
				return err
			}
			if len(b.Roots) == 0 {
				return s.requireFull(a)
			}
		}
	}
	return nil
}

// Empty non-nil requirements denote explicitly shared inventory/history. New
// routes are unavailable to restricted peers until classified here.
func swarmRouteAccess(pattern string) ([]string, bool) {
	allow := []string{}
	method, path, _ := strings.Cut(pattern, " ")
	path = strings.TrimPrefix(path, "/api/v1/")
	one := func(value string) ([]string, bool) { return []string{value}, false }
	switch pattern {
	case "GET /api/v1/state", "GET /api/v1/events", "GET /api/v1/health", "GET /api/v1/swarm/peers", "POST /api/v1/swarm/revoke",
		"GET /api/v1/bot-groups", "GET /api/v1/slash-commands", "GET /api/v1/slash-commands/catalog", "GET /api/v1/usage",
		"GET /api/v1/chats/{id}/messages", "GET /api/v1/chats/{id}/snapshot", "GET /api/v1/chats/{id}/attachments/{attachment}",
		"POST /api/v1/chats/{id}/read", "POST /api/v1/chats/{id}/unread", "GET /api/v1/notifications/feed", "GET /api/v1/notifications/config":
		return allow, false
	case "POST /api/v1/chats", "POST /api/v1/chats/{id}/messages", "POST /api/v1/chats/{id}/messages/{messageId}/rerun", "POST /api/v1/chats/{id}/messages/{messageId}/rerun-input",
		"PUT /api/v1/chats/{id}/generation", "POST /api/v1/chats/{id}/questions/{messageId}", "POST /api/v1/chats/{id}/attachments", "DELETE /api/v1/chats/{id}/attachments/{attachment}":
		return one(domain.AccessBots)
	case "POST /api/v1/references", "POST /api/v1/references/validate", "POST /api/v1/references/open", "POST /api/v1/references/reveal",
		"POST /api/v1/chats/{id}/references/open", "POST /api/v1/chats/{id}/references/reveal", "POST /api/v1/chats/{id}/files/open", "POST /api/v1/chats/{id}/files/reveal":
		return one(domain.AccessRead)
	case "POST /api/v1/chats/{id}/messages/{messageId}/resubmit":
		return []string{domain.AccessChats, domain.AccessBots}, false
	case "DELETE /api/v1/chats/{id}", "POST /api/v1/chats/{id}/restore", "DELETE /api/v1/chats/{id}/permanent", "POST /api/v1/chats/{id}/stop",
		"POST /api/v1/chats/{id}/messages/{messageId}/fork", "DELETE /api/v1/chats/{id}/messages/{messageId}", "PATCH /api/v1/chats/{id}/messages/{messageId}":
		return one(domain.AccessChats)
	case "POST /api/v1/bots", "DELETE /api/v1/bots/{id}", "POST /api/v1/bot-groups", "PUT /api/v1/bot-groups/{id}", "DELETE /api/v1/bot-groups/{id}":
		return one(domain.AccessManageBots)
	case "POST /api/v1/slash-commands/{id}/invoke":
		return allow, false // Host capability checks the resolved tool and bot.
	case "POST /api/v1/slash-commands", "PUT /api/v1/slash-commands/{id}", "DELETE /api/v1/slash-commands/{id}", "POST /api/v1/slash-commands/{id}/restore":
		return one(domain.AccessManagePlugins)
	case "GET /api/v1/endpoints/{id}/models":
		return one(domain.AccessBots)
	case "POST /api/v1/endpoints", "DELETE /api/v1/endpoints/{id}", "POST /api/v1/endpoints/{id}/check", "POST /api/v1/external-agents/reset":
		return one(domain.AccessModels)
	case "POST /api/v1/schedules", "DELETE /api/v1/schedules/{id}", "POST /api/v1/schedules/{id}/run", "POST /api/v1/missed/review":
		return one(domain.AccessSchedules)
	case "PATCH /api/v1/plugins/{id}", "POST /api/v1/plugins/{id}/approve", "POST /api/v1/plugins/{id}/rollback", "PUT /api/v1/plugins/{id}/app/dock":
		return one(domain.AccessManagePlugins)
	case "GET /api/v1/plugins/{id}/views/{view}", "POST /api/v1/plugins/{id}/bridge", "GET /api/v1/plugins/{id}/network":
		return allow, false
	}
	// Worker identity, request proof and resource checks also apply inside each RPC.
	switch method + " " + path {
	case "POST swarm/workers/control", "POST swarm/workers/stream", "POST swarm/workers/snapshot", "POST swarm/workers/access-check", "POST swarm/workers/group-receipt":
		return allow, false
	case "POST swarm/workers/validate", "POST swarm/workers/prepare", "POST swarm/workers/start", "POST swarm/workers/group", "POST swarm/workers/group-slot", "POST swarm/workers/group-rotation", "POST swarm/workers/group-permits", "POST swarm/workers/group-validate":
		return one(domain.AccessBots)
	case "GET hosting/sessions", "DELETE hosting/sessions/{id}", "POST hosting/password", "POST swarm/peers", "PATCH swarm/peers/{id}", "DELETE swarm/peers/{id}", "POST usage/reset",
		"GET development", "PUT development", "GET development/update-status", "GET development/operations/{id}", "POST development/check-updates", "POST development/update-relaunch", "POST development/build-relaunch", "POST development/relaunch", "POST development/terminate",
		"POST development/update-target", "POST development/updates/check", "POST development/updates/{id}/prepare", "GET development/updates/{id}", "POST development/updates/{id}/{action}", "POST development/rollouts/check", "GET development/rollouts", "GET development/rollouts/{id}", "POST development/rollouts", "POST development/rollouts/{id}/{action}",
		"POST notifications/devices", "DELETE notifications/devices/{id}", "GET notifications/devices/{id}/pending", "POST notifications/devices/{id}/claim", "POST notifications/devices/{id}/test":
		return one(domain.AccessMachine)
	case "GET plugins/notes/notes", "POST plugins/notes/notes", "GET plugins/notes/notes/{noteId}", "PUT plugins/notes/notes/{noteId}", "DELETE plugins/notes/notes/{noteId}":
		return allow, false
	case "POST plugins/jira/credential", "POST plugins/p4/credential", "POST plugins/confluence/credential":
		return one(domain.AccessModels)
	case "POST plugins/jira/test", "POST plugins/confluence/test":
		return one(domain.AccessManagePlugins)
	case "POST plugins/go/bots/{botId}/language/install":
		return []string{domain.AccessExecute, domain.AccessManagePlugins}, false
	case "GET plugins/go/bots/{botId}/language/source":
		return one(domain.AccessRead)
	case "GET plugins/go/bots/{botId}/language/session", "GET plugins/go/bots/{botId}/language/status":
		return one(domain.AccessExecute)
	}
	// Terminals and the native debugger run as the OS owner. No custom
	// policy can grant access, including to sessions started before pairing.
	if strings.HasPrefix(path, "plugins/terminal/bots/{botId}/") || strings.HasPrefix(path, "plugins/monaco-editor/bots/{botId}/debug/") || strings.HasPrefix(path, "plugins/go/bots/{botId}/debug/") {
		return allow, true
	}
	if strings.HasPrefix(path, "plugins/monaco-editor/bots/{botId}/") {
		suffix := strings.TrimPrefix(path, "plugins/monaco-editor/bots/{botId}/")
		switch method + " " + suffix {
		case "POST inline-chat":
			return []string{domain.AccessBots, domain.AccessRead}, false
		case "GET events", "GET bookmarks", "POST bookmarks", "GET fs/roots", "GET fs/entries", "GET fs/file", "GET fs/trash", "GET fs/media", "GET fs/search", "POST fs/text-search", "POST fs/reveal":
			return one(domain.AccessRead)
		case "PUT fs/file", "POST fs/entries", "PATCH fs/entry", "DELETE fs/entry", "POST fs/trash/{trashId}/restore", "DELETE fs/trash/{trashId}", "POST fs/text-replace":
			return one(domain.AccessWrite)
		}
		switch method + " " + suffix {
		case "PUT source-control/settings":
			return []string{domain.AccessManageBots, domain.AccessExecute}, false
		case "POST source-control/p4/credential":
			return one(domain.AccessModels)
		case "GET source-control/settings", "GET source-control/repositories", "GET source-control/repositories/{repositoryId}/status", "GET source-control/repositories/{repositoryId}/metadata", "GET source-control/repositories/{repositoryId}/diff", "GET source-control/repositories/{repositoryId}/detail", "GET source-control/repositories/{repositoryId}/history", "GET source-control/repositories/{repositoryId}/ui/{path...}", "POST source-control/repositories/{repositoryId}/actions", "POST source-control/initialize", "POST source-control/clone":
			return one(domain.AccessExecute) // Native programs may run even during a repository read.
		}
	}
	return nil, false
}
