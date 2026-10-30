package swarm

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type packet struct {
	From  Node     `json:"from"`
	To    string   `json:"to"`
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Route []string `json:"route"`
	Nonce []byte   `json:"nonce"`
	Data  []byte   `json:"data"`
}
type wire struct {
	Packet *packet `json:"packet,omitempty"`
	Routes []Route `json:"routes,omitempty"`
}
type link struct {
	m        *Manager
	peer, id string
	conn     *websocket.Conn
	queue    chan wire
	done     chan struct{}
	once     sync.Once
	routes   []Route // Manager.mu
}

func (l *link) close() { l.once.Do(func() { close(l.done); _ = l.conn.Close() }) }
func (l *link) send(value wire) error {
	select {
	case <-l.done:
		return ErrUnavailable
	default:
	}
	select {
	case l.queue <- value:
		return nil
	case <-l.done:
		return ErrUnavailable
	default:
		l.close()
		return errors.New("Swarm link is too slow; reconnect to recover")
	}
}

func (m *Manager) LinkHTTP(w http.ResponseWriter, r *http.Request) {
	id, linkID := r.Header.Get("X-Echo-Node"), r.Header.Get("X-Echo-Link")
	if len(linkID) != 64 || !m.authenticate(id, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		http.Error(w, "Swarm authorization required", http.StatusUnauthorized)
		return
	}
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }, ReadBufferSize: frameSize, WriteBufferSize: frameSize}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	m.installLink(id, linkID, conn)
}

func (m *Manager) connectLink(ctx context.Context, id string) error {
	m.mu.Lock()
	p := m.peers[id]
	existing := m.links[id] != nil
	m.mu.Unlock()
	if existing {
		return nil
	}
	if !p.Authorized || p.Blocked || p.Disabled || p.Node.URL == "" || !m.Enabled() {
		return ErrUnavailable
	}
	token, err := m.config.Secrets.Get(ctx, p.Outbound)
	if err != nil {
		return err
	}
	endpoint := strings.Replace(p.Node.URL, "http", "ws", 1) + "/api/v1/swarm/link"
	linkID := randomID()
	header := http.Header{"Authorization": []string{"Bearer " + token}, "X-Echo-Node": []string{m.Node().ID}, "X-Echo-Link": []string{linkID}}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: nil, ReadBufferSize: frameSize, WriteBufferSize: frameSize}
	conn, response, err := dialer.DialContext(ctx, endpoint, header)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return err
	}
	m.installLink(id, linkID, conn)
	return nil
}

func (m *Manager) installLink(id, linkID string, conn *websocket.Conn) {
	l := &link{m: m, peer: id, id: linkID, conn: conn, queue: make(chan wire, 256), done: make(chan struct{})}
	m.mu.Lock()
	p := m.peers[id]
	old := m.links[id]
	if m.closed || !p.Authorized || p.Blocked || p.Disabled || old != nil && old.id < linkID {
		m.mu.Unlock()
		l.close()
		return
	}
	l.routes = []Route{{Node: p.Node, Path: []string{id}}}
	m.links[id] = l
	m.wg.Add(2)
	m.mu.Unlock()
	if old != nil {
		old.close()
	}
	go l.read()
	go l.write()
	m.announce()
	m.changed()
}

func (l *link) write() {
	defer l.m.wg.Done()
	defer l.close()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-l.m.ctx.Done():
			return
		case value := <-l.queue:
			_ = l.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if err := l.conn.WriteJSON(value); err != nil {
				return
			}
		case <-ticker.C:
			if err := l.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func (l *link) read() {
	defer l.m.wg.Done()
	defer l.close()
	defer func() {
		l.m.mu.Lock()
		current := l.m.links[l.peer] == l
		if current {
			delete(l.m.links, l.peer)
		}
		streams := []*stream{}
		if current {
			for _, s := range l.m.streams {
				if slices.Contains(s.route, l.peer) {
					streams = append(streams, s)
				}
			}
		}
		l.m.mu.Unlock()
		for _, s := range streams {
			s.finish(ErrUnavailable)
		}
		if current {
			l.m.announce()
			l.m.changed()
		}
	}()
	l.conn.SetReadLimit(8 << 20)
	_ = l.conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	l.conn.SetPongHandler(func(string) error { return l.conn.SetReadDeadline(time.Now().Add(45 * time.Second)) })
	for {
		var value wire
		if err := l.conn.ReadJSON(&value); err != nil {
			return
		}
		if !l.m.Enabled() || !l.m.Authorized(l.peer) {
			return
		}
		if value.Packet != nil {
			l.m.receive(l.peer, *value.Packet)
			continue
		}
		if value.Routes != nil {
			routes := []Route{}
			for _, route := range value.Routes {
				if !route.Node.Valid() || len(route.Path) == 0 || route.Path[0] != l.peer || route.Path[len(route.Path)-1] != route.Node.ID || slices.Contains(route.Path, l.m.Node().ID) {
					continue
				}
				seen := map[string]bool{}
				valid := true
				for _, id := range route.Path {
					if seen[id] {
						valid = false
						break
					}
					seen[id] = true
				}
				if valid {
					routes = append(routes, route)
				}
			}
			l.m.mu.Lock()
			l.routes = routes
			for _, r := range routes {
				p, ok := l.m.peers[r.Node.ID]
				if ok && p.Authorized { // Names/epochs are live; retain the owner-entered direct URL.
					url := p.Node.URL
					p.Node = r.Node
					if url != "" {
						p.Node.URL = url
					}
					l.m.peers[p.Node.ID] = p
				}
			}
			l.m.mu.Unlock()
		}
	}
}

func (m *Manager) routesLocked() map[string]Route {
	routes := map[string]Route{m.node.ID: {Node: m.node, Path: []string{m.node.ID}}}
	for id, l := range m.links {
		for _, r := range l.routes {
			if r.Node.ID == m.node.ID || len(r.Path) == 0 || r.Path[0] != id || slices.Contains(r.Path, m.node.ID) {
				continue
			}
			path := append([]string{m.node.ID}, r.Path...)
			old, ok := routes[r.Node.ID]
			if !ok || len(path) < len(old.Path) || len(path) == len(old.Path) && strings.Join(path, ":") < strings.Join(old.Path, ":") {
				routes[r.Node.ID] = Route{Node: r.Node, Path: path}
			}
		}
	}
	return routes
}

func (m *Manager) announce() {
	m.mu.Lock()
	routes := m.routesLocked()
	links := []*link{}
	for _, l := range m.links {
		links = append(links, l)
	}
	m.mu.Unlock()
	for _, l := range links {
		out := []Route{}
		for _, r := range routes {
			if !slices.Contains(r.Path, l.peer) {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Node.ID < out[j].Node.ID })
		_ = l.send(wire{Routes: out})
	}
}

func (m *Manager) disconnect(id string) {
	m.mu.Lock()
	l := m.links[id]
	streams := []*stream{}
	for _, s := range m.streams {
		if slices.Contains(s.route, id) {
			streams = append(streams, s)
		}
	}
	m.mu.Unlock()
	if l != nil {
		l.close()
	}
	for _, s := range streams {
		s.finish(ErrUnavailable)
	}
}

func (m *Manager) aead(node Node) (cipher.AEAD, error) {
	if !node.Valid() {
		return nil, errors.New("invalid peer identity")
	}
	public, err := ecdh.X25519().NewPublicKey(node.PublicKey)
	if err != nil {
		return nil, err
	}
	shared, err := m.key.ECDH(public)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte("echo-swarm-tunnel-v1\x00"))
	h.Write(shared)
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func associated(p packet) []byte {
	b, _ := json.Marshal([]any{p.From.ID, p.To, p.ID, p.Kind, p.Route})
	return b
}
func (m *Manager) seal(node Node, id, kind string, route []string, data []byte) (packet, error) {
	aead, err := m.aead(node)
	if err != nil {
		return packet{}, err
	}
	p := packet{From: m.Node(), To: node.ID, ID: id, Kind: kind, Route: route, Nonce: make([]byte, aead.NonceSize())}
	if _, err = rand.Read(p.Nonce); err != nil {
		return packet{}, err
	}
	p.Data = aead.Seal(nil, p.Nonce, data, associated(p))
	return p, nil
}
func (m *Manager) transmit(p packet) error {
	self := m.Node().ID
	at := slices.Index(p.Route, self)
	if at < 0 || at+1 >= len(p.Route) {
		return ErrUnavailable
	}
	m.mu.Lock()
	l := m.links[p.Route[at+1]]
	m.mu.Unlock()
	if l == nil {
		return ErrUnavailable
	}
	return l.send(wire{Packet: &p})
}
func (m *Manager) send(node Node, id, kind string, route []string, data []byte) error {
	p, err := m.seal(node, id, kind, route, data)
	if err != nil {
		return err
	}
	return m.transmit(p)
}

func (m *Manager) receive(previous string, p packet) {
	self := m.Node().ID
	if len(p.Route) < 2 || p.Route[0] != p.From.ID || p.Route[len(p.Route)-1] != p.To || !p.From.Valid() || len(p.ID) != 64 || len(p.Data) > frameSize+4096 {
		return
	}
	seen := map[string]bool{}
	for _, id := range p.Route {
		if seen[id] {
			return
		}
		seen[id] = true
	}
	at := slices.Index(p.Route, self)
	if at < 1 || p.Route[at-1] != previous {
		return
	}
	if p.To != self {
		if err := m.transmit(p); err != nil {
			back := append([]string(nil), p.Route[:at+1]...)
			slices.Reverse(back)
			_ = m.send(p.From, p.ID, "fault", back, []byte("A relay is unavailable"))
		}
		return
	}
	aead, err := m.aead(p.From)
	if err != nil || len(p.Nonce) != aead.NonceSize() {
		return
	}
	data, err := aead.Open(nil, p.Nonce, p.Data, associated(p))
	if err != nil {
		return
	}
	if p.Kind == "open" {
		m.accept(p, data)
		return
	}
	m.mu.Lock()
	s := m.streams[p.ID]
	m.mu.Unlock()
	if s == nil {
		return
	}
	if p.Kind == "fault" && slices.Contains(s.route, p.From.ID) {
		s.finish(ErrUnavailable)
		return
	}
	if s.peer.ID != p.From.ID {
		return
	}
	s.receive(p.Kind, data)
}

type opening struct {
	Token   string `json:"token"`
	Pairing bool   `json:"pairing"`
}

func (m *Manager) dial(ctx context.Context, id string, pairing bool) (net.Conn, error) {
	if !m.Enabled() {
		return nil, ErrUnavailable
	}
	m.mu.Lock()
	p := m.peers[id]
	route := m.routesLocked()[id]
	m.mu.Unlock()
	if pairing && route.Node.ID == "" {
		if p.Authorized && p.Node.URL != "" {
			_ = m.connectLink(ctx, id)
			m.mu.Lock()
			route = m.routesLocked()[id]
			m.mu.Unlock()
		}
	}
	if route.Node.ID == "" || len(route.Path) < 2 {
		return nil, ErrUnavailable
	}
	input := opening{Pairing: pairing}
	if !pairing {
		if !p.Authorized || p.Blocked || p.Disabled {
			return nil, ErrUnauthorized
		}
		token, err := m.config.Secrets.Get(ctx, p.Outbound)
		if err != nil {
			return nil, err
		}
		input.Token = token
	}
	s := newStream(m, randomID(), route.Node, route.Path)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, net.ErrClosed
	}
	m.streams[s.id] = s
	m.mu.Unlock()
	data, _ := json.Marshal(input)
	if err := m.send(s.peer, s.id, "open", s.route, data); err != nil {
		s.finish(err)
		return nil, err
	}
	select {
	case err := <-s.opened:
		if err != nil {
			s.finish(err)
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		_ = s.Close()
		return nil, ctx.Err()
	case <-s.done:
		return nil, s.failure()
	}
}

func (m *Manager) accept(p packet, data []byte) {
	var input opening
	if json.Unmarshal(data, &input) != nil {
		return
	}
	route := append([]string(nil), p.Route...)
	slices.Reverse(route)
	if !input.Pairing && !m.authenticate(p.From.ID, input.Token) {
		_ = m.send(p.From, p.ID, "rejected", route, []byte(ErrUnauthorized.Error()))
		return
	}
	if !m.Enabled() {
		_ = m.send(p.From, p.ID, "rejected", route, []byte("Swarm is disabled"))
		return
	}
	s := newStream(m, p.ID, p.From, route)
	// A fresh receiver challenge binds subsequent frames to this acceptance.
	// Replaying a sealed open and old request frames cannot replay an owner API.
	s.challenge = make([]byte, 32)
	if _, err := rand.Read(s.challenge); err != nil {
		return
	}
	m.mu.Lock()
	if m.closed || m.streams[p.ID] != nil {
		m.mu.Unlock()
		return
	}
	count := 0
	for _, active := range m.streams {
		if active.peer.ID == p.From.ID {
			count++
		}
	}
	if count >= 256 {
		m.mu.Unlock()
		_ = m.send(p.From, p.ID, "rejected", route, []byte("Too many active streams"))
		return
	}
	m.streams[p.ID] = s
	m.wg.Add(1)
	m.mu.Unlock()
	if err := m.send(p.From, p.ID, "opened", route, s.challenge); err != nil {
		s.finish(err)
		m.wg.Done()
		return
	}
	go func() {
		defer m.wg.Done()
		defer s.Close()
		principal := Principal{NodeID: p.From.ID, Pairing: input.Pairing}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if input.Pairing && (r.Method != "POST" || r.URL.Path != "/api/v1/swarm/pair") {
				http.Error(w, "Pairing-only connection", 403)
				return
			}
			if !input.Pairing && !m.authenticate(p.From.ID, input.Token) {
				http.Error(w, "Swarm authorization revoked", 401)
				return
			}
			m.config.Handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
		})
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return m.ctx }}
		_ = server.Serve(&singleListener{conn: s, done: s.done})
	}()
}

func (m *Manager) Watch(ctx context.Context, id, path string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return m.dial(ctx, id, false) }}
	conn, response, err := dialer.DialContext(ctx, "ws://swarm"+path, nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("subscribe to remote Echo: %w", err)
	}
	return conn, nil
}
