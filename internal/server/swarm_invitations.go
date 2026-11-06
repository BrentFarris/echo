package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

type swarmInvitation struct {
	Epoch   string             `json:"epoch,omitempty"`
	ID      string             `json:"id"`
	Hash    string             `json:"hash,omitempty"`
	Expires time.Time          `json:"expiresAt"`
	Policy  domain.SwarmPolicy `json:"policy"`
	Node    string             `json:"redeemedBy,omitempty"`
	Pair    string             `json:"pair,omitempty"`
	Revoked bool               `json:"revoked,omitempty"`
}

func invitationHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Server) acceptSwarmInvitation(ctx context.Context, request swarm.PairRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.invitationMu.Lock()
	defer s.invitationMu.Unlock()
	if len(request.Invitation) != 64 {
		return errors.New("invalid or expired swarm invitation")
	}
	hash := invitationHash(request.Invitation)
	var invite swarmInvitation
	if s.Store.Get("swarm-invitations", hash, &invite) != nil || invite.Revoked || !time.Now().Before(invite.Expires) {
		return errors.New("invalid or expired swarm invitation")
	}
	var epoch string
	for _, p := range s.Swarm.Directory() {
		if p.Node.ID == request.Node.ID {
			epoch = p.Revocation
			break
		}
	}
	if invite.Node != "" {
		if invite.Node == request.Node.ID && invite.Pair == request.ID && invite.Epoch == epoch {
			for _, p := range s.Swarm.Directory() {
				if p.Node.ID == invite.Node && (p.Blocked || p.Disabled) {
					return errors.New("pairing has been revoked; create a new invitation")
				}
			}
			return nil
		}
		return errors.New("swarm invitation has already been used")
	}
	// Commit the restriction before the transport publishes any peer grant.
	invite.Node, invite.Pair, invite.Epoch = request.Node.ID, request.ID, epoch
	policy, err := s.pairPolicy(invite.Node, invite.Policy)
	if err != nil {
		return err
	}
	if err := s.Store.PutRecords(map[string]map[string]any{"swarm-invitations": {hash: invite}, "swarm-access": {invite.Node: policy}}); err != nil {
		return err
	}
	s.invalidatePolicies()
	if s.Swarm.Authorized(invite.Node) {
		s.revokeAccessWork(invite.Node)
	}
	return nil
}

// Pairing never erases a prior exclusion or an explicit denial. Widening
// an existing grant is a separate owner action in the peer's access editor.
func (s *Server) pairPolicy(peer string, offered domain.SwarmPolicy) (domain.SwarmPolicy, error) {
	old, err := s.accessPolicy(peer)
	if err != nil {
		return offered, err
	}
	merge := func(previous, next map[string]bool) map[string]bool {
		if next == nil {
			next = map[string]bool{}
		}
		for key, value := range previous {
			if _, exists := next[key]; !exists || !value {
				next[key] = value
			}
		}
		return next
	}
	offered.Permissions = merge(old.Permissions, offered.Permissions)
	offered.Bots = merge(old.Bots, offered.Bots)
	offered.Plugins = merge(old.Plugins, offered.Plugins)
	return offered, nil
}

func (s *Server) swarmInvitationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/swarm/invitations", func(w http.ResponseWriter, r *http.Request) {
		if !ownerAccessOnly(w, r) {
			return
		}
		var body struct {
			Policy domain.SwarmPolicy `json:"policy"`
		}
		if err := decode(w, r, &body); err != nil {
			respond(w, nil, err)
			return
		}
		if err := validateAccessPolicy(body.Policy); err != nil {
			respond(w, nil, err)
			return
		}
		token := domain.ID() + domain.ID()
		i := swarmInvitation{ID: domain.ID(), Hash: invitationHash(token), Expires: time.Now().UTC().Add(15 * time.Minute), Policy: body.Policy}
		err := s.Store.Put("swarm-invitations", i.Hash, i)
		i.Hash = ""
		respond(w, map[string]any{"invitation": i, "token": token + "." + hex.EncodeToString(s.Swarm.Node().PublicKey)}, err)
	})
	mux.HandleFunc("GET /api/v1/swarm/invitations", func(w http.ResponseWriter, r *http.Request) {
		if !ownerAccessOnly(w, r) {
			return
		}
		rows, err := s.Store.List("swarm-invitations")
		out := []swarmInvitation{}
		for _, raw := range rows {
			var i swarmInvitation
			if json.Unmarshal(raw, &i) == nil {
				i.Hash, i.Pair = "", ""
				out = append(out, i)
			}
		}
		respond(w, out, err)
	})
	mux.HandleFunc("DELETE /api/v1/swarm/invitations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !ownerAccessOnly(w, r) {
			return
		}
		s.invitationMu.Lock()
		defer s.invitationMu.Unlock()
		rows, err := s.Store.List("swarm-invitations")
		if err == nil {
			err = errors.New("invitation not found")
			for _, raw := range rows {
				var i swarmInvitation
				if json.Unmarshal(raw, &i) == nil && i.ID == r.PathValue("id") {
					i.Revoked = true
					err = s.Store.Put("swarm-invitations", i.Hash, i)
					break
				}
			}
		}
		respond(w, map[string]bool{"revoked": err == nil}, err)
	})
	mux.HandleFunc("POST /api/v1/swarm/invitations/redeem", func(w http.ResponseWriter, r *http.Request) {
		if !ownerAccessOnly(w, r) {
			return
		}
		var body struct {
			URL    string             `json:"url"`
			NodeID string             `json:"nodeId"`
			Token  string             `json:"token"`
			Policy domain.SwarmPolicy `json:"policy"`
		}
		if err := decode(w, r, &body); err != nil {
			respond(w, nil, err)
			return
		}
		if err := validateAccessPolicy(body.Policy); err != nil {
			respond(w, nil, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		node, err := s.Swarm.ConnectInvitation(ctx, body.URL, body.NodeID, body.Token, func(node swarm.Node) error {
			policy, err := s.pairPolicy(node.ID, body.Policy)
			if err != nil {
				return err
			}
			return s.saveAccessPolicy(node.ID, policy)
		})
		respond(w, node, err)
	})
}
