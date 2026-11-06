package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) initSwarm() error {
	s.swarmCache = make(map[string]swarmCachedState)
	s.swarmRefresh = make(chan struct{}, 1)
	host, _ := os.Hostname()
	manager, err := swarm.New(s.ctx, swarm.Config{
		Store: s.Store, Secrets: swarmSecrets{s}, SecretPrefix: "Echo2/" + s.Dir + "/swarm", IdentityFile: filepath.Join(s.Dir, "swarm-identity.key"), Name: host, Epoch: s.Development.InstanceID(),
		Enabled: func() bool { p, err := s.Plugins.Get("swarm"); return err == nil && p.Enabled && !p.Pending },
		Enable: func() error {
			p, err := s.Plugins.Get("swarm")
			if err != nil {
				return err
			}
			p.Enabled = true
			return s.Plugins.Save(p)
		},
		CheckPassword:    s.swarmPassword,
		AcceptInvitation: s.acceptSwarmInvitation,
		Handler:          http.HandlerFunc(s.serveSwarmOwner),
		Changed:          func() { s.requestSwarmRefresh(); s.emit("changed", "", "", "", nil) },
	})
	if err != nil {
		return err
	}
	s.Swarm = manager
	s.startSwarmCache()
	plugin, _ := s.Plugins.Get("swarm")
	name := text(plugin.Config, "name")
	if name == "" {
		name = manager.Node().Name
	}
	return manager.Configure(name, text(plugin.Config, "advertiseUrl"))
}

type swarmSecrets struct{ server *Server }

func (s swarmSecrets) Get(ctx context.Context, key string) (string, error) {
	return s.server.Secrets.Get(ctx, key)
}
func (s swarmSecrets) Set(ctx context.Context, key, value string) error {
	return s.server.Secrets.Set(ctx, key, value)
}
func (s swarmSecrets) Delete(ctx context.Context, key string) error {
	return s.server.Secrets.Delete(ctx, key)
}

func (s *Server) swarmPassword(ctx context.Context, password, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	key := "swarm:" + address
	now := time.Now()
	recent := []time.Time{}
	for _, at := range s.loginAttempts[key] {
		if now.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	if len(recent) >= 5 {
		return errors.New("too many pairing attempts; wait a minute")
	}
	var hash string
	if password == "" || s.Store.Get("auth", "password", &hash) != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		s.loginAttempts[key] = append(recent, now)
		return errors.New("invalid remote owner password")
	}
	delete(s.loginAttempts, key)
	return nil
}

// The peer receives a target-local API, never a proxy with this host's other
// peer credentials. In particular /state is local and /swarm/state is blocked.
func (s *Server) serveSwarmOwner(w http.ResponseWriter, r *http.Request) {
	// Swarm reconnects while New is still recovering state. Gate requests
	// before constructing routes or reading any partially initialized service.
	if !s.ready.Load() {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Echo is starting up or shutting down; retry shortly"})
		return
	}
	caller, ok := swarm.Caller(r.Context())
	if !ok {
		http.Error(w, "Swarm principal required", 401)
		return
	}
	path := r.URL.Path
	if caller.Pairing {
		if path != "/api/v1/swarm/pair" || r.Method != "POST" {
			http.Error(w, "Pairing only", 403)
			return
		}
	} else if !strings.HasPrefix(path, "/api/v1/") || path == "/api/v1/session" || path == "/api/v1/swarm/state" || path == "/api/v1/swarm/events" || strings.HasPrefix(path, "/api/v1/swarm/instances/") || path == "/api/v1/swarm/link" {
		http.Error(w, "This operation is local to its entry instance", 403)
		return
	}
	s.Handler().ServeHTTP(w, r)
}

func (s *Server) swarmRoutes(mux *http.ServeMux) {
	s.swarmAccessRoutes(mux)
	s.swarmWorkerRoutes(mux)
	s.groupRoutingRoutes(mux)
	mux.HandleFunc("POST /api/v1/swarm/pair", func(w http.ResponseWriter, r *http.Request) {
		var input swarm.PairRequest
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		if caller, ok := swarm.Caller(r.Context()); ok && caller.NodeID != input.Node.ID && (input.InvitationPacket == nil || input.InvitationPacket.From.ID != caller.NodeID) {
			http.Error(w, "Pairing identity mismatch", 403)
			return
		}
		value, err := s.Swarm.Pair(r.Context(), input, requestAddress(r))
		respond(w, value, err)
	})
	mux.HandleFunc("GET /api/v1/swarm/link", func(w http.ResponseWriter, r *http.Request) { s.Swarm.LinkHTTP(w, r) })
	mux.HandleFunc("GET /api/v1/swarm/peers", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"node": s.Swarm.Node(), "peers": s.Swarm.Directory(), "enabled": s.Swarm.Enabled()}, nil)
	})
	mux.HandleFunc("POST /api/v1/swarm/peers", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			URL      string `json:"url"`
			NodeID   string `json:"nodeId"`
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		if input.Name != "" && !swarm.ValidName(input.Name) {
			respond(w, nil, errors.New("invalid machine namespace"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		node, err := s.Swarm.Connect(ctx, input.URL, input.NodeID, input.Password, true)
		if err == nil && input.Name != "" && input.Name != node.Name {
			var plugin map[string]any
			err = s.Swarm.JSON(ctx, node.ID, "GET", "/api/v1/state", nil, &plugin)
			if err == nil {
				config := map[string]any{"name": input.Name, "advertiseUrl": node.URL}
				err = s.Swarm.JSON(ctx, node.ID, "PATCH", "/api/v1/plugins/swarm", map[string]any{"enabled": true, "config": config}, nil)
				if err == nil {
					node.Name = input.Name
				}
			}
		}
		respond(w, node, err)
	})
	mux.HandleFunc("PATCH /api/v1/swarm/peers/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Disconnected bool `json:"disconnected"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		err := s.Swarm.SetDisconnected(r.PathValue("id"), input.Disconnected)
		if err == nil && input.Disconnected {
			s.revokeAccessWork(r.PathValue("id"))
		}
		respond(w, map[string]bool{"disconnected": input.Disconnected}, err)
	})
	mux.HandleFunc("DELETE /api/v1/swarm/peers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		_ = s.Swarm.JSON(ctx, id, "POST", "/api/v1/swarm/revoke", nil, nil)
		cancel()
		err := s.Swarm.Revoke(id)
		if err == nil {
			s.revokeAccessWork(id)
		}
		respond(w, map[string]bool{"revoked": err == nil}, err)
	})
	mux.HandleFunc("POST /api/v1/swarm/revoke", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := swarm.Caller(r.Context())
		if !ok || caller.Pairing {
			http.Error(w, "Peer connection required", 403)
			return
		}
		// Finish the response before closing the tunnel carrying it.
		respond(w, map[string]bool{"revoked": true}, nil)
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = s.Swarm.Revoke(caller.NodeID)
			s.revokeAccessWork(caller.NodeID)
		}()
	})
	mux.HandleFunc("GET /api/v1/swarm/state", s.swarmState)
	mux.HandleFunc("GET /api/v1/swarm/events", s.swarmEvents)
	mux.HandleFunc("/api/v1/swarm/instances/{nodeId}/{path...}", s.swarmProxy)
}

// Bind bare handles to the browser entry. For reruns, the home supplies its
// original IDs instead. In both cases the entry signs the concrete recipients.
func (s *Server) prepareForwardedMessage(w http.ResponseWriter, r *http.Request, home, chat, path, action string) (bool, error) {
	var req SendRequest
	if err := decode(w, r, &req); err != nil {
		return false, err
	}
	req.OriginNodeID = s.Swarm.Node().ID
	if req.RequestID == "" {
		req.RequestID = domain.ID()
	}
	// An acknowledged request wins even if the group was edited or deleted
	// before the browser could retry a lost response.
	if action != "rerun" {
		var scope groupIntent
		if s.Store.Get("group-intents", intentID(home, chat, req.RequestID), &scope) == nil {
			var accepted string
			err := s.Swarm.JSON(r.Context(), home, "POST", "/api/v1/swarm/workers/group-receipt", groupReceipt{Chat: chat, Request: req.RequestID}, &accepted)
			if err != nil {
				return false, err
			}
			if accepted != "" {
				respond(w, map[string]string{"dispatchId": accepted}, nil)
				return false, nil
			}
		}
	}
	if action == "resubmit" {
		req.Segments = nil
	}
	if action == "rerun" {
		var input struct {
			domain.Message
			DispatchID string `json:"dispatchId"`
		}
		err := s.Swarm.JSON(r.Context(), home, "POST", path+"-input", map[string]string{"requestId": req.RequestID}, &input)
		if err != nil {
			return false, err
		}
		if input.DispatchID != "" {
			respond(w, map[string]string{"dispatchId": input.DispatchID}, nil)
			return false, nil
		}
		req.Content, req.Segments, req.rerunMessageID = input.Content, input.Segments, "remote"
		req.inputAuthor = input.Author
		if len(input.Recipients) == 0 && slices.ContainsFunc(input.Segments, func(g domain.Segment) bool { return g.DeliveryMode == "one" && len(g.Members) == 0 }) {
			req.rerunMessageID = ""
			req.forwardedGroups = true
		}
		for _, id := range input.Recipients {
			if node, _ := swarm.Split(id); node == "" {
				id = swarm.Key(home, id)
			}
			req.rerunRecipients = append(req.rerunRecipients, id)
		}
	}
	participants, segments, err := s.resolveSwarm(r.Context(), req)
	if err != nil {
		return false, err
	}
	if hasGroupSegment(segments) {
		if err := s.checkGroupCapability(home); err != nil {
			return false, err
		}
	}
	req.Segments = slices.Clone(segments)
	request := workerRequest{Home: home, Chat: domain.Chat{ID: chat}, RequestID: req.RequestID, Message: domain.Message{Content: req.Content, Segments: segments}, Participants: participants}
	req.Permits = map[string]string{}
	deferred := slices.ContainsFunc(segments, func(g domain.Segment) bool { return g.DeliveryMode == "one" && len(g.Members) == 0 })
	if slices.ContainsFunc(segments, func(g domain.Segment) bool { return g.Type == "group" && g.DeliveryMode == "one" }) {
		if err := s.checkOnePeer(home); err != nil {
			return false, err
		}
	}
	if deferred {
		if err := s.rememberGroupIntent(request); err != nil {
			return false, err
		}
	}
	fingerprint := requestFingerprint(request)
	for _, p := range participants {
		req.Segments = append(req.Segments, domain.Segment{Type: "bot", NodeID: p.Ref.NodeID, BotID: p.Ref.ID, Text: "@" + p.Handle})
		if deferred || p.Ref.NodeID == req.OriginNodeID || req.Permits[p.Ref.NodeID] != "" {
			continue
		}
		proof, err := s.Swarm.Proof(p.Ref.NodeID, fingerprint)
		if err != nil {
			return false, err
		}
		req.Permits[p.Ref.NodeID] = proof
	}
	var body any = req
	switch action {
	case "resubmit":
		edit := map[string]any{"content": req.Content, "requestId": req.RequestID}
		if s.groupCapability(home) {
			edit["segments"], edit["permits"] = req.Segments, req.Permits
		}
		body = edit
	case "rerun":
		body = map[string]any{"requestId": req.RequestID, "permits": req.Permits}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return false, err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	return true, nil
}

func (s *Server) swarmProxy(w http.ResponseWriter, r *http.Request) {
	if !s.Swarm.Enabled() {
		http.Error(w, "Swarm is disabled", 503)
		return
	}
	id := r.PathValue("nodeId")
	if id == s.Swarm.Node().ID {
		http.Error(w, "Use the local API for this instance", 400)
		return
	}
	if !s.swarmCacheAuthorized(id) {
		http.Error(w, swarm.ErrUnauthorized.Error(), 403)
		return
	}
	path := "/api/v1/" + r.PathValue("path")
	if strings.Contains(path, "..") || strings.Contains(path, "\\") || strings.HasPrefix(path, "/api/v1/swarm/workers/") || strings.HasPrefix(path, "/api/v1/swarm/instances/") || path == "/api/v1/swarm/state" || path == "/api/v1/swarm/events" || path == "/api/v1/session" {
		http.Error(w, "Invalid target API", 400)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	chatDelete := r.Method == "DELETE" && len(parts) == 2 && parts[0] == "chats"
	chatRestore := r.Method == "POST" && len(parts) == 3 && parts[0] == "chats" && parts[2] == "restore"
	chatPurge := r.Method == "DELETE" && len(parts) == 3 && parts[0] == "chats" && parts[2] == "permanent"
	if chatDelete || chatRestore || chatPurge {
		capable := false
		for _, peer := range s.Swarm.Directory() {
			if peer.Node.ID == id {
				capable = slices.Contains(peer.Node.Capabilities, "chat-retention/1")
			}
		}
		if !capable {
			http.Error(w, "Update this chat's machine to support 30-day retention", http.StatusConflict)
			return
		}
	}
	if r.Method == "GET" && len(parts) >= 3 && parts[0] == "chats" && s.remoteChatUnavailable(id, parts[1]) {
		s.removeRemoteTranscript(id, parts[1])
		respond(w, nil, store.ErrChatExpired)
		return
	}
	if r.Method != "GET" && !s.swarmAvailable(id) {
		respond(w, nil, s.offlineMachine(id, ""))
		return
	}
	if r.Method == "POST" && len(parts) >= 3 && parts[0] == "chats" && parts[2] == "messages" {
		send := len(parts) == 3
		action := ""
		if len(parts) == 5 {
			action = parts[4]
		}
		if send || action == "resubmit" || (action == "rerun" && s.groupCapability(id)) {
			forward, err := s.prepareForwardedMessage(w, r, id, parts[1], path, action)
			if err != nil {
				respond(w, nil, err)
				return
			}
			if !forward {
				return
			}
		}
	}
	cacheable := r.Method == "GET" && len(parts) == 3 && parts[0] == "chats" && (parts[2] == "messages" || parts[2] == "snapshot")
	cacheKey := swarm.Key(id, path)
	s.swarmCacheMu.Lock()
	cacheRevision := uint64(0)
	if cacheable {
		cacheRevision = s.swarmCache[id].transcriptRevisions[parts[1]]
	}
	s.swarmCacheMu.Unlock()
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.Out.URL = &url.URL{Scheme: "http", Host: "swarm", Path: path, RawQuery: p.In.URL.RawQuery}
		p.Out.Host = "swarm"
		p.Out.Header.Del("Cookie")
		p.Out.Header.Del("Authorization")
		p.Out.Header.Del("Origin")
		p.Out.Header.Del("Forwarded")
		p.Out.Header.Del("X-Forwarded-For")
		p.Out.Header.Del("X-Forwarded-Host")
		p.Out.Header.Del("X-Forwarded-Proto")
	}, Transport: s.Swarm.Client(id).Transport, FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			if response.StatusCode < 300 && r.Method != "GET" {
				s.swarmCacheMu.Lock()
				cached := s.swarmCache[id]
				cached.mutation++
				s.swarmCache[id] = cached
				s.swarmCacheMu.Unlock()
				s.requestSwarmRefresh()
			}
			response.Header.Del("Set-Cookie")
			response.Header.Set("Cache-Control", "no-store")
			if cacheable && response.StatusCode == 200 {
				body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20+1))
				response.Body.Close()
				if err != nil {
					return err
				}
				if len(body) > 64<<20 {
					return errors.New("remote transcript exceeds cache limit")
				}
				response.Body = io.NopCloser(bytes.NewReader(body))
				s.swarmCacheMu.Lock()
				cached := s.swarmCache[id]
				if cached.transcriptRevisions[parts[1]] == cacheRevision && !s.remoteChatUnavailableIn(id, parts[1], cached.state) {
					_ = s.Store.Put("swarm-transcripts", cacheKey, json.RawMessage(body))
				}
				s.swarmCacheMu.Unlock()
			}
			if response.StatusCode < 300 && (chatDelete || chatRestore || chatPurge) {
				chat := domain.Chat{ID: parts[1]}
				permanent := chatPurge
				if !chatPurge {
					body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
					response.Body.Close()
					if err != nil {
						return err
					}
					response.Body = io.NopCloser(bytes.NewReader(body))
					var retained *domain.Chat
					if err = json.Unmarshal(body, &retained); err != nil {
						return err
					}
					if retained != nil {
						chat = *retained
					} else if chatDelete {
						permanent = true
					} else {
						return errors.New("remote restore returned no chat")
					}
				}
				s.recordRemoteChatMutation(id, chat, permanent)
				s.requestSwarmRefresh()
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if cacheable && s.remoteChatUnavailable(id, parts[1]) {
				s.removeRemoteTranscript(id, parts[1])
				respond(w, nil, store.ErrChatExpired)
				return
			}
			var cached json.RawMessage
			if cacheable && s.swarmCacheAuthorized(id) && s.Store.Get("swarm-transcripts", cacheKey, &cached) == nil {
				w.Header().Set("X-Echo-Cached", "true")
				respond(w, cached, nil)
				return
			}
			http.Error(w, "Remote Echo is unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
	if !s.swarmAvailable(id) {
		proxy.ErrorHandler(w, r, s.offlineMachine(id, ""))
		return
	}
	proxy.ServeHTTP(w, r)
}

type instanceState struct {
	Node   swarm.Node      `json:"node"`
	State  json.RawMessage `json:"state,omitempty"`
	Online bool            `json:"online"`
	Error  string          `json:"error,omitempty"`
}

func (s *Server) filterRemoteDeleted(node string, raw json.RawMessage) json.RawMessage {
	var state map[string]json.RawMessage
	if json.Unmarshal(raw, &state) != nil {
		return raw
	}
	var chats []json.RawMessage
	if json.Unmarshal(state["chats"], &chats) != nil {
		return raw
	}
	kept := []json.RawMessage{}
	visible := map[string]bool{}
	for _, chat := range chats {
		var c domain.Chat
		var deleted bool
		if json.Unmarshal(chat, &c) == nil && c.DeletedAt != nil && !domain.Now().Before(c.DeletedAt.Add(store.ChatRetention)) {
			s.removeRemoteTranscript(node, c.ID)
			continue
		}
		if s.Store.Get("swarm-remote-deletions", swarm.Key(node, c.ID), &deleted) != nil || !deleted {
			kept = append(kept, chat)
			visible[c.ID] = true
		}
	}
	state["chats"], _ = json.Marshal(kept)
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
			if json.Unmarshal(record, &owner) == nil && visible[owner.ChatID] {
				kept = append(kept, record)
			}
		}
		state[field], _ = json.Marshal(kept)
	}
	result, err := json.Marshal(state)
	if err != nil {
		return raw
	}
	return result
}

func (s *Server) swarmState(w http.ResponseWriter, r *http.Request) {
	peers := s.Swarm.Directory()
	instances := []instanceState{}
	if s.Swarm.Enabled() {
		for _, p := range peers {
			if !p.Authorized || p.Blocked {
				continue
			}
			entry := instanceState{Node: p.Node, Online: s.swarmAvailable(p.Node.ID)}
			entry.State = s.filterRemoteDeleted(p.Node.ID, s.cachedSwarmState(p.Node.ID))
			if !entry.Online {
				entry.Error = s.offlineMachine(p.Node.ID, "").Error()
			}
			instances = append(instances, entry)
		}
	}
	respond(w, map[string]any{"node": s.Swarm.Node(), "enabled": s.Swarm.Enabled(), "peers": peers, "instances": instances}, nil)
}

// The browser receives an envelope per source. Its snapshots and sequence
// watermarks remain independent when a peer restarts or a route changes.
func (s *Server) swarmEvents(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		u, err := url.Parse(r.Header.Get("Origin"))
		return err == nil && u.Host == r.Host
	}}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		conn.SetReadLimit(1024)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()
	out := make(chan any, 256)
	watching := map[string]context.CancelFunc{}
	defer func() {
		for _, stop := range watching {
			stop()
		}
	}()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	update := func() {
		wanted := map[string]bool{}
		if s.Swarm.Enabled() {
			for _, p := range s.Swarm.Directory() {
				if p.Authorized && !p.Blocked {
					wanted[p.Node.ID] = true
					if watching[p.Node.ID] == nil {
						child, stop := context.WithCancel(ctx)
						watching[p.Node.ID] = stop
						go s.forwardSwarmEvents(child, p.Node.ID, out)
					}
				}
			}
		}
		for id, stop := range watching {
			if !wanted[id] {
				stop()
				delete(watching, id)
			}
		}
	}
	update()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !s.authenticated(r) {
				return
			}
			update()
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(map[string]any{"type": "directory", "node": s.Swarm.Node(), "peers": s.Swarm.Directory()}); err != nil {
				return
			}
		case value := <-out:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(value); err != nil {
				return
			}
		}
	}
}

func (s *Server) forwardSwarmEvents(ctx context.Context, id string, out chan<- any) {
	for ctx.Err() == nil {
		conn, err := s.Swarm.Watch(ctx, id, "/api/v1/events")
		if err == nil {
			done := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					conn.Close()
				case <-done:
				}
			}()
			for {
				var event json.RawMessage
				if err = conn.ReadJSON(&event); err != nil {
					break
				}
				var header struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(event, &header) == nil && (header.Type == "changed" || header.Type == "run" || header.Type == "reset") {
					s.requestSwarmRefresh()
				}
				select {
				case out <- map[string]any{"nodeId": id, "event": event}:
				case <-ctx.Done():
					conn.Close()
					close(done)
					return
				}
			}
			conn.Close()
			close(done)
		}
		select {
		case out <- map[string]any{"nodeId": id, "error": fmt.Sprint(swarm.ErrUnavailable)}:
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}
