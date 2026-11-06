package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

type accessError struct{ Permission, Reason string }

func (e *accessError) Error() string { return e.Reason }
func denied(permission string) error {
	reason := map[string]string{domain.AccessBots: "running bots", domain.AccessRead: "direct file reading", domain.AccessWrite: "direct file changes", domain.AccessExecute: "direct execution", domain.AccessManageBots: "bot and group administration", domain.AccessManagePlugins: "plugin administration", domain.AccessModels: "model and credential administration", domain.AccessSchedules: "scheduled work", domain.AccessChats: "chat changes", domain.AccessMachine: "machine administration", "ownerSession": "changing sharing outside the owning computer’s owner session"}[permission]
	if reason == "" {
		reason = permission
	}
	return &accessError{permission, "The owner has not allowed " + reason}
}
func accessErrorResponse(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	var a *accessError
	_ = errors.As(err, &a)
	permission := ""
	if a != nil {
		permission = a.Permission
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "code": "swarm_access_denied", "permission": permission})
}

type accessContextKey struct{}
type accessLease struct {
	access *domain.ExecutionAccess
	cancel context.CancelFunc
}
type terminalAccess struct{ bot, session string }
type resourceGrant struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Shared bool   `json:"shared"`
}

func (s *Server) invalidatePolicies() { s.policyMu.Lock(); s.policyCache = nil; s.policyMu.Unlock() }
func contextAccess(ctx context.Context) *domain.ExecutionAccess {
	a, _ := ctx.Value(accessContextKey{}).(*domain.ExecutionAccess)
	return a
}
func (s *Server) accessContext(ctx context.Context, a *domain.ExecutionAccess) context.Context {
	ctx = context.WithValue(ctx, accessContextKey{}, a)
	if a != nil {
		ctx = broker.RemoteExecution(ctx)
	}
	if a != nil && !s.executionFull(a) {
		ctx = broker.RestrictedExecution(ctx)
	}
	return ctx
}
func (s *Server) trackAccess(ctx context.Context, a *domain.ExecutionAccess) (context.Context, func()) {
	ctx, cancel := context.WithCancel(s.accessContext(ctx, a))
	lease := &accessLease{a, cancel}
	s.accessMu.Lock()
	if s.accessRequests == nil {
		s.accessRequests = map[*accessLease]bool{}
	}
	s.accessRequests[lease] = true
	s.accessMu.Unlock()
	return ctx, func() { cancel(); s.accessMu.Lock(); delete(s.accessRequests, lease); s.accessMu.Unlock() }
}

func (s *Server) accessPolicy(peer string) (domain.SwarmPolicy, error) {
	var p domain.SwarmPolicy
	id := peer
	if id == "" {
		id = "defaults"
	}
	err := s.Store.Get("swarm-access", id, &p)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return p, err
}
func validateAccessPolicy(p domain.SwarmPolicy) error {
	for name := range p.Permissions {
		if !slices.Contains(domain.SwarmPermissions, name) {
			return fmt.Errorf("unknown swarm permission %q", name)
		}
	}
	if len(p.Bots)+len(p.Plugins) > 10000 {
		return errors.New("too many sharing overrides")
	}
	for _, resources := range []map[string]bool{p.Bots, p.Plugins} {
		for id, allowed := range resources {
			if id == "" || len(id) > 128 || allowed {
				return errors.New("resource overrides must exclude a valid bot or plugin ID; omit to inherit")
			}
		}
	}
	return nil
}
func (s *Server) effectiveAccess(peer string) domain.SwarmAccess {
	a := domain.SwarmAccess{Permissions: map[string]bool{}, Bots: map[string]bool{}, Plugins: map[string]bool{}, Full: true, Owner: peer == "" || peer == s.Swarm.Node().ID}
	for _, name := range domain.SwarmPermissions {
		a.Permissions[name] = true
	}
	if a.Owner {
		return a
	}
	s.policyMu.RLock()
	cached, ok := s.policyCache[peer]
	s.policyMu.RUnlock()
	if ok {
		return cached
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	if cached, ok := s.policyCache[peer]; ok {
		return cached
	}
	defaults, e1 := s.accessPolicy("")
	override, e2 := s.accessPolicy(peer)
	if e1 != nil || e2 != nil {
		for name := range a.Permissions {
			a.Permissions[name] = false
		}
	} else {
		maps.Copy(a.Permissions, defaults.Permissions)
		maps.Copy(a.Permissions, override.Permissions)
		maps.Copy(a.Bots, defaults.Bots)
		maps.Copy(a.Bots, override.Bots)
		maps.Copy(a.Plugins, defaults.Plugins)
		maps.Copy(a.Plugins, override.Plugins)
	}
	rows, err := s.Store.List("swarm-resource-sharing")
	if err != nil {
		for key := range a.Permissions {
			a.Permissions[key] = false
		}
	}
	for _, raw := range rows {
		var g resourceGrant
		if json.Unmarshal(raw, &g) == nil && !g.Shared {
			if g.Kind == "bots" {
				a.Bots[g.ID] = false
			} else {
				a.Plugins[g.ID] = false
			}
		}
	}
	for _, allowed := range a.Permissions {
		a.Full = a.Full && allowed
	}
	a.Full = a.Full && len(a.Bots) == 0 && len(a.Plugins) == 0
	if s.policyCache == nil {
		s.policyCache = map[string]domain.SwarmAccess{}
	}
	s.policyCache[peer] = a
	return a
}
func (s *Server) accessPrincipals(a *domain.ExecutionAccess) []string {
	if a == nil {
		return nil
	}
	result := []string{}
	for _, id := range append([]string{a.Origin}, a.Via...) {
		if id != "" && id != s.Swarm.Node().ID && !slices.Contains(result, id) {
			result = append(result, id)
		}
	}
	return result
}
func (s *Server) requireAccess(a *domain.ExecutionAccess, permission string) error {
	if a == nil {
		return nil
	}
	if slices.Contains(a.Denied, permission) {
		return denied(permission)
	}
	for _, peer := range s.accessPrincipals(a) {
		if !s.Swarm.Enabled() || !s.Swarm.Authorized(peer) || !s.effectiveAccess(peer).Permissions[permission] {
			return denied(permission)
		}
	}
	return nil
}
func (s *Server) executionFull(a *domain.ExecutionAccess) bool {
	if a == nil {
		return true
	}
	if a.FullDenied || len(a.Denied) != 0 {
		return false
	}
	for _, peer := range s.accessPrincipals(a) {
		if !s.Swarm.Authorized(peer) || !s.effectiveAccess(peer).Full {
			return false
		}
	}
	return true
}
func (s *Server) requireFull(a *domain.ExecutionAccess) error {
	if !s.executionFull(a) {
		return &accessError{"fullAccess", "This operation runs as the machine owner and requires Full access"}
	}
	return nil
}
func (s *Server) requireSharedBot(a *domain.ExecutionAccess, id string) error {
	if a == nil {
		return nil
	}
	b, err := s.Bot(id)
	if err != nil {
		return err
	}
	if !domain.Shared(b.SwarmAvailable) {
		return denied("bot:" + id)
	}
	for _, peer := range s.accessPrincipals(a) {
		if allowed, set := s.effectiveAccess(peer).Bots[id]; set && !allowed {
			return denied("bot:" + id)
		}
	}
	return nil
}
func (s *Server) requireSharedPlugin(a *domain.ExecutionAccess, id string) error {
	if a == nil {
		return nil
	}
	p, err := s.Plugins.Get(id)
	if err != nil {
		return err
	}
	if !domain.Shared(p.SwarmAvailable) {
		return denied("plugin:" + id)
	}
	for _, peer := range s.accessPrincipals(a) {
		if allowed, set := s.effectiveAccess(peer).Plugins[id]; set && !allowed {
			return denied("plugin:" + id)
		}
	}
	return nil
}
func (s *Server) requestAccess(r *http.Request) *domain.ExecutionAccess {
	if a := contextAccess(r.Context()); a != nil {
		return a
	}
	if p, ok := swarm.Caller(r.Context()); ok && !p.Pairing {
		return &domain.ExecutionAccess{Origin: p.NodeID}
	}
	return nil
}
func (s *Server) narrowAccess(a *domain.ExecutionAccess) *domain.ExecutionAccess {
	if a == nil {
		return nil
	}
	copy := *a
	copy.FullDenied = !s.executionFull(a)
	copy.Via = slices.Clone(a.Via)
	copy.Denied = slices.Clone(a.Denied)
	copy.Parents = slices.Clone(a.Parents)
	for _, permission := range domain.SwarmPermissions {
		if s.requireAccess(a, permission) != nil && !slices.Contains(copy.Denied, permission) {
			copy.Denied = append(copy.Denied, permission)
		}
	}
	return &copy
}

// Only a reduction cancels work. A closed connection is insufficient: accepted
// workers and scheduled/delegated jobs outlive the initiating HTTP request.
func (s *Server) revokeAccessWork(peer string) { s.stopRemoteAccess(peer, true) }

func (s *Server) stopRemoteAccess(peer string, force bool) {
	// Direct file and plugin grants do not govern a shared bot's normal tools.
	// A formerly full-trust task may already have an owner process running.
	stopBotWork := func(a *domain.ExecutionAccess, bots []string) bool {
		if force || !a.FullDenied && !s.executionFull(a) {
			return true
		}
		for _, key := range []string{domain.AccessBots, domain.AccessManageBots, domain.AccessManagePlugins, domain.AccessModels, domain.AccessSchedules, domain.AccessMachine, domain.AccessChats} {
			if !slices.Contains(a.Denied, key) && s.requireAccess(a, key) != nil {
				return true
			}
		}
		for _, key := range bots {
			node, id := swarm.Split(key)
			if (node == "" || node == s.Swarm.Node().ID) && s.requireSharedBot(a, id) != nil {
				return true
			}
		}
		return false
	}
	affected := func(a *domain.ExecutionAccess) bool {
		return a != nil && (peer == "" || slices.Contains(s.accessPrincipals(a), peer))
	}
	s.accessMu.Lock()
	for lease := range s.accessRequests {
		if affected(lease.access) {
			lease.cancel()
		}
	}
	var terminals []terminalAccess
	for node, sessions := range s.remoteTerminals {
		if peer == "" || peer == node {
			for session := range sessions {
				terminals = append(terminals, session)
			}
			delete(s.remoteTerminals, node)
		}
	}
	s.accessMu.Unlock()
	for _, session := range terminals {
		_ = s.Terminal.Stop(session.bot, session.session)
	}
	s.mu.Lock()
	var dispatches []*dispatch
	for _, d := range s.dispatches {
		if affected(d.access) && (d.waitingGroup || stopBotWork(d.access, slices.Collect(maps.Keys(d.wake)))) {
			dispatches = append(dispatches, d)
		}
	}
	s.mu.Unlock()
	for _, d := range dispatches {
		if d.federation != nil {
			_ = s.persistSwarmStop(d.federation)
		}
		d.cancel()
	}
	s.swarmMu.Lock()
	for _, w := range s.swarmWorkers {
		if affected(w.d.access) && stopBotWork(w.d.access, slices.Collect(maps.Keys(w.d.wake))) {
			w.d.cancel()
		}
	}
	s.swarmMu.Unlock()
	s.authMu.Lock()
	for key := range s.sessionSockets {
		if strings.HasPrefix(key, "swarm:") && (peer == "" || key == "swarm:"+peer) {
			s.closeSessionSocketsLocked(key)
		}
	}
	s.authMu.Unlock()
	// Invalidate durable parent grants, including completed parents whose child
	// schedules can run later. The child validates these over authenticated RPC.
	rows, _ := s.Store.List("swarm-access-grants")
	for _, raw := range rows {
		var grant accessGrant
		if json.Unmarshal(raw, &grant) == nil && affected(grant.Access) && stopBotWork(grant.Access, []string{grant.Bot}) {
			grant.Revoked = true
			_ = s.Store.Put("swarm-access-grants", grant.ID, grant)
		}
	}
	s.requestSwarmRefresh()
	s.emit("changed", "", "", "", nil)
}

func (s *Server) saveAccessPolicy(peer string, p domain.SwarmPolicy) error {
	if err := validateAccessPolicy(p); err != nil {
		return err
	}
	before := map[string]domain.SwarmAccess{}
	for _, remote := range s.Swarm.Directory() {
		if peer == "" || peer == remote.Node.ID {
			before[remote.Node.ID] = s.effectiveAccess(remote.Node.ID)
		}
	}
	id := peer
	if id == "" {
		id = "defaults"
	}
	if err := s.Store.Put("swarm-access", id, p); err != nil {
		return err
	}
	s.invalidatePolicies()
	for node, old := range before {
		current := s.effectiveAccess(node)
		reduced := old.Full && !current.Full
		for k, v := range old.Permissions {
			reduced = reduced || v && !current.Permissions[k]
		}
		for k := range current.Bots {
			_, existed := old.Bots[k]
			reduced = reduced || !existed
		}
		for k := range current.Plugins {
			_, existed := old.Plugins[k]
			reduced = reduced || !existed
		}
		if reduced {
			s.stopRemoteAccess(node, false)
		}
	}
	s.emit("changed", "", "", "", nil)
	return nil
}

func ownerAccessOnly(w http.ResponseWriter, r *http.Request) bool {
	if _, remote := swarm.Caller(r.Context()); remote {
		accessErrorResponse(w, denied("ownerSession"))
		return false
	}
	return true
}
func (s *Server) swarmAccessRoutes(mux *http.ServeMux) {
	for _, route := range []string{"GET /api/v1/swarm/access", "PUT /api/v1/swarm/access", "GET /api/v1/swarm/peers/{id}/access", "PUT /api/v1/swarm/peers/{id}/access"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			if !ownerAccessOnly(w, r) {
				return
			}
			peer := r.PathValue("id")
			if peer != "" && (!noteNodeID.MatchString(peer) || peer == s.Swarm.Node().ID) {
				respond(w, nil, errors.New("invalid peer ID"))
				return
			}
			if r.Method == "PUT" {
				var p domain.SwarmPolicy
				if err := decode(w, r, &p); err != nil {
					respond(w, nil, err)
					return
				}
				if err := s.saveAccessPolicy(peer, p); err != nil {
					respond(w, nil, err)
					return
				}
			}
			p, err := s.accessPolicy(peer)
			effectiveID := peer
			if effectiveID == "" {
				effectiveID = "machine-defaults"
			}
			respond(w, map[string]any{"policy": p, "effective": s.effectiveAccess(effectiveID), "permissions": domain.SwarmPermissions}, err)
		})
	}
	s.swarmInvitationRoutes(mux)
	mux.HandleFunc("PUT /api/v1/swarm/access/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !ownerAccessOnly(w, r) {
			return
		}
		var body struct {
			Shared *bool `json:"shared"`
		}
		if err := decode(w, r, &body); err != nil || body.Shared == nil {
			respond(w, nil, errors.New("shared must be true or false"))
			return
		}
		kind, id := r.PathValue("kind"), r.PathValue("id")
		if kind != "bots" && kind != "plugins" {
			respond(w, nil, errors.New("unknown sharing resource"))
			return
		}
		err := s.updateResourceSharing(kind, id, *body.Shared)
		if err == nil {
			err = s.setResourceSharing(kind, id, *body.Shared)
		}
		respond(w, map[string]bool{"shared": *body.Shared}, err)
	})
	s.accessGrantRoutes(mux)
}

func (s *Server) updateResourceSharing(kind, id string, shared bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "bots" && s.revoking[id] > 0 {
		return errors.New("bot settings are changing; retry after saving finishes")
	}
	err := s.Store.Update(kind, id, func(raw json.RawMessage) (any, error) {
		var record map[string]any
		if err := json.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		record["swarmAvailable"] = shared
		if kind == "bots" && !shared {
			delete(record, "sharedCliEndpointId")
		}
		return record, nil
	})
	if err == nil && kind == "bots" && !shared {
		s.sharedCLIGrantChangedLocked(id)
	}
	return err
}

func (s *Server) setResourceSharing(kind, id string, shared bool) error {
	var previous resourceGrant
	if err := s.Store.Get("swarm-resource-sharing", kind+":"+id, &previous); err == nil && previous.Shared == shared {
		return nil
	}
	if err := s.Store.Put("swarm-resource-sharing", kind+":"+id, resourceGrant{kind, id, shared}); err != nil {
		return err
	}
	s.invalidatePolicies()
	if !shared {
		s.stopRemoteAccess("", false)
	} else {
		s.emit("changed", "", "", "", nil)
	}
	return nil
}

// Refresh worker authorization while it runs, including durable delegation
// grants. Local owners and legacy unrestricted work keep their existing lifetime.
func (s *Server) watchExecutionAccess(ctx context.Context, cancel context.CancelFunc, a *domain.ExecutionAccess, bots []string) {
	if a == nil {
		return
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if s.requireAccess(a, domain.AccessBots) != nil {
			cancel()
			return
		}
		for _, id := range bots {
			if s.requireSharedBot(a, id) != nil {
				cancel()
				return
			}
		}
		if len(a.Parents) > 0 && s.validateAccessParents(ctx, a) != nil {
			cancel()
			return
		}
	}
}
