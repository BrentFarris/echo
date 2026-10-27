package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/brent/echo/internal/development"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

var errMaintenance = errors.New("Echo is updating; retry after maintenance")

type rolloutMember struct {
	NodeID       string                  `json:"nodeId"`
	Name         string                  `json:"name"`
	Depth        int                     `json:"depth"`
	OperationID  string                  `json:"operationId"`
	Status       string                  `json:"status"`
	Error        string                  `json:"error,omitempty"`
	Check        development.UpdateCheck `json:"check"`
	Update       *development.Update     `json:"update,omitempty"`
	LostSince    time.Time               `json:"lostSince,omitempty"`
	RestartSince time.Time               `json:"restartSince,omitempty"`
}
type rollout struct {
	Version         int             `json:"version"`
	ID              string          `json:"id"`
	SourceNodeID    string          `json:"sourceNodeId"`
	Scope           string          `json:"scope"`
	Target          string          `json:"target"`
	State           string          `json:"state"`
	CreatedAt       time.Time       `json:"createdAt"`
	CancelRequested bool            `json:"cancelRequested"`
	Members         []rolloutMember `json:"members"`
}
type rolloutManager struct {
	s             *Server
	mu            sync.Mutex
	wg            sync.WaitGroup
	jobs          map[string]*rollout
	requestUpdate func(context.Context, rolloutMember, string, string) (development.Update, error)
}

func (s *Server) initRollouts() error {
	if err := s.Development.ConfigureUpdates(s.Store, func(value bool) { s.mu.Lock(); s.maintenance = value; s.mu.Unlock() }, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.dispatches) == 0 && len(s.botReservations) == 0
	}); err != nil {
		return err
	}
	m := &rolloutManager{s: s, jobs: map[string]*rollout{}}
	m.requestUpdate = m.participant
	s.rollouts = m
	rows, err := s.Store.List("development-rollouts")
	if err != nil {
		return err
	}
	for _, data := range rows {
		var r rollout
		if err = json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.Version != 1 {
			return errors.New("unsupported rollout journal version")
		}
		m.jobs[r.ID] = &r
	}
	for _, r := range m.jobs {
		if r.State == "running" {
			m.start(r)
		}
	}
	return nil
}

func (m *rolloutManager) save(r *rollout) error {
	return m.s.Store.Put("development-rollouts", r.ID, r)
}
func (m *rolloutManager) change(r *rollout, f func()) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
	return m.save(r)
}
func (m *rolloutManager) snapshot(r *rollout) rollout {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, _ := json.Marshal(r)
	var copy rollout
	_ = json.Unmarshal(data, &copy)
	return copy
}
func (m *rolloutManager) start(r *rollout) { m.wg.Add(1); go func() { defer m.wg.Done(); m.run(r) }() }

func (s *Server) developmentUpdateRoutes(mux *http.ServeMux) {
	// Orchestration is entry-local: an authenticated peer may invoke only leaf
	// operations, never use this machine's grants to fan out to other machines.
	entryOnly := func(w http.ResponseWriter, r *http.Request) bool {
		if _, ok := swarm.Caller(r.Context()); ok {
			http.Error(w, "Start the rollout on the browser entry instance", 403)
			return false
		}
		return true
	}
	owner := func(r *http.Request) string {
		if p, ok := swarm.Caller(r.Context()); ok {
			return p.NodeID
		}
		return s.Swarm.Node().ID
	}
	mux.HandleFunc("POST /api/v1/development/update-target", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		target, err := s.Development.UpdateTarget(ctx)
		respond(w, map[string]string{"target": target}, err)
	})
	mux.HandleFunc("POST /api/v1/development/updates/check", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Target string `json:"target"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		respond(w, s.Development.CheckUpdate(ctx, input.Target), nil)
	})
	mux.HandleFunc("POST /api/v1/development/updates/{id}/prepare", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Target string `json:"target"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := s.Development.PrepareUpdate(owner(r), r.PathValue("id"), input.Target)
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/v1/development/updates/{id}", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.Development.UpdateStatus(owner(r), r.PathValue("id"))
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/development/updates/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.Development.ControlUpdate(owner(r), r.PathValue("id"), r.PathValue("action"))
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/development/rollouts/check", func(w http.ResponseWriter, r *http.Request) {
		if !entryOnly(w, r) {
			return
		}
		var input struct {
			SourceNodeID string `json:"sourceNodeId"`
			Scope        string `json:"scope"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := s.rollouts.check(r.Context(), input.SourceNodeID, input.Scope)
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/v1/development/rollouts", func(w http.ResponseWriter, r *http.Request) {
		if !entryOnly(w, r) {
			return
		}
		m := s.rollouts
		m.mu.Lock()
		defer m.mu.Unlock()
		values := make([]*rollout, 0, len(m.jobs))
		for _, v := range m.jobs {
			values = append(values, v)
		}
		sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
		respond(w, values, nil)
	})
	mux.HandleFunc("GET /api/v1/development/rollouts/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !entryOnly(w, r) {
			return
		}
		m := s.rollouts
		m.mu.Lock()
		defer m.mu.Unlock()
		value := m.jobs[r.PathValue("id")]
		if value == nil {
			http.NotFound(w, r)
			return
		}
		respond(w, value, nil)
	})
	mux.HandleFunc("POST /api/v1/development/rollouts", func(w http.ResponseWriter, r *http.Request) {
		if !entryOnly(w, r) {
			return
		}
		var input struct {
			CheckID string `json:"checkId"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		value, err := s.rollouts.control(input.CheckID, "start")
		respond(w, value, err)
	})
	mux.HandleFunc("POST /api/v1/development/rollouts/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !entryOnly(w, r) {
			return
		}
		value, err := s.rollouts.control(r.PathValue("id"), r.PathValue("action"))
		respond(w, value, err)
	})
}

func (m *rolloutManager) peerError(node string) error {
	if node == m.s.Swarm.Node().ID {
		return nil
	}
	if !m.s.Swarm.Enabled() || !m.s.Swarm.Authorized(node) {
		return swarm.ErrUnauthorized
	}
	for _, p := range m.s.Swarm.Directory() {
		if p.Node.ID == node {
			if p.Disabled || p.Blocked {
				return errors.New("connection is disabled or revoked")
			}
			if !p.Connected {
				return swarm.ErrUnavailable
			}
			if !slices.Contains(p.Node.Capabilities, development.UpdateCapability) {
				return errors.New("requires a one-time update using this machine's existing local updater")
			}
			return nil
		}
	}
	return swarm.ErrUnavailable
}

func (m *rolloutManager) call(ctx context.Context, node, method, path string, input, output any) error {
	if err := m.peerError(node); err != nil {
		return err
	}
	return m.s.Swarm.JSON(ctx, node, method, path, input, output)
}

func (m *rolloutManager) check(ctx context.Context, source, scope string) (*rollout, error) {
	if source == "" {
		source = m.s.Swarm.Node().ID
	}
	if scope != "swarm" && scope != "machine" {
		return nil, errors.New("scope must be swarm or machine")
	}
	if err := m.peerError(source); err != nil {
		return nil, err
	}
	var result struct {
		Target string `json:"target"`
	}
	var err error
	if source == m.s.Swarm.Node().ID {
		result.Target, err = m.s.Development.UpdateTarget(ctx)
	} else {
		err = m.call(ctx, source, "POST", "/api/v1/development/update-target", nil, &result)
	}
	if err != nil {
		return nil, err
	}
	r := &rollout{Version: 1, ID: domain.ID(), SourceNodeID: source, Scope: scope, Target: result.Target, State: "checked", CreatedAt: time.Now().UTC()}
	add := func(id, name string) {
		r.Members = append(r.Members, rolloutMember{NodeID: id, Name: name, Depth: m.s.Swarm.RouteDepth(id), OperationID: domain.ID(), Status: "queued"})
	}
	if scope == "swarm" || source == m.s.Swarm.Node().ID {
		add(m.s.Swarm.Node().ID, m.s.Swarm.Namespace(m.s.Swarm.Node().ID))
	}
	for _, p := range m.s.Swarm.Directory() {
		if (scope == "swarm" || p.Node.ID == source) && p.Authorized && !p.Blocked && !p.Disabled {
			add(p.Node.ID, m.s.Swarm.Namespace(p.Node.ID))
		}
	}
	if len(r.Members) == 0 {
		return nil, errors.New("selected machine is unavailable")
	}
	// Bound network work, not the number of machines in the swarm.
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i := range r.Members {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				r.Members[i].Status = "skipped"
				r.Members[i].Error = ctx.Err().Error()
				return
			}
			defer func() { <-slots }()
			p := &r.Members[i]
			child, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if e := m.peerError(p.NodeID); e != nil {
				p.Error = e.Error()
			} else if p.NodeID == m.s.Swarm.Node().ID {
				p.Check = m.s.Development.CheckUpdate(child, r.Target)
			} else if e = m.call(child, p.NodeID, "POST", "/api/v1/development/updates/check", map[string]string{"target": r.Target}, &p.Check); e != nil {
				p.Error = e.Error()
			}
			if p.Error == "" {
				p.Error = p.Check.Error
			}
			if p.Error != "" {
				p.Status = "skipped"
			} else if p.Check.Current {
				p.Status = "succeeded"
			}
		}(i)
	}
	wg.Wait()
	sort.Slice(r.Members, func(i, j int) bool {
		a, b := r.Members[i], r.Members[j]
		if a.NodeID == m.s.Swarm.Node().ID {
			return false
		}
		if b.NodeID == m.s.Swarm.Node().ID {
			return true
		}
		if a.Depth != b.Depth {
			return a.Depth > b.Depth
		}
		return a.NodeID < b.NodeID
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.save(r); err != nil {
		return nil, err
	}
	m.jobs[r.ID] = r
	return r, nil
}

func (m *rolloutManager) control(id, action string) (rollout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.jobs[id]
	if r == nil {
		return rollout{}, errors.New("rollout not found")
	}
	start := false
	switch action {
	case "start":
		if r.State == "checked" {
			r.State = "running"
			start = true
		}
	case "retry":
		if r.State == "running" {
			return rollout{}, development.ErrBusy
		}
		r.CancelRequested = false
		for i := range r.Members {
			p := &r.Members[i]
			if p.Status != "succeeded" {
				p.Status, p.Error, p.OperationID = "queued", "", domain.ID()
				p.Update = nil
				p.LostSince = time.Time{}
				p.RestartSince = time.Time{}
			}
		}
		r.State = "running"
		start = true
	case "cancel":
		if r.State == "checked" {
			r.State = "cancelled"
		}
		r.CancelRequested = true
	default:
		return rollout{}, errors.New("unknown rollout action")
	}
	if err := m.save(r); err != nil {
		return rollout{}, err
	}
	if start {
		m.start(r)
	}
	data, _ := json.Marshal(r)
	var copy rollout
	_ = json.Unmarshal(data, &copy)
	return copy, nil
}

func (m *rolloutManager) participant(ctx context.Context, p rolloutMember, target, action string) (development.Update, error) {
	if err := m.peerError(p.NodeID); err != nil {
		return development.Update{}, err
	}
	if p.NodeID == m.s.Swarm.Node().ID {
		switch action {
		case "prepare":
			return m.s.Development.PrepareUpdate(p.NodeID, p.OperationID, target)
		case "status":
			return m.s.Development.UpdateStatus(p.NodeID, p.OperationID)
		default:
			return m.s.Development.ControlUpdate(p.NodeID, p.OperationID, action)
		}
	}
	var u development.Update
	method, path := "POST", "/api/v1/development/updates/"+p.OperationID+"/"+action
	if action == "status" {
		method, path = "GET", "/api/v1/development/updates/"+p.OperationID
	}
	err := m.call(ctx, p.NodeID, method, path, map[string]string{"target": target}, &u)
	return u, err
}

func (m *rolloutManager) run(r *rollout) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if m.s.ctx.Err() != nil {
			return
		}
		snap := m.snapshot(r)
		// Renew independently so an offline peer cannot starve another peer's lease.
		var pollers sync.WaitGroup
		for i, p := range snap.Members {
			if development.UpdateTerminal(p.Status) {
				continue
			}
			pollers.Add(1)
			go func(i int, p rolloutMember) {
				defer pollers.Done()
				action := "status"
				if snap.CancelRequested {
					action = "cancel"
				} else if p.Status == "queued" {
					action = "prepare"
				} else if p.Status != "restarting" {
					action = "renew"
				}
				ctx, cancel := context.WithTimeout(m.s.ctx, 8*time.Second)
				u, err := m.requestUpdate(ctx, p, snap.Target, action)
				cancel()
				if err = m.change(r, func() {
					member := &r.Members[i]
					if err != nil {
						if member.LostSince.IsZero() {
							member.LostSince = time.Now()
						}
						member.Error = err.Error()
						if action == "cancel" && p.Status == "queued" {
							member.Status = "cancelled"
						} else if time.Since(member.LostSince) > 2*time.Minute {
							member.Status = "failed"
						}
					} else {
						member.LostSince = time.Time{}
						member.Update = &u
						member.Status = u.Status
						member.Error = u.Error
						if u.Status == "restarting" {
							if member.RestartSince.IsZero() {
								member.RestartSince = time.Now()
							}
							if time.Since(member.RestartSince) > 2*time.Minute {
								member.Status, member.Error = "failed", "Replacement did not report the expected revision within two minutes; inspect the launcher log"
							}
						}
					}
				}); err != nil {
					return
				}
			}(i, p)
		}
		pollers.Wait()
		snap = m.snapshot(r)
		preparing, active, allDone := false, false, true
		for _, p := range snap.Members {
			preparing = preparing || p.Status == "queued" || p.Status == "preparing"
			active = active || p.Status == "draining" || p.Status == "restarting"
			allDone = allDone && development.UpdateTerminal(p.Status)
		}
		if allDone {
			_ = m.change(r, func() {
				r.State = "succeeded"
				for _, p := range r.Members {
					if p.Status != "succeeded" {
						r.State = "partial"
					}
				}
				if r.CancelRequested {
					r.State = "cancelled"
				}
			})
			return
		}
		if !snap.CancelRequested && !preparing && !active {
			for i, p := range snap.Members {
				if p.Status != "ready" {
					continue
				}
				ctx, cancel := context.WithTimeout(m.s.ctx, 8*time.Second)
				u, err := m.requestUpdate(ctx, p, snap.Target, "activate")
				cancel()
				if e := m.change(r, func() {
					if err != nil {
						r.Members[i].Error = err.Error()
					} else {
						r.Members[i].Update = &u
						r.Members[i].Status = u.Status
					}
				}); e != nil {
					return
				}
				break
			}
		}
		select {
		case <-m.s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) maintenanceActive() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.maintenance }
