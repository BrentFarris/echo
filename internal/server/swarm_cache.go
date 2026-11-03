package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type swarmCachedState struct {
	state  json.RawMessage
	token  string
	failed bool
}

func (s *Server) requestSwarmRefresh() {
	select {
	case s.swarmRefresh <- struct{}{}:
	default:
	}
}

func (s *Server) swarmAvailable(id string) bool {
	if id == s.Swarm.Node().ID {
		return true
	}
	token := s.Swarm.RouteToken(id)
	if token == "" {
		return false
	}
	s.swarmCacheMu.Lock()
	defer s.swarmCacheMu.Unlock()
	cached := s.swarmCache[id]
	return cached.token != token || !cached.failed
}

// Disconnect suspends transport but retains permission to cached history.
func (s *Server) swarmCacheAuthorized(id string) bool {
	for _, peer := range s.Swarm.Directory() {
		if peer.Node.ID == id {
			return peer.Authorized && !peer.Blocked
		}
	}
	return false
}

func (s *Server) offlineMachine(id, bot string) error {
	if bot != "" {
		return fmt.Errorf("Machine '%s' is offline; @%s is unavailable", s.Swarm.Namespace(id), bot)
	}
	return fmt.Errorf("Machine '%s' is offline", s.Swarm.Namespace(id))
}

func (s *Server) cachedSwarmState(id string) json.RawMessage {
	s.swarmCacheMu.Lock()
	cached := s.swarmCache[id].state
	s.swarmCacheMu.Unlock()
	if cached == nil {
		_ = s.Store.Get("swarm-state", id, &cached)
	}
	return cached
}

// One scheduler owns all cache jobs, independent of browser lifetimes. Network
// I/O never holds the cache lock or delays another machine's publication.
func (s *Server) startSwarmCache() {
	s.swarmWG.Add(1)
	go func() {
		defer s.swarmWG.Done()
		var jobs sync.WaitGroup
		defer jobs.Wait()
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		active := map[string]bool{}
		pending := map[string]bool{}
		started := map[string]time.Time{}
		done := make(chan string, 8)
		s.requestSwarmRefresh()
		for {
			refreshAll := true
			select {
			case <-s.ctx.Done():
				return
			case id := <-done:
				delete(active, id)
				refreshAll = false
			case <-tick.C:
			case <-s.swarmRefresh:
			}
			peers := s.Swarm.Directory()
			if refreshAll {
				for _, peer := range peers {
					pending[peer.Node.ID] = true
				}
			}
			sort.SliceStable(peers, func(i, j int) bool { return started[peers[i].Node.ID].Before(started[peers[j].Node.ID]) })
			for _, peer := range peers {
				id := peer.Node.ID
				token := s.Swarm.RouteToken(id)
				if token == "" {
					delete(pending, id)
					continue
				}
				if !pending[id] || active[id] || len(active) >= 8 {
					continue
				}
				delete(pending, id)
				active[id] = true
				started[id] = time.Now()
				jobs.Add(1)
				go func() {
					defer jobs.Done()
					defer func() { done <- id }()
					ctx, cancel := context.WithTimeout(s.ctx, 4*time.Second)
					defer cancel()
					var raw json.RawMessage
					err := s.Swarm.JSON(ctx, id, "GET", "/api/v1/state", nil, &raw)
					if s.ctx.Err() != nil || s.Swarm.RouteToken(id) != token {
						return
					}
					s.swarmCacheMu.Lock()
					previous := s.swarmCache[id]
					changed := previous.token != token || previous.failed != (err != nil)
					if err == nil {
						changed = changed || !bytes.Equal(previous.state, raw)
						previous.state = raw
						if changed {
							_ = s.Store.Put("swarm-state", id, raw)
						}
					}
					previous.token, previous.failed = token, err != nil
					s.swarmCache[id] = previous
					s.swarmCacheMu.Unlock()
					if changed {
						s.emit("changed", "", "", "", nil)
					}
				}()
			}
		}
	}()
}
