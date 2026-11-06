package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/externalagent"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/swarm"
)

// The home probes only routing metadata. A claim is checked again under the
// same lock and in the same reservation map as direct, inline and Swarm work.
type slotQuery struct {
	Access     *domain.ExecutionAccess `json:"access,omitempty"`
	Queued     bool                    `json:"queued,omitempty"`
	Turn       string                  `json:"turn"`
	Bot        string                  `json:"bot"`
	Origin     string                  `json:"origin"`
	Generation domain.Generation       `json:"generation"`
	Paths      []string                `json:"paths,omitempty"`
	Action     string                  `json:"action"`
	Revision   int64                   `json:"revision,omitempty"`
}
type slotInfo struct {
	Active   int      `json:"active"`
	Free     bool     `json:"free"`
	Revision int64    `json:"revision"`
	Handle   string   `json:"handle"`
	Paths    []string `json:"paths,omitempty"`
}

type groupLeaseKey struct{ bot, slot string }

func (s *Server) dropGroupSlotLocked(bot, key string) {
	delete(s.botReservations[bot], key)
	k := groupLeaseKey{bot, key}
	if timer := s.groupLeases[k]; timer != nil {
		timer.Stop()
		delete(s.groupLeases, k)
	}
}
func groupSlotKey(home, turn string) string { return "group:" + swarm.Key(home, turn) }
func (s *Server) hasGroupSlotLocked(bot, home, turn string) bool {
	return s.botReservations[bot][groupSlotKey(home, turn)]
}
func (s *Server) adoptGroupSlotLocked(bot, home, turn, dispatchID string) bool {
	if !s.hasGroupSlotLocked(bot, home, turn) {
		return false
	}
	s.dropGroupSlotLocked(bot, groupSlotKey(home, turn))
	s.botReservations[bot][dispatchID] = true
	return true
}
func (s *Server) signalGroupWakeLocked() {
	if s.groupWake != nil {
		close(s.groupWake)
		s.groupWake = nil
	}
}
func (s *Server) groupWakeChannel() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groupWake == nil {
		s.groupWake = make(chan struct{})
	}
	return s.groupWake
}

type groupModelConfig struct {
	url, credential, provider, model string
	cloud                            bool
}
type groupModelCheck struct {
	config groupModelConfig
	turn   string
	until  time.Time
	err    error
}

// Share short-lived metadata probes across groups waiting on the same
// endpoint. A claim checks again before any prompt can be delivered.
func (s *Server) checkGroupModel(ep domain.Endpoint, name string, q *slotQuery) error {
	config := groupModelConfig{url: ep.URL, credential: ep.Credential, provider: ep.Provider, model: name, cloud: ep.Cloud}
	s.mu.Lock()
	old := s.groupModels[ep.ID]
	s.mu.Unlock()
	if q.Action == "probe" && (q.Queued || old.turn == q.Turn) && old.config == config && time.Now().Before(old.until) {
		return old.err
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	key, err := s.key(ctx, ep)
	if err == nil {
		var catalog any
		catalog, err = s.Model.Models(ctx, ep, key)
		var status *model.ModelListError
		switch {
		case errors.As(err, &status) && (status.Status == http.StatusNotFound || status.Status == http.StatusMethodNotAllowed || status.Status == http.StatusNotImplemented):
			// Some compatible completion services take a configured model name
			// and do not implement the optional catalog endpoint.
			err = nil
		case err == nil:
			list, _ := catalog.(map[string]any)
			if models, ok := list["data"].([]any); ok && !slices.ContainsFunc(models, func(v any) bool {
				m, _ := v.(map[string]any)
				return m["id"] == name
			}) {
				err = fmt.Errorf("model %q is unavailable on %s", name, ep.Name)
			}
		}
	}
	check := groupModelCheck{config: config, turn: q.Turn, until: time.Now().Add(5 * time.Second), err: err}
	s.mu.Lock()
	if s.groupModels == nil {
		s.groupModels = map[string]groupModelCheck{}
	}
	s.groupModels[ep.ID] = check
	s.mu.Unlock()
	return err
}

// Captured run settings are validated without consulting current overrides.
func (s *Server) validateGeneration(bot domain.Bot, g domain.Generation, ep domain.Endpoint, query *slotQuery) error {
	if err := model.ValidateEndpoint(ep); err != nil {
		return err
	}
	if err := model.ValidateSettings(domain.Endpoint{Temperature: &g.Temperature, ReasoningEffort: g.Thinking}); err != nil {
		return err
	}
	if ep.External() {
		if err := externalagent.ValidateThinking(ep.Kind, g.Thinking); err != nil {
			return err
		}
		_, err := externalagent.Resolve(ep)
		return err
	}
	if g.Model == "" {
		return errors.New("select a model name")
	}
	if ep.Provider != "" {
		provider, err := s.Plugins.Provider(bot, ep.Provider)
		if err != nil {
			return err
		}
		if !provider.Builtin {
			// A custom provider owns model discovery and execution.
			if query != nil {
				_, err = s.key(s.ctx, ep)
			}
			return err
		}
	}
	if query == nil {
		return nil
	}
	return s.checkGroupModel(ep, g.Model, query)
}
func (s *Server) querySlot(ctx context.Context, node string, q slotQuery) (info slotInfo, err error) {
	if node == s.Swarm.Node().ID {
		return s.localSlot(node, q)
	}
	child, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = s.Swarm.JSON(child, node, "POST", "/api/v1/swarm/workers/group-slot", q, &info)
	return
}
func (s *Server) localSlot(home string, q slotQuery) (slotInfo, error) {
	var info slotInfo
	if q.Turn == "" || q.Bot == "" {
		return info, errors.New("invalid group slot request")
	}
	key := groupSlotKey(home, q.Turn)
	if q.Action == "release" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.botReservations[q.Bot][key] {
			s.dropGroupSlotLocked(q.Bot, key)
			s.admitLocked(q.Bot)
		}
		return info, nil
	}
	if q.Origin != s.Swarm.Node().ID && !s.Swarm.Authorized(q.Origin) {
		return info, errors.New("entry machine is not paired with this worker")
	}
	access := s.workerAccess(workerRequest{Home: home, Origin: q.Origin, Access: q.Access})
	if err := s.validateAccessParents(s.ctx, access); err != nil {
		return info, err
	}
	if err := s.requireAccess(access, domain.AccessBots); err != nil {
		return info, err
	}
	if err := s.requireSharedBot(access, q.Bot); err != nil {
		return info, err
	}
	bot, err := s.Bot(q.Bot)
	if err != nil {
		return info, errors.New("bot no longer exists")
	}
	g := q.Generation
	if g.EndpointID == "" {
		g = s.defaultGeneration()
	}
	if _, _, err := s.resolveGeneration(bot, bot.OverrideGeneration(g), access, &q); err != nil {
		return info, err
	}
	info.Handle, info.Revision = bot.Handle, bot.Revision
	for _, path := range q.Paths {
		if _, err := s.Files.ReferenceKind(bot, path); err == nil {
			info.Paths = append(info.Paths, path)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Bot(q.Bot)
	if err != nil || s.revoking[q.Bot] > 0 || current.Revision != bot.Revision {
		return info, errors.New("bot settings are changing")
	}
	if s.maintenance {
		return info, errMaintenance
	}
	info.Active = len(s.botReservations[q.Bot])
	info.Free = !s.promptSlotsFullLocked(current) && len(s.botQueues[q.Bot]) == 0
	switch q.Action {
	case "probe":
		return info, nil
	case "claim":
		if current.Revision != q.Revision {
			return info, errors.New("bot scope changed")
		}
		if s.botReservations[q.Bot][key] {
			info.Free = true
			return info, nil
		}
		if !info.Free {
			return info, nil
		}
	default:
		return info, errors.New("unknown slot operation")
	}
	if s.botReservations == nil {
		s.botReservations = map[string]map[string]bool{}
		s.botQueues = map[string][]*dispatch{}
	}
	if s.botReservations[q.Bot] == nil {
		s.botReservations[q.Bot] = map[string]bool{}
	}
	s.botReservations[q.Bot][key] = true
	// A disconnected home cannot strand capacity. Adoption removes this key.
	if s.groupLeases == nil {
		s.groupLeases = map[groupLeaseKey]*time.Timer{}
	}
	k := groupLeaseKey{q.Bot, key}
	var timer *time.Timer
	timer = time.AfterFunc(2*time.Minute, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.groupLeases[k] == timer {
			s.dropGroupSlotLocked(q.Bot, key)
			if s.ctx.Err() == nil {
				s.admitLocked(q.Bot)
			}
		}
	})
	s.groupLeases[k] = timer
	return info, nil
}

// Each owner serializes its group's tickets across every chat home. Only the
// head may assign, and only a committed assignment moves the durable cursor.
type groupTicket struct {
	key  string
	seen time.Time
}
type groupCommit struct {
	Owner string        `json:"owner"`
	Query rotationQuery `json:"query"`
}
type rotationCursor struct {
	Last    swarm.Ref `json:"last"`
	Holder  string    `json:"holder,omitempty"`
	Expires time.Time `json:"expires,omitempty"`
}
type rotationQuery struct {
	Group  string    `json:"group"`
	Turn   string    `json:"turn"`
	Action string    `json:"action"`
	Winner swarm.Ref `json:"winner,omitempty"`
}
type rotationState struct {
	Head bool      `json:"head"`
	Last swarm.Ref `json:"last"`
}

func (s *Server) rotation(ctx context.Context, node string, q rotationQuery) (state rotationState, err error) {
	if node == s.Swarm.Node().ID {
		return s.localRotation(node, q)
	}
	child, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err = s.Swarm.JSON(child, node, "POST", "/api/v1/swarm/workers/group-rotation", q, &state)
	return
}
func (s *Server) localRotation(home string, q rotationQuery) (state rotationState, err error) {
	if q.Turn == "" || q.Group == "" {
		return state, errors.New("invalid group rotation ticket")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groupTickets == nil {
		s.groupTickets = map[string][]*groupTicket{}
	}
	key := swarm.Key(home, q.Turn)
	now := time.Now()
	queue := slices.DeleteFunc(s.groupTickets[q.Group], func(t *groupTicket) bool { return now.Sub(t.seen) > 2*time.Minute })
	idx := slices.IndexFunc(queue, func(t *groupTicket) bool { return t.key == key })
	var cursor rotationCursor
	if err = s.Store.Get("group-cursors", q.Group, &cursor); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	err = nil
	switch q.Action {
	case "wait":
		if idx < 0 {
			idx = len(queue)
			queue = append(queue, &groupTicket{key: key})
		}
		queue[idx].seen = now
		state.Head = cursor.Holder == key || idx == 0 && (cursor.Holder == "" || !cursor.Expires.After(now))
		if state.Head {
			state.Last = cursor.Last
			if cursor.Holder != key || cursor.Expires.Sub(now) < time.Minute {
				cursor.Holder = key
				cursor.Expires = now.Add(2 * time.Minute)
				if err = s.Store.Put("group-cursors", q.Group, cursor); err != nil {
					return state, err
				}
			}
		}
	case "commit", "leave":
		if cursor.Holder == key {
			if q.Action == "commit" {
				cursor.Last = q.Winner
			}
			cursor.Holder = ""
			cursor.Expires = time.Time{}
			if err = s.Store.Put("group-cursors", q.Group, cursor); err != nil {
				return state, err
			}
		}
		if idx >= 0 {
			queue = slices.Delete(queue, idx, idx+1)
			s.signalGroupWakeLocked()
		}
	default:
		return state, errors.New("unknown rotation operation")
	}
	if len(queue) == 0 {
		delete(s.groupTickets, q.Group)
	} else {
		s.groupTickets[q.Group] = queue
	}
	return
}

// Validate the final grant and scope without forwarding the input, uploads,
// or chat title. The worker verifies the actual payload again during prepare.
type groupWorkerCheck struct {
	Worker      workerRequest `json:"worker"`
	Fingerprint string        `json:"fingerprint"`
}

func (s *Server) validateGroupWorkerRemote(ctx context.Context, node string, host workerRequest) error {
	check := groupWorkerCheck{Fingerprint: requestFingerprint(host)}
	check.Worker = workerRequest{ID: host.ID, Token: host.Token, Home: host.Home, Origin: host.Origin, Permit: host.Permit, Access: host.Access, AccessPermit: host.AccessPermit, Participants: host.Participants, Runs: host.Runs}
	for _, seg := range host.Message.Segments {
		if seg.Type == "file" && seg.NodeID == node {
			check.Worker.Message.Segments = append(check.Worker.Message.Segments, domain.Segment{Type: "file", NodeID: node, Path: seg.Path})
		}
	}
	if node == s.Swarm.Node().ID {
		return s.checkWorker(check.Worker, check.Fingerprint)
	}
	return s.Swarm.JSON(ctx, node, "POST", "/api/v1/swarm/workers/group-validate", check, nil)
}

// All routing RPCs inherit the authenticated home from their peer tunnel.
func groupRPC[Q, R any](run func(string, Q) (R, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := swarm.Caller(r.Context())
		if !ok || caller.Pairing {
			http.Error(w, "Peer-only worker endpoint", http.StatusForbidden)
			return
		}
		var q Q
		if err := decode(w, r, &q); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := run(caller.NodeID, q)
		respond(w, value, err)
	}
}
func (s *Server) groupRoutingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/swarm/workers/group-slot", groupRPC(s.localSlot))
	mux.HandleFunc("POST /api/v1/swarm/workers/group-rotation", groupRPC(s.localRotation))
	mux.HandleFunc("POST /api/v1/swarm/workers/group-permits", groupRPC(s.issueGroupPermits))
	mux.HandleFunc("POST /api/v1/swarm/workers/group-receipt", groupRPC(func(_ string, q groupReceipt) (string, error) {
		return s.Store.Accepted(q.Chat, q.Request), nil
	}))
	mux.HandleFunc("POST /api/v1/swarm/workers/group-validate", groupRPC(func(home string, check groupWorkerCheck) (bool, error) {
		if check.Worker.Home != home {
			return false, errors.New("chat home mismatch")
		}
		return true, s.checkWorker(check.Worker, check.Fingerprint)
	}))
}
