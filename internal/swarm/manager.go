package swarm

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	config        Config
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	pairMu        sync.Mutex
	node          Node
	key           *ecdh.PrivateKey
	peers         map[string]Peer
	links         map[string]*link
	dialing       map[string]bool
	streams       map[string]*stream
	discovering   map[string]bool
	discoverAfter map[string]time.Time
	closed        bool
	wg            sync.WaitGroup
}

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func tokenHash(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

func New(parent context.Context, config Config) (*Manager, error) {
	ctx, cancel := context.WithCancel(parent)
	m := &Manager{config: config, ctx: ctx, cancel: cancel, peers: map[string]Peer{}, links: map[string]*link{}, dialing: map[string]bool{}, streams: map[string]*stream{}, discovering: map[string]bool{}, discoverAfter: map[string]time.Time{}}
	var identity struct {
		Node Node `json:"node"`
	}
	stored := config.Store.Get("swarm", "identity", &identity)
	if stored != nil && !errors.Is(stored, sql.ErrNoRows) {
		cancel()
		return nil, stored
	}
	var data []byte
	info, err := os.Lstat(config.IdentityFile)
	if err == nil {
		if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			cancel()
			return nil, errors.New("Swarm identity must be an owner-only regular file")
		}
		data, err = os.ReadFile(config.IdentityFile)
	}
	if errors.Is(err, os.ErrNotExist) && errors.Is(stored, sql.ErrNoRows) {
		key, keyErr := ecdh.X25519().GenerateKey(rand.Reader)
		if keyErr != nil {
			cancel()
			return nil, keyErr
		}
		file, createErr := os.OpenFile(config.IdentityFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			cancel()
			return nil, createErr
		}
		data = key.Bytes()
		_, err = file.Write(data)
		if err == nil {
			err = file.Sync()
		}
		file.Close()
	}
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open Swarm identity: %w", err)
	}
	m.key, err = ecdh.X25519().NewPrivateKey(data)
	if err != nil {
		cancel()
		return nil, err
	}
	if stored == nil {
		m.node = identity.Node
		if !m.node.Valid() || !slices.Equal(m.node.PublicKey, m.key.PublicKey().Bytes()) {
			cancel()
			return nil, errors.New("Swarm identity does not match its private key")
		}
	} else {
		h := sha256.Sum256(m.key.PublicKey().Bytes())
		m.node = Node{ID: hex.EncodeToString(h[:16]), Name: HostName(config.Name), PublicKey: m.key.PublicKey().Bytes(), Version: Version}
		identity.Node = m.node
		if err = config.Store.Put("swarm", "identity", identity); err != nil {
			cancel()
			return nil, err
		}
	}
	m.node.Version, m.node.Epoch, m.node.Capabilities = Version, config.Epoch, capabilities()
	entries, err := config.Store.List("swarm-peers")
	if err != nil {
		cancel()
		return nil, err
	}
	for _, raw := range entries {
		var p Peer
		if err := json.Unmarshal(raw, &p); err != nil {
			cancel()
			return nil, err
		}
		if p.Node.Valid() {
			p.Connected = false
			m.peers[p.Node.ID] = p
		}
	}
	m.wg.Add(1)
	go m.maintain()
	return m, nil
}

func (m *Manager) Node() Node {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.node
	n.PublicKey = append([]byte(nil), n.PublicKey...)
	return n
}
func (m *Manager) Enabled() bool { return m.config.Enabled != nil && m.config.Enabled() }

// RouteToken identifies the current authorized route without dialing. A new
// connection, peer epoch, or grant invalidates work started on an older route.
func (m *Manager) RouteToken(id string) string {
	if !m.Enabled() {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.peers[id]
	r := m.routesLocked()[id]
	if !p.Authorized || p.Blocked || p.Disabled || len(r.Path) < 2 {
		return ""
	}
	l := m.links[r.Path[1]]
	if l == nil {
		return ""
	}
	select {
	case <-l.done:
		return ""
	default:
	}
	return p.PairID + ":" + r.Node.Epoch + ":" + l.id + ":" + strings.Join(r.Path, ":")
}
func (m *Manager) changed() {
	if m.config.Changed != nil {
		m.config.Changed()
	}
}

func (m *Manager) Configure(name, advertised string) error {
	if !ValidName(name) {
		return errors.New("machine name must start with a letter and contain up to 48 lowercase letters, digits, underscores or hyphens")
	}
	if advertised != "" {
		var err error
		advertised, err = normalizeURL(advertised)
		if err != nil {
			return err
		}
	}
	m.pairMu.Lock()
	defer m.pairMu.Unlock()
	n := m.Node()
	n.Name, n.URL = name, advertised
	if err := m.config.Store.Put("swarm", "identity", map[string]any{"node": n}); err != nil {
		return err
	}
	m.mu.Lock()
	m.node = n
	m.mu.Unlock()
	m.announce()
	m.changed()
	return nil
}

func (m *Manager) Directory() []Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	routes := m.routesLocked()
	all := map[string]Peer{}
	for id, p := range m.peers {
		p.Connected = !p.Disabled && routes[id].Node.ID != ""
		p.Outbound, p.Inbound, p.InboundHash, p.Discovery, p.PairID = "", "", "", "", ""
		p.Grants = nil
		all[id] = p
	}
	for id, r := range routes {
		if id == m.node.ID {
			continue
		}
		if _, ok := all[id]; !ok {
			all[id] = Peer{Node: r.Node, Connected: true}
		}
	}
	out := []Peer{}
	for _, p := range all {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Node.Name < out[j].Node.Name || out[i].Node.Name == out[j].Node.Name && out[i].Node.ID < out[j].Node.ID
	})
	return out
}

// RouteDepth is used to replace relay destinations before their intermediaries.
func (m *Manager) RouteDepth(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.routesLocked()[id].Path)
}

func (m *Manager) Namespace(id string) string {
	nodes := []Node{m.Node()}
	for _, p := range m.Directory() {
		if p.Authorized && !p.Blocked {
			nodes = append(nodes, p.Node)
		}
	}
	name := ""
	counts := map[string]int{}
	for _, n := range nodes {
		counts[n.Name]++
		if n.ID == id {
			name = n.Name
		}
	}
	if name != "" && counts[name] > 1 {
		name += "-" + id[:8]
	}
	return name
}

func normalizeURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", errors.New("use an Echo HTTP(S) origin without credentials, path, query or fragment")
	}
	return u.Scheme + "://" + u.Host, nil
}

func (m *Manager) savePeer(p Peer) error {
	if err := m.config.Store.Put("swarm-peers", p.Node.ID, p); err != nil {
		return err
	}
	m.mu.Lock()
	m.peers[p.Node.ID] = p
	m.mu.Unlock()
	return nil
}

func peerGrants(p Peer) []Grant {
	if len(p.Grants) > 0 {
		return append([]Grant(nil), p.Grants...)
	}
	if p.InboundHash != "" {
		return []Grant{{PairID: p.PairID, Inbound: p.Inbound, Outbound: p.Outbound, Hash: p.InboundHash}}
	}
	return nil
}

// Concurrent reciprocal pairings can both succeed. Keep a small rolling set
// of grants so a lost response or crossed handshake cannot invalidate the
// other direction's accepted token. Owner revocation invalidates every grant.
func (m *Manager) savePair(p Peer) error {
	m.mu.Lock()
	previous := m.peers[p.Node.ID]
	m.mu.Unlock()
	grants := []Grant{}
	if previous.Authorized && !previous.Blocked {
		grants = peerGrants(previous)
	}
	grants = append(grants, Grant{PairID: p.PairID, Inbound: p.Inbound, Outbound: p.Outbound, Hash: p.InboundHash})
	removed := []Grant{}
	if len(grants) > 8 {
		removed = grants[:len(grants)-8]
		grants = grants[len(grants)-8:]
	}
	p.Grants = grants
	if p.Node.URL == "" {
		p.Node.URL = previous.Node.URL
	}
	if err := m.savePeer(p); err != nil {
		return err
	}
	for _, g := range removed {
		_ = m.config.Secrets.Delete(m.ctx, g.Inbound)
		_ = m.config.Secrets.Delete(m.ctx, g.Outbound)
	}
	return nil
}

// Pair is the only password entry point. It never uses browser cookies or the
// loopback bootstrap exemption. Automatic discovery cannot enable a plugin.
func (m *Manager) Pair(ctx context.Context, input PairRequest, address string) (PairResponse, error) {
	if input.Version != Version || !compatible(input.Node) {
		return PairResponse{}, errors.New("incompatible Swarm protocol; update both Echo instances")
	}
	if !input.Node.Valid() || input.Node.ID == m.Node().ID || len(input.ID) != 64 || len(input.ReturnToken) != 64 {
		return PairResponse{}, errors.New("invalid Swarm pairing identity")
	}
	if err := m.config.CheckPassword(ctx, input.Password, address); err != nil {
		return PairResponse{}, err
	}
	m.pairMu.Lock()
	defer m.pairMu.Unlock()
	m.mu.Lock()
	p := m.peers[input.Node.ID]
	m.mu.Unlock()
	if (!m.Enabled() || p.Blocked || p.Disabled) && !input.Activate {
		return PairResponse{}, errors.New("Swarm is disabled or this peer was revoked; connect explicitly to enable it")
	}
	if input.Activate && !m.Enabled() {
		if err := m.config.Enable(); err != nil {
			return PairResponse{}, err
		}
	}
	if p.Authorized && !p.Blocked && !p.Disabled {
		for _, grant := range peerGrants(p) {
			if grant.PairID == input.ID {
				token, err := m.config.Secrets.Get(ctx, grant.Inbound)
				return PairResponse{Node: m.Node(), Token: token}, err
			}
		}
	}
	token := randomID()
	prefix := m.config.SecretPrefix + "/peers/" + input.Node.ID
	p = Peer{Node: input.Node, Authorized: true, PairID: input.ID, Inbound: prefix + "/" + input.ID + "/in", Outbound: prefix + "/" + input.ID + "/out", InboundHash: tokenHash(token), Discovery: prefix + "/discovery", LastSeen: time.Now().UTC()}
	for key, value := range map[string]string{p.Inbound: token, p.Outbound: input.ReturnToken, p.Discovery: input.Password} {
		if err := m.config.Secrets.Set(ctx, key, value); err != nil {
			return PairResponse{}, err
		}
	}
	if err := m.savePair(p); err != nil {
		return PairResponse{}, err
	}
	m.changed()
	return PairResponse{Node: m.Node(), Token: token}, nil
}

func (m *Manager) Connect(ctx context.Context, endpoint, nodeID, password string, activate bool) (Node, error) {
	if password == "" {
		return Node{}, errors.New("enter the remote owner's password")
	}
	var err error
	if endpoint != "" {
		endpoint, err = normalizeURL(endpoint)
		if err != nil {
			return Node{}, err
		}
	}
	if endpoint == "" && nodeID == "" {
		return Node{}, errors.New("enter a remote Echo address")
	}
	if activate && !m.Enabled() {
		if err := m.config.Enable(); err != nil {
			return Node{}, err
		}
	}
	if !m.Enabled() {
		return Node{}, errors.New("Swarm is disabled")
	}
	input := PairRequest{Version: Version, ID: randomID(), Node: m.Node(), Password: password, ReturnToken: randomID(), Activate: activate}
	raw, _ := json.Marshal(input)
	var client *http.Client
	if nodeID != "" {
		client = m.client(nodeID, true)
		endpoint = "http://swarm"
	} else {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect}
	}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint+"/api/v1/swarm/pair", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return Node{}, fmt.Errorf("connect to Echo: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == 404 {
			return Node{}, errors.New("remote Echo does not support Swarm; update it to a compatible version")
		}
		data, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return Node{}, fmt.Errorf("pairing failed: %s", strings.TrimSpace(string(data)))
	}
	var result PairResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return Node{}, err
	}
	if !result.Node.Valid() || result.Node.ID == input.Node.ID || !compatible(result.Node) || len(result.Token) != 64 || nodeID != "" && nodeID != result.Node.ID {
		return Node{}, errors.New("remote Echo returned an invalid or incompatible identity")
	}
	if nodeID == "" {
		result.Node.URL = endpoint
	}
	m.pairMu.Lock()
	prefix := m.config.SecretPrefix + "/peers/" + result.Node.ID
	p := Peer{Node: result.Node, Authorized: true, PairID: input.ID, Inbound: prefix + "/" + input.ID + "/in", Outbound: prefix + "/" + input.ID + "/out", InboundHash: tokenHash(input.ReturnToken), Discovery: prefix + "/discovery", LastSeen: time.Now().UTC()}
	for key, value := range map[string]string{p.Inbound: input.ReturnToken, p.Outbound: result.Token, p.Discovery: password} {
		if err = m.config.Secrets.Set(ctx, key, value); err != nil {
			break
		}
	}
	if err == nil {
		err = m.savePair(p)
	}
	m.pairMu.Unlock()
	if err != nil {
		return Node{}, err
	}
	if result.Node.URL != "" {
		_ = m.connectLink(ctx, result.Node.ID)
	}
	m.announce()
	m.changed()
	return result.Node, nil
}

func noRedirect(*http.Request, []*http.Request) error {
	return errors.New("Echo connections do not follow redirects")
}

func (m *Manager) Authorized(node string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.peers[node]
	return p.Authorized && !p.Blocked && !p.Disabled
}

func (m *Manager) authenticate(node, token string) bool {
	if !m.Enabled() || token == "" {
		return false
	}
	m.mu.Lock()
	p := m.peers[node]
	m.mu.Unlock()
	if !p.Authorized || p.Blocked || p.Disabled {
		return false
	}
	match := 0
	hash := tokenHash(token)
	for _, grant := range peerGrants(p) {
		match |= subtle.ConstantTimeCompare([]byte(hash), []byte(grant.Hash))
	}
	return match == 1
}

// Disconnect retains credentials, but stops both direct and relayed access.
// Automatic discovery and incoming reconnects cannot undo this owner choice.
func (m *Manager) SetDisconnected(id string, disabled bool) error {
	m.pairMu.Lock()
	m.mu.Lock()
	p, ok := m.peers[id]
	m.mu.Unlock()
	if !ok || !p.Authorized || p.Blocked {
		m.pairMu.Unlock()
		return ErrUnauthorized
	}
	p.Disabled = disabled
	err := m.savePeer(p)
	m.pairMu.Unlock()
	if err != nil {
		return err
	}
	if disabled {
		m.disconnect(id)
	}
	m.changed()
	return nil
}

func (m *Manager) Revoke(id string) error {
	m.pairMu.Lock()
	defer m.pairMu.Unlock()
	m.mu.Lock()
	p, ok := m.peers[id]
	m.mu.Unlock()
	if !ok {
		return errors.New("unknown Swarm peer")
	}
	grants := peerGrants(p)
	p.Authorized, p.Blocked, p.Connected = false, true, false
	p.InboundHash = ""
	p.Grants = nil
	if err := m.savePeer(p); err != nil {
		return err
	}
	for _, key := range []string{p.Inbound, p.Outbound, p.Discovery} {
		if key != "" {
			_ = m.config.Secrets.Delete(m.ctx, key)
		}
	}
	for _, g := range grants {
		_ = m.config.Secrets.Delete(m.ctx, g.Inbound)
		_ = m.config.Secrets.Delete(m.ctx, g.Outbound)
	}
	m.disconnect(id)
	m.changed()
	return nil
}

func (m *Manager) RevokeAll() error {
	for _, peer := range m.Directory() {
		if peer.Authorized {
			if err := m.Revoke(peer.Node.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) Refresh() {
	if !m.Enabled() {
		m.mu.Lock()
		ids := []string{}
		for id := range m.links {
			ids = append(ids, id)
		}
		m.mu.Unlock()
		for _, id := range ids {
			m.disconnect(id)
		}
	}
	m.changed()
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	links := []*link{}
	for _, l := range m.links {
		links = append(links, l)
	}
	streams := []*stream{}
	for _, s := range m.streams {
		streams = append(streams, s)
	}
	m.mu.Unlock()
	for _, l := range links {
		l.close()
	}
	for _, s := range streams {
		s.finish(net.ErrClosed)
	}
	m.wg.Wait()
}

func (m *Manager) maintain() {
	defer m.wg.Done()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
		if !m.Enabled() {
			continue
		}
		for _, p := range m.Directory() {
			if !p.Authorized || p.Blocked || p.Disabled || p.Node.URL == "" {
				continue
			}
			m.mu.Lock()
			needed := m.links[p.Node.ID] == nil && !m.dialing[p.Node.ID]
			if needed {
				m.dialing[p.Node.ID] = true
			}
			m.mu.Unlock()
			if needed {
				m.wg.Add(1)
				go func(id string) {
					defer m.wg.Done()
					ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
					defer cancel()
					_ = m.connectLink(ctx, id)
					m.mu.Lock()
					delete(m.dialing, id)
					m.mu.Unlock()
				}(p.Node.ID)
			}
		}
		m.announce()
		m.discover()
	}
}

func (m *Manager) discover() {
	m.mu.Lock()
	routes := m.routesLocked()
	candidates := []struct {
		route Route
		key   string
	}{}
	for id, route := range routes {
		p := m.peers[id]
		if id == m.node.ID || p.Authorized || p.Blocked || len(route.Path) < 3 || m.discovering[id] || time.Now().Before(m.discoverAfter[id]) || len(m.discovering) >= 8 {
			continue
		}
		via := m.peers[route.Path[1]]
		if via.Discovery != "" {
			m.discovering[id] = true
			candidates = append(candidates, struct {
				route Route
				key   string
			}{route, via.Discovery})
		}
	}
	m.mu.Unlock()
	for _, candidate := range candidates {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer func() { m.mu.Lock(); delete(m.discovering, candidate.route.Node.ID); m.mu.Unlock() }()
			password, err := m.config.Secrets.Get(m.ctx, candidate.key)
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
			_, err = m.Connect(ctx, "", candidate.route.Node.ID, password, false)
			cancel()
			if err != nil {
				// A failed discovery is informational, not a durable revocation.
				m.mu.Lock()
				m.discoverAfter[candidate.route.Node.ID] = time.Now().Add(time.Minute)
				p := m.peers[candidate.route.Node.ID]
				if !p.Authorized && !p.Blocked {
					p.Node = candidate.route.Node
					p.Error = "Authentication required"
					m.peers[p.Node.ID] = p
				}
				m.mu.Unlock()
			}
		}()
	}
}

func (m *Manager) Client(id string) *http.Client { return m.client(id, false) }
func (m *Manager) client(id string, pairing bool) *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 130 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return m.dial(ctx, id, pairing) }}
	return &http.Client{Transport: transport, CheckRedirect: noRedirect}
}

func (m *Manager) RoundTrip(ctx context.Context, id, method, path string, body io.Reader) (*http.Response, error) {
	if !strings.HasPrefix(path, "/api/v1/") {
		return nil, errors.New("invalid Echo API path")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://swarm"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return m.Client(id).Do(req)
}

func (m *Manager) JSON(ctx context.Context, id, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(data))
	}
	response, err := m.RoundTrip(ctx, id, method, path, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return fmt.Errorf("%s", strings.TrimSpace(string(data)))
	}
	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(output)
}
