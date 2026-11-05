package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

func (s *Server) guardRetainedChat(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/chats/") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/chats/"), "/")
	if len(parts) == 1 && r.Method == "DELETE" || len(parts) == 2 && (parts[1] == "restore" || parts[1] == "permanent") {
		return true
	}
	c, err := s.Store.RetainedChat(parts[0])
	if err == nil && c.DeletedAt != nil && r.Method != "GET" && r.Method != "HEAD" {
		err = store.ErrChatDeleted
	}
	if err != nil {
		respond(w, nil, err)
		return false
	}
	return true
}

func (s *Server) remoteChatUnavailable(node, id string) bool {
	return s.remoteChatUnavailableIn(node, id, s.cachedSwarmState(node))
}

func (s *Server) remoteChatUnavailableIn(node, id string, raw json.RawMessage) bool {
	var purged bool
	if s.Store.Get("swarm-remote-deletions", swarm.Key(node, id), &purged) == nil && purged {
		return true
	}
	var state struct {
		Chats []domain.Chat `json:"chats"`
	}
	if json.Unmarshal(raw, &state) == nil {
		for _, c := range state.Chats {
			if c.ID == id && c.DeletedAt != nil && !domain.Now().Before(c.DeletedAt.Add(store.ChatRetention)) {
				return true
			}
		}
	}
	return false
}

func (s *Server) removeRemoteTranscript(node, id string) {
	for _, suffix := range []string{"messages", "snapshot"} {
		_ = s.Store.Delete("swarm-transcripts", swarm.Key(node, "/api/v1/chats/"+id+"/"+suffix))
	}
}

// Inventory changes unrelated to this chat must not discard a successful
// transcript read. Only removal invalidates an in-flight cache write.
func (c *swarmCachedState) retireTranscript(id string) {
	if c.transcriptRevisions == nil {
		c.transcriptRevisions = map[string]uint64{}
	}
	c.transcriptRevisions[id]++
}

// Publish successful mutations immediately, fencing inventory requests which
// began before the mutation. The next fresh inventory remains authoritative.
func (s *Server) recordRemoteChatMutation(node string, chat domain.Chat, permanent bool) {
	s.swarmCacheMu.Lock()
	defer s.swarmCacheMu.Unlock()
	cached := s.swarmCache[node]
	if cached.state == nil {
		_ = s.Store.Get("swarm-state", node, &cached.state)
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(cached.state, &state) != nil {
		state = map[string]json.RawMessage{}
	}
	var chats []domain.Chat
	_ = json.Unmarshal(state["chats"], &chats)
	kept := []domain.Chat{}
	for _, c := range chats {
		if c.ID != chat.ID {
			kept = append(kept, c)
		}
	}
	if !permanent {
		kept = append(kept, chat)
	}
	state["chats"], _ = json.Marshal(kept)
	cached.state, _ = json.Marshal(state)
	cached.mutation++
	if permanent {
		cached.retireTranscript(chat.ID)
	}
	s.swarmCache[node] = cached
	_ = s.Store.Put("swarm-state", node, cached.state)
	if permanent {
		_ = s.Store.Put("swarm-remote-deletions", swarm.Key(node, chat.ID), true)
		s.removeRemoteTranscript(node, chat.ID)
	} else {
		_ = s.Store.Delete("swarm-remote-deletions", swarm.Key(node, chat.ID))
	}
}

func (s *Server) pruneRemoteHistory(node string, previous, next json.RawMessage) []string {
	var before, after struct {
		Chats []domain.Chat `json:"chats"`
	}
	if json.Unmarshal(previous, &before) != nil || json.Unmarshal(next, &after) != nil {
		return nil
	}
	present := map[string]bool{}
	for _, c := range after.Chats {
		present[c.ID] = true
	}
	var removed []string
	for _, c := range before.Chats {
		if !present[c.ID] {
			s.removeRemoteTranscript(node, c.ID)
			removed = append(removed, c.ID)
		}
	}
	return removed
}

// Offline entry machines also retire their own cached transcript bytes. Keep
// only expired IDs/deadlines until the next authoritative home inventory.
func (s *Server) expireRemoteChatCache() error {
	entries, err := s.Store.Entries("swarm-state")
	if err != nil {
		return err
	}
	s.swarmCacheMu.Lock()
	defer s.swarmCacheMu.Unlock()
	for node, raw := range entries {
		cached := s.swarmCache[node]
		if cached.state != nil {
			raw = cached.state
		}
		var state map[string]json.RawMessage
		if json.Unmarshal(raw, &state) != nil {
			continue
		}
		var chats []domain.Chat
		if json.Unmarshal(state["chats"], &chats) != nil {
			continue
		}
		expired := map[string]bool{}
		for i, chat := range chats {
			if chat.DeletedAt != nil && !domain.Now().Before(chat.DeletedAt.Add(store.ChatRetention)) {
				expired[chat.ID] = true
				s.removeRemoteTranscript(node, chat.ID)
				cached.retireTranscript(chat.ID)
				chats[i] = domain.Chat{ID: chat.ID, DeletedAt: chat.DeletedAt, ExpiresAt: chat.ExpiresAt}
			}
		}
		if len(expired) == 0 {
			continue
		}
		state["chats"], _ = json.Marshal(chats)
		for _, field := range []string{"runs", "questions"} {
			var records []json.RawMessage
			if json.Unmarshal(state[field], &records) != nil {
				continue
			}
			kept := []json.RawMessage{}
			for _, record := range records {
				var owner struct {
					ChatID string `json:"chatId"`
				}
				if json.Unmarshal(record, &owner) == nil && !expired[owner.ChatID] {
					kept = append(kept, record)
				}
			}
			state[field], _ = json.Marshal(kept)
		}
		cached.state, _ = json.Marshal(state)
		cached.mutation++
		s.swarmCache[node] = cached
		if err = s.Store.Put("swarm-state", node, cached.state); err != nil {
			return err
		}
	}
	return nil
}
