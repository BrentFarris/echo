package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

func (s *Server) botGroups() ([]domain.BotGroup, error) {
	rows, err := s.Store.List("bot-groups")
	if err != nil {
		return nil, err
	}
	groups, err := store.Decode[domain.BotGroup](rows)
	for i := range groups {
		if groups[i].DeliveryMode == "" {
			groups[i].DeliveryMode = "all"
		}
	}
	return groups, err
}

func (s *Server) visibleBotGroups(ctx context.Context) ([]domain.BotGroup, error) {
	groups, err := s.botGroups()
	if caller, ok := swarm.Caller(ctx); ok && !s.oneGroupCapable(caller.NodeID) {
		groups = slices.DeleteFunc(groups, func(g domain.BotGroup) bool { return g.DeliveryMode == "one" })
	}
	return groups, err
}

func (s *Server) checkGroupHandle(handle, except string) error {
	groups, err := s.botGroups()
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group.Handle == handle && group.ID != except {
			return fmt.Errorf("group handle @%s is already in use", handle)
		}
	}
	return nil
}

func (s *Server) saveBotGroup(ctx context.Context, id string, group domain.BotGroup) (domain.BotGroup, error) {
	group.ID, group.Handle = id, strings.ToLower(strings.TrimSpace(group.Handle))
	if !handlePattern.MatchString(group.Handle) {
		return group, errors.New("use a handle of up to 48 letters, digits, hyphens or underscores, starting with a letter")
	}
	if len(group.Members) == 0 {
		return group, errors.New("choose at least one bot for the group")
	}
	var original domain.BotGroup
	if id != "" {
		if err := s.Store.Get("bot-groups", id, &original); err != nil {
			return group, err
		}
	}
	if group.DeliveryMode == "" {
		group.DeliveryMode = original.DeliveryMode
	}
	if group.DeliveryMode == "" {
		group.DeliveryMode = "all"
	}
	if group.DeliveryMode != "all" && group.DeliveryMode != "one" {
		return group, errors.New("choose All bots or One available bot for delivery")
	}
	// Existing bindings may be retained while a member is offline or deleted.
	// New members must come from their owner's current bot roster.
	existing := map[swarm.Ref]domain.BotGroupMember{}
	for _, member := range original.Members {
		existing[swarm.Ref{NodeID: member.NodeID, ID: member.BotID}] = member
	}
	rosters := map[string][]domain.Bot{}
	seen := map[swarm.Ref]bool{}
	members := make([]domain.BotGroupMember, 0, len(group.Members))
	self := s.Swarm.Node().ID
	for _, member := range group.Members {
		if member.NodeID == "" {
			member.NodeID = self
		}
		key := swarm.Ref{NodeID: member.NodeID, ID: member.BotID}
		if seen[key] {
			continue
		}
		seen[key] = true
		if old, ok := existing[key]; ok {
			members = append(members, old)
			continue
		}
		bots, ok := rosters[key.NodeID]
		if !ok {
			var err error
			if key.NodeID == self {
				bots, err = s.Bots()
			} else {
				if !s.swarmAvailable(key.NodeID) {
					return group, s.offlineMachine(key.NodeID, member.Handle)
				}
				var state struct {
					Bots []domain.Bot `json:"bots"`
				}
				child, cancel := context.WithTimeout(ctx, 3*time.Second)
				err = s.Swarm.JSON(child, key.NodeID, "GET", "/api/v1/state", nil, &state)
				cancel()
				bots = state.Bots
			}
			if err != nil {
				return group, err
			}
			rosters[key.NodeID] = bots
		}
		at := slices.IndexFunc(bots, func(bot domain.Bot) bool { return bot.ID == key.ID })
		if at < 0 {
			return group, fmt.Errorf("bot %s is unavailable on %s", key.ID, s.Swarm.Namespace(key.NodeID))
		}
		member.Handle = bots[at].Handle
		members = append(members, member)
	}
	group.Members = members
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		group.ID = domain.ID()
	} else {
		var latest domain.BotGroup
		if err := s.Store.Get("bot-groups", id, &latest); err != nil {
			return group, err
		}
		if original.DeliveryMode != latest.DeliveryMode || original.Handle != latest.Handle || !slices.Equal(original.Members, latest.Members) {
			return group, errors.New("group changed while saving; reopen it and try again")
		}
	}
	if err := s.checkGroupHandle(group.Handle, group.ID); err != nil {
		return group, err
	}
	bots, err := s.Bots()
	if err != nil {
		return group, err
	}
	if slices.ContainsFunc(bots, func(b domain.Bot) bool { return b.Handle == group.Handle }) {
		return group, fmt.Errorf("bot handle @%s is already in use", group.Handle)
	}
	err = s.Store.Put("bot-groups", group.ID, group)
	if err == nil {
		s.emitLocked("changed", "", "", "", nil)
	}
	return group, err
}

func (s *Server) botGroupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/bot-groups", func(w http.ResponseWriter, r *http.Request) {
		groups, err := s.visibleBotGroups(r.Context())
		respond(w, groups, err)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var group domain.BotGroup
		if err := decode(w, r, &group); err != nil {
			respond(w, nil, err)
			return
		}
		group, err := s.saveBotGroup(r.Context(), r.PathValue("id"), group)
		respond(w, group, err)
	}
	mux.HandleFunc("POST /api/v1/bot-groups", save)
	mux.HandleFunc("PUT /api/v1/bot-groups/{id}", save)
	mux.HandleFunc("DELETE /api/v1/bot-groups/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		err := s.Store.Delete("bot-groups", r.PathValue("id"))
		if err == nil {
			s.emitLocked("changed", "", "", "", nil)
		}
		s.mu.Unlock()
		respond(w, map[string]bool{"deleted": err == nil}, err)
	})
}
