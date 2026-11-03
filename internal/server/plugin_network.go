package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/swarm"
	"github.com/gorilla/websocket"
)

func pluginNetworkMethod(method string) bool {
	switch method {
	case "web.get", "web.request", "websocket.open", "websocket.send", "websocket.receive", "websocket.close":
		return true
	}
	return false
}

func (s *Server) pluginNetworkCall(ctx context.Context, manifest domain.PluginManifest, method string, raw json.RawMessage) (any, error) {
	if len(raw) > broker.NetworkWireLimit {
		return nil, errors.New("network request exceeds 2 MiB bridge limit")
	}
	var origins []string
	if manifest.Network != nil {
		origins = manifest.Network.Origins
	}
	if strings.HasPrefix(method, "websocket.") {
		return broker.NetworkSocketCall(ctx, method, raw, origins)
	}
	var args broker.NetworkRequest
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if method == "web.get" {
		args = broker.NetworkRequest{URL: args.URL, Method: "GET"}
	}
	return broker.NetworkFetch(ctx, args, origins)
}

// watchPluginNetwork also covers changes to bot selections, owner sessions and
// swarm authorization. Package saves revoke the host lease synchronously.
func (s *Server) watchPluginNetwork(ctx context.Context, cancel context.CancelFunc, r *http.Request, botID, pluginID string) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ctx.Done():
			cancel()
			return
		case <-ticker.C:
			bot, err := s.Bot(botID)
			if err != nil || !s.Plugins.Enabled(bot, pluginID) || !s.authenticated(r) {
				cancel()
				return
			}
		}
	}
}

func (s *Server) pluginBridge(w http.ResponseWriter, r *http.Request) {
	var a struct {
		BotID  string          `json:"botId"`
		Digest string          `json:"digest"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := decode(w, r, &a); err != nil {
		respond(w, nil, err)
		return
	}
	if a.BotID == "" {
		a.BotID = "echo"
	}
	bot, err := s.Bot(a.BotID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if a.Method != "storage.get" && a.Method != "storage.set" && a.Method != "storage.delete" && a.Method != "web.get" && a.Method != "web.request" {
		respond(w, nil, errors.New("UI bridge supports declared storage and HTTP methods; use the network session for WebSockets"))
		return
	}
	ctx := r.Context()
	if pluginNetworkMethod(a.Method) {
		var release func()
		ctx, release, err = s.Plugins.Lease(ctx, bot, r.PathValue("id"), a.Digest)
		if err != nil {
			respond(w, nil, err)
			return
		}
		defer release()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go s.watchPluginNetwork(ctx, cancel, r, bot.ID, r.PathValue("id"))
		value, err := s.pluginCall(ctx, domain.ExecutionScope{Bot: bot, PluginID: r.PathValue("id")}, a.Method, a.Params)
		respond(w, value, err)
		return
	}
	value, err := s.pluginCall(ctx, domain.ExecutionScope{Bot: bot, PluginID: r.PathValue("id")}, a.Method, a.Params)
	respond(w, value, err)
}

// The trusted parent opens this owner-authenticated relay. The opaque iframe
// only sees its nonce bridge; it never receives a session cookie or relay URL.
func (s *Server) pluginNetworkRelay(w http.ResponseWriter, r *http.Request) {
	botID := r.URL.Query().Get("botId")
	if botID == "" {
		botID = "echo"
	}
	bot, err := s.Bot(botID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	id, digest := r.PathValue("id"), r.URL.Query().Get("digest")
	m, err := s.Plugins.Get(id)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if _, err = plugins.FloatingView(m, r.URL.Query().Get("viewId")); err != nil {
		respond(w, nil, err)
		return
	}
	ctx, release, err := s.Plugins.Lease(r.Context(), bot, id, digest)
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx, closeNetwork := broker.WithNetworkSession(ctx)
	defer closeNetwork()
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		if swarm.Owner(r.Context()) {
			return true
		}
		u, err := url.Parse(r.Header.Get("Origin"))
		return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
	}}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	go s.watchPluginNetwork(ctx, cancel, r, botID, id)
	// Join the existing connected-device revocation mechanism.
	key := sessionKey(r)
	if caller, ok := swarm.Caller(r.Context()); ok {
		key = "swarm:" + caller.NodeID
	}
	s.authMu.Lock()
	if s.sessionSockets == nil {
		s.sessionSockets = map[string]map[*websocket.Conn]bool{}
	}
	if s.sessionSockets[key] == nil {
		s.sessionSockets[key] = map[*websocket.Conn]bool{}
	}
	s.sessionSockets[key][conn] = true
	s.authMu.Unlock()
	defer func() { s.authMu.Lock(); delete(s.sessionSockets[key], conn); s.authMu.Unlock() }()
	conn.SetReadLimit(broker.NetworkWireLimit)
	var writes, pending sync.Mutex
	var wg sync.WaitGroup
	inflight := map[string]bool{}
	bytes := 0
	responseBytes := 0
	defer func() { cancel(); closeNetwork(); wg.Wait() }()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var call struct {
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &call) != nil || call.ID == "" || len(call.ID) > 128 || !pluginNetworkMethod(call.Method) {
			return
		}
		pending.Lock()
		if inflight[call.ID] || len(inflight) >= 32 || bytes+len(data) > 4<<20 {
			pending.Unlock()
			return
		}
		inflight[call.ID] = true
		bytes += len(data)
		pending.Unlock()
		wg.Add(1)
		go func(requestID, method string, raw json.RawMessage, size int) {
			defer wg.Done()
			defer func() { pending.Lock(); delete(inflight, requestID); bytes -= size; pending.Unlock() }()
			if ctx.Err() != nil || !s.authenticated(r) {
				cancel()
				return
			}
			currentBot, err := s.Bot(botID)
			var result any
			if err == nil {
				result, err = s.pluginCall(ctx, domain.ExecutionScope{Bot: currentBot, PluginID: id}, method, raw)
			}
			reply := map[string]any{"id": requestID, "result": result}
			if err != nil {
				reply = map[string]any{"id": requestID, "error": err.Error()}
			}
			encoded, _ := json.Marshal(reply)
			if len(encoded) > broker.NetworkWireLimit {
				encoded, _ = json.Marshal(map[string]any{"id": requestID, "error": "network response exceeds bridge limit"})
			}
			pending.Lock()
			if responseBytes+len(encoded) > 4<<20 {
				pending.Unlock()
				cancel()
				return
			}
			responseBytes += len(encoded)
			pending.Unlock()
			defer func() { pending.Lock(); responseBytes -= len(encoded); pending.Unlock() }()
			writes.Lock()
			defer writes.Unlock()
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteMessage(websocket.TextMessage, encoded) != nil {
				cancel()
			}
		}(call.ID, call.Method, call.Params, len(data))
	}
}
