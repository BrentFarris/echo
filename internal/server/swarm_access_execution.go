package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

type accessGrant struct {
	Task    string                  `json:"task"`
	ID      string                  `json:"id"`
	Hash    string                  `json:"hash"`
	Bot     string                  `json:"bot"`
	Access  *domain.ExecutionAccess `json:"access"`
	Revoked bool                    `json:"revoked,omitempty"`
}

func (s *Server) checkScheduleTarget(a *domain.ExecutionAccess, id string) error {
	if id == "" || a == nil {
		return nil
	}
	var schedule calendar.Schedule
	if err := s.Store.Get("calendar:schedules", id, &schedule); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	return s.requireSharedBot(a, schedule.BotID)
}
func (s *Server) saveScheduleAccess(a *domain.ExecutionAccess) ([]string, error) {
	if a == nil {
		return nil, nil
	}
	data, err := json.Marshal(s.narrowAccess(a))
	if err != nil {
		return nil, err
	}
	id := invitationHash(string(data))
	return []string{id}, s.Store.Put("swarm-schedule-access", id, json.RawMessage(data))
}
func (s *Server) scheduleAccess(ids []string) (*domain.ExecutionAccess, error) {
	var merged *domain.ExecutionAccess
	for _, id := range ids {
		var a domain.ExecutionAccess
		if err := s.Store.Get("swarm-schedule-access", id, &a); err != nil || a.Origin == "" {
			return nil, denied("scheduleProvenance")
		}
		if merged == nil {
			merged = &a
			continue
		}
		for _, peer := range append([]string{a.Origin}, a.Via...) {
			if peer != merged.Origin && !slices.Contains(merged.Via, peer) {
				merged.Via = append(merged.Via, peer)
			}
		}
		merged.FullDenied = merged.FullDenied || a.FullDenied
		for _, deny := range a.Denied {
			if !slices.Contains(merged.Denied, deny) {
				merged.Denied = append(merged.Denied, deny)
			}
		}
		for _, p := range a.Parents {
			if !slices.Contains(merged.Parents, p) {
				merged.Parents = append(merged.Parents, p)
			}
		}
	}
	return merged, nil
}

func (s *Server) delegatedAccess(a *domain.ExecutionAccess, bot, task string) (*domain.ExecutionAccess, error) {
	if a == nil {
		return nil, nil
	}
	if len(a.Parents) >= 16 {
		return nil, errors.New("swarm delegation is limited to 16 generations")
	}
	if err := s.requireAccess(a, domain.AccessBots); err != nil {
		return nil, err
	}
	if err := s.requireSharedBot(a, bot); err != nil {
		return nil, err
	}
	child := s.narrowAccess(a)
	if !slices.Contains(child.Via, s.Swarm.Node().ID) {
		child.Via = append(child.Via, s.Swarm.Node().ID)
	}
	token := domain.ID() + domain.ID()
	grant := accessGrant{Task: task, ID: domain.ID(), Hash: invitationHash(token), Bot: bot, Access: s.narrowAccess(a)}
	if err := s.Store.Put("swarm-access-grants", grant.ID, grant); err != nil {
		return nil, err
	}
	child.Parents = append(child.Parents, domain.AccessParent{Task: task, Node: s.Swarm.Node().ID, ID: grant.ID, Token: token})
	return child, nil
}
func (s *Server) checkAccessParent(parent domain.AccessParent) error {
	var grant accessGrant
	if len(parent.Token) != 64 || s.Store.Get("swarm-access-grants", parent.ID, &grant) != nil || grant.Task == "" || grant.Task != parent.Task || grant.Revoked || subtle.ConstantTimeCompare([]byte(grant.Hash), []byte(invitationHash(parent.Token))) != 1 {
		return denied("delegationRevoked")
	}
	if err := s.requireAccess(grant.Access, domain.AccessBots); err != nil {
		return err
	}
	return s.requireSharedBot(grant.Access, grant.Bot)
}
func (s *Server) validateAccessParents(ctx context.Context, a *domain.ExecutionAccess) error {
	if a == nil {
		return nil
	}
	if len(a.Parents) > 16 || len(a.Via) > 32 || len(a.Denied) > len(domain.SwarmPermissions) {
		return denied("invalidDelegation")
	}
	for _, parent := range a.Parents {
		if parent.Node == s.Swarm.Node().ID {
			if err := s.checkAccessParent(parent); err != nil {
				return err
			}
			continue
		}
		if !s.Swarm.Authorized(parent.Node) {
			return denied("delegationPeer")
		}
		child, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := s.Swarm.JSON(child, parent.Node, "POST", "/api/v1/swarm/workers/access-check", parent, nil)
		cancel()
		if err != nil {
			return &accessError{"delegationRevoked", "Delegated work cannot continue until its parent authorization is available: " + err.Error()}
		}
	}
	return nil
}
func (s *Server) accessGrantRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/swarm/workers/access-check", func(w http.ResponseWriter, r *http.Request) {
		if p, ok := swarm.Caller(r.Context()); !ok || p.Pairing {
			accessErrorResponse(w, denied("peerRequired"))
			return
		}
		var parent domain.AccessParent
		if err := decode(w, r, &parent); err != nil {
			respond(w, nil, err)
			return
		}
		if parent.Node != s.Swarm.Node().ID {
			respond(w, nil, denied("delegationIdentity"))
			return
		}
		respond(w, true, s.checkAccessParent(parent))
	})
}

func accessFingerprint(req workerRequest, fingerprint string) string {
	data, _ := json.Marshal(struct {
		Fingerprint, ID, Origin, Home, Token string
		Runs                                 []domain.Run
		Access                               *domain.ExecutionAccess
	}{fingerprint, req.ID, req.Origin, req.Home, req.Token, req.Runs, req.Access})
	return "swarm-access/1:" + invitationHash(string(data))
}
func (s *Server) workerAccess(req workerRequest) *domain.ExecutionAccess {
	a := req.Access
	if a == nil {
		a = &domain.ExecutionAccess{Origin: req.Origin}
	}
	copy := *a
	copy.Via = slices.Clone(a.Via)
	if req.Home != s.Swarm.Node().ID && !slices.Contains(copy.Via, req.Home) {
		copy.Via = append(copy.Via, req.Home)
	}
	if len(s.accessPrincipals(&copy)) == 0 && len(copy.Parents) == 0 && !copy.FullDenied && len(copy.Denied) == 0 {
		return nil
	}
	return &copy
}
func (s *Server) authorizeWorker(req workerRequest, fingerprint string) error {
	if req.Access != nil && (!noteNodeID.MatchString(req.Access.Origin) || req.Origin != req.Home && req.Access.Origin != req.Origin) {
		return denied("invalidDelegation")
	}
	if req.Access != nil && req.Home != s.Swarm.Node().ID {
		if !s.peerSupports(req.Home, "swarm-access/1") {
			return denied("updateRequired")
		}
		if err := s.Swarm.VerifyProof(req.Home, req.AccessPermit, accessFingerprint(req, fingerprint)); err != nil {
			return denied("invalidDelegation")
		}
	}
	a := s.workerAccess(req)
	if err := s.requireAccess(a, domain.AccessBots); err != nil {
		return err
	}
	if !s.executionFull(a) {
		if req.Access == nil && req.Origin != req.Home {
			return denied("missingDelegation")
		}
		for _, peer := range s.accessPrincipals(a) {
			if !s.peerSupports(peer, "swarm-access/1") {
				return &accessError{"updateRequired", "Update all participating machines to run bots with restricted swarm access"}
			}
		}
	}
	if err := s.validateAccessParents(s.ctx, a); err != nil {
		return err
	}
	for _, run := range req.Runs {
		if err := s.requireSharedBot(a, run.BotID); err != nil {
			return err
		}
		b, err := s.Bot(run.BotID)
		if err != nil {
			return err
		}
		g := b.OverrideGeneration(run.Generation)
		if g.EndpointID == "" {
			g = s.defaultGeneration()
		}
		var endpoint domain.Endpoint
		_ = s.Store.Get("endpoints", g.EndpointID, &endpoint)
		if err := s.requireModelAccess(a, b, endpoint); err != nil {
			return fmt.Errorf("@%s: %w", b.Handle, err)
		}
	}
	return nil
}

func (s *Server) authorizeTool(scope domain.ExecutionScope, method string, raw json.RawMessage) error {
	a := scope.Access
	if a == nil {
		return nil
	}
	if err := s.requireSharedBot(a, scope.Bot.ID); err != nil {
		return err
	}
	if !scope.Standalone {
		if err := s.requireAccess(a, domain.AccessBots); err != nil {
			return err
		}
	}
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	permission := ""
	switch method {
	case "bot_save", "bot_delete", "workspace.bind":
		permission = domain.AccessManageBots
	case "model_save":
		permission = domain.AccessModels
	case "plugin.install", "plugin.configure", "plugin.rollback":
		permission = domain.AccessManagePlugins
	case "calendar.save", "calendar.delete":
		permission = domain.AccessSchedules
	case "workspace_skill_search", "workspace_skill_read", "workspace_skill_save",
		"file_read", "file_read_image", "file_write", "file_list", "file_search", "file_delete", "file_move", "file_mkdir",
		"chat_search", "bot_send", "bot_reply", "bot_wait", "bot_list", "model_list", AskUserQuestionsToolName,
		"chat.start", "confluence.search", "confluence.read", "confluence.users.search", "confluence.users.current", "jira.search", "jira.issue", "jira.comments",
		"comfyui.generate", "comfyui.generate_video", "comfyui.save_image", "comfyui.save_video", "git.run", "fossil.run", "p4.run", "go.run", "go.inspect",
		"model.complete", "model.request", "model.emit", "plugin.template", "plugin.list", "plugin.open", "calendar.list", "calendar.missed",
		"file.read", "file.write", "file.delete", "file.move", "file.list", "file.search", "file.mkdir", "storage.get", "storage.set", "storage.delete",
		"web.get", "web.request", "websocket.open", "websocket.send", "websocket.receive", "websocket.close", "web.search", "process.run":
		// Ordinary bot tools retain the bot's existing scope. Direct use is checked below.
	default:
		if !s.executionFull(a) {
			return denied("unclassifiedTool")
		}
	}
	if method == "plugin.configure" && (text(args, "id") == "swarm" || text(args, "id") == "hosted-server") {
		if err := s.requireAccess(a, domain.AccessMachine); err != nil {
			return err
		}
	}
	if permission != "" {
		if err := s.requireAccess(a, permission); err != nil {
			return err
		}
	}
	if method == "plugin.configure" || method == "plugin.rollback" || method == "plugin.open" {
		if id := text(args, "id"); id != "" {
			if err := s.requireSharedPlugin(a, id); err != nil {
				return err
			}
		}
	}
	if method == "calendar.save" || method == "calendar.delete" {
		if err := s.checkScheduleTarget(a, text(args, "id")); err != nil {
			return err
		}
	}
	if method == "bot_save" && args["swarmAvailable"] != nil {
		return denied("ownerSession")
	}
	if method == "bot_save" || method == "bot_delete" || method == "workspace.bind" || method == "calendar.save" {
		target := text(args, "id")
		if method == "workspace.bind" || method == "calendar.save" {
			target = text(args, "botId")
		}
		if target != "" {
			b, err := s.findBot(target)
			if err != nil {
				if method != "bot_save" {
					return err
				}
			} else if err = s.requireSharedBot(a, b.ID); err != nil {
				return err
			}
		}
	}
	if method == "process.run" || method == "go.run" || method == "go.inspect" || method == "git.run" || method == "fossil.run" || method == "p4.run" {
		if len(scope.Bot.Roots) == 0 {
			if err := s.requireFull(a); err != nil {
				return err
			}
		}
	}
	if scope.Standalone {
		if err := s.requireSharedPlugin(a, scope.PluginID); err != nil {
			return err
		}
		switch method {
		case "file.read", "file.list", "file.search":
			permission = domain.AccessRead
		case "file.write", "file.delete", "file.move", "file.mkdir":
			permission = domain.AccessWrite
		case "process.run", "go.run", "go.inspect", "git.run", "fossil.run", "p4.run":
			for _, p := range []string{domain.AccessRead, domain.AccessWrite, domain.AccessExecute} {
				if err := s.requireAccess(a, p); err != nil {
					return err
				}
			}
		}
		if permission != "" {
			return s.requireAccess(a, permission)
		}
	}
	// A plugin installed by a remote bot cannot smuggle protected sharing fields
	// through an arbitrary manifest (the target's sharing is owner-controlled).
	if method == "plugin.install" {
		if manifest, ok := args["manifest"].(map[string]any); ok {
			if _, exists := manifest["swarmAvailable"]; exists {
				return denied("ownerSession")
			}
		}
	}
	return nil
}

func (s *Server) visibleCoreTools(scope domain.ExecutionScope) []domain.Tool {
	out := []domain.Tool{}
	for _, tool := range s.coreTools(scope.Bot) {
		if s.authorizeTool(scope, tool.Name, json.RawMessage(`{}`)) == nil {
			out = append(out, tool)
		}
	}
	return out
}

func (s *Server) restrictedPluginTools(scope domain.ExecutionScope) []domain.Tool {
	out := []domain.Tool{}
	for _, tool := range s.Plugins.Tools(scope.Bot) {
		// A WASM guest can execute its own code. Every host call it makes must
		// still pass authorizeTool with the resolved host capability.
		if tool.Capability == "" || s.authorizeTool(scope, tool.Capability, json.RawMessage(`{}`)) == nil {
			out = append(out, tool)
		}
	}
	return out
}
