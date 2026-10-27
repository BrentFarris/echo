package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/brent/echo/internal/swarm"
)

type pendingSwarmControl struct {
	Node    string        `json:"node"`
	Request workerControl `json:"request"`
}

func (s *Server) recoverSwarm() error {
	workers, err := s.Store.Entries("swarm-workers")
	if err != nil {
		return err
	}
	for key, data := range workers {
		var worker workerRequest
		if err = json.Unmarshal(data, &worker); err != nil {
			return err
		}
		if worker.Status == "running" || worker.Status == "prepared" {
			worker.Status = "interrupted"
			if err = s.Store.Put("swarm-workers", key, worker); err != nil {
				return err
			}
		}
	}
	dispatches, err := s.Store.Entries("swarm-dispatches")
	if err != nil {
		return err
	}
	for key, data := range dispatches {
		var f swarmDispatch
		if err = json.Unmarshal(data, &f); err != nil {
			return err
		}
		if f.Status == "running" || f.Status == "queued" {
			f.Status = "interrupted"
			if err = s.Store.Put("swarm-dispatches", key, &f); err != nil {
				return err
			}
		}
	}
	s.swarmWG.Add(1)
	go func() {
		defer s.swarmWG.Done()
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				s.reconcileSwarm()
			}
		}
	}()
	return nil
}

// Reconciliation never starts a worker. A process crash leaves local execution
// interrupted while workers which remained alive can finish and report results.
func (s *Server) reconcileSwarm() {
	if !s.Swarm.Enabled() {
		return
	}
	pending, _ := s.Store.Entries("swarm-cancellations")
	for key, data := range pending {
		if s.ctx.Err() != nil {
			return
		}
		var item pendingSwarmControl
		if json.Unmarshal(data, &item) != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
		err := s.Swarm.JSON(ctx, item.Node, "POST", "/api/v1/swarm/workers/control", item.Request, nil)
		cancel()
		if err == nil {
			_ = s.Store.Delete("swarm-cancellations", key)
		}
	}
	entries, _ := s.Store.Entries("swarm-dispatches")
	for id, data := range entries {
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		active := s.dispatches[id] != nil
		s.mu.Unlock()
		if active {
			continue
		}
		var f swarmDispatch
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.Reconciled == nil {
			f.Reconciled = map[string]bool{}
		}
		deleted := s.Store.ChatDeleted(f.ChatID)
		if deleted && f.Status != "deleted" {
			f.Status = "deleted"
			f.Reconciled = map[string]bool{}
		}
		changed := false
		for node, req := range f.Hosts {
			if f.Reconciled[node] || node == s.Swarm.Node().ID || !s.Swarm.Authorized(node) {
				continue
			}
			input := workerControl{Home: req.Home, ID: req.ID, Token: req.Token}
			ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
			if deleted {
				input.Action = "delete"
				if s.Swarm.JSON(ctx, node, "POST", "/api/v1/swarm/workers/control", input, nil) == nil {
					f.Reconciled[node] = true
					changed = true
				}
				cancel()
				continue
			}
			var snapshot workerSnapshot
			err := s.Swarm.JSON(ctx, node, "POST", "/api/v1/swarm/workers/snapshot", input, &snapshot)
			cancel()
			if err != nil {
				continue
			}
			s.mergeWorker(&f, node, snapshot)
			if snapshot.Status == "completed" || snapshot.Status == "interrupted" || snapshot.Status == "cancelled" || snapshot.Status == "deleted" {
				f.Reconciled[node] = true
				changed = true
			}
		}
		if changed && s.Store.ChatDeleted(f.ChatID) == deleted {
			_ = s.Store.Put("swarm-dispatches", id, &f)
		}
	}
}

func (s *Server) persistSwarmStop(f *swarmDispatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Status = "cancelled"
	values := map[string]any{}
	for node, req := range f.Hosts {
		if node != s.Swarm.Node().ID {
			values[swarm.Key(node, f.ID)] = pendingSwarmControl{Node: node, Request: workerControl{Home: req.Home, ID: req.ID, Token: req.Token, Action: "stop"}}
		}
	}
	return s.Store.PutRecords(map[string]map[string]any{
		"swarm-cancellations": values,
		"swarm-dispatches":    {f.ID: f},
	})
}
