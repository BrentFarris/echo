package server

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

type groupReceipt struct {
	Chat    string `json:"chatId"`
	Request string `json:"requestId"`
}

// The entry retains a digest of the draft and its fixed recipients. The home
// can exchange it for proofs for just those bots and one candidate per group.
type groupIntent struct {
	Fingerprint string             `json:"fingerprint"`
	Fixed       []swarmParticipant `json:"fixed"`
	Assigned    string             `json:"assigned,omitempty"`
}

func intentID(home, chat, request string) string { return swarm.Key(home, swarm.Key(chat, request)) }
func (s *Server) rememberGroupIntent(req workerRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := intentID(req.Home, req.Chat.ID, req.RequestID)
	scope := groupIntent{Fingerprint: requestFingerprint(req), Fixed: req.Participants}
	var old groupIntent
	err := s.Store.Get("group-intents", id, &old)
	if err == nil {
		if old.Fingerprint != scope.Fingerprint {
			return errors.New("request id already refers to another group message")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.Store.Put("group-intents", id, scope)
}
func (s *Server) issueGroupPermits(home string, req workerRequest) (map[string]string, error) {
	if home != req.Home {
		return nil, errors.New("chat home mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := intentID(req.Home, req.Chat.ID, req.RequestID)
	var scope groupIntent
	if err := s.Store.Get("group-intents", id, &scope); err != nil {
		return nil, errors.New("group send has no authorization from its entry machine")
	}
	final := requestFingerprint(req)
	if scope.Assigned != "" && scope.Assigned != final {
		return nil, errors.New("entry already authorized a different assignment")
	}
	concrete := req.Participants
	req.Participants = scope.Fixed
	req.Message.Segments = slices.Clone(req.Message.Segments)
	expected := map[swarm.Ref]bool{}
	for _, p := range scope.Fixed {
		expected[p.Ref] = true
	}
	chosen := map[string]swarm.Ref{}
	for i, g := range req.Message.Segments {
		if g.Type != "group" || g.DeliveryMode != "one" {
			continue
		}
		if len(g.Members) != 1 {
			return nil, errors.New("group assignment requires one recipient")
		}
		ref := refOf(g.Members[0])
		if !slices.ContainsFunc(g.Candidates, func(m domain.BotGroupMember) bool { return refOf(m) == ref }) {
			return nil, errors.New("assigned bot is outside the group")
		}
		key := groupKey(g)
		if previous, ok := chosen[key]; ok && previous != ref {
			return nil, errors.New("conflicting group assignments")
		}
		chosen[key] = ref
		expected[ref] = true
		req.Message.Segments[i].Members = nil
	}
	actual := map[swarm.Ref]bool{}
	for _, p := range concrete {
		actual[p.Ref] = true
	}
	if !maps.Equal(actual, expected) || requestFingerprint(req) != scope.Fingerprint {
		return nil, errors.New("group assignment does not match the authorized message")
	}
	proofs := map[string]string{}
	for ref := range actual {
		if ref.NodeID == s.Swarm.Node().ID || proofs[ref.NodeID] != "" {
			continue
		}
		proof, err := s.Swarm.Proof(ref.NodeID, final)
		if err != nil {
			return nil, err
		}
		proofs[ref.NodeID] = proof
	}
	scope.Assigned = final
	return proofs, s.Store.Put("group-intents", id, scope)
}
func (s *Server) finalGroupPermits(ctx context.Context, req SendRequest, chat string, participants []swarmParticipant, segments []domain.Segment) (map[string]string, error) {
	var permits map[string]string
	input := workerRequest{Home: s.Swarm.Node().ID, Chat: domain.Chat{ID: chat}, RequestID: req.RequestID, Participants: participants, Message: domain.Message{Content: req.Content, Segments: segments}}
	err := s.Swarm.JSON(ctx, req.OriginNodeID, "POST", "/api/v1/swarm/workers/group-permits", input, &permits)
	return permits, err
}
