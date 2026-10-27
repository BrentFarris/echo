package swarm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testRecords struct {
	sync.Mutex
	values map[string]json.RawMessage
}

func (r *testRecords) Get(ns, id string, out any) error {
	r.Lock()
	defer r.Unlock()
	v, ok := r.values[ns+"/"+id]
	if !ok {
		return sql.ErrNoRows
	}
	return json.Unmarshal(v, out)
}
func (r *testRecords) Put(ns, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r.Lock()
	defer r.Unlock()
	r.values[ns+"/"+id] = b
	return nil
}
func (r *testRecords) Delete(ns, id string) error {
	r.Lock()
	defer r.Unlock()
	delete(r.values, ns+"/"+id)
	return nil
}
func (r *testRecords) List(ns string) ([]json.RawMessage, error) {
	r.Lock()
	defer r.Unlock()
	out := []json.RawMessage{}
	for k, v := range r.values {
		if strings.HasPrefix(k, ns+"/") {
			out = append(out, append(json.RawMessage(nil), v...))
		}
	}
	return out, nil
}

type testSecrets struct {
	sync.Mutex
	values map[string]string
}

func (s *testSecrets) Get(_ context.Context, k string) (string, error) {
	s.Lock()
	defer s.Unlock()
	v, ok := s.values[k]
	if !ok {
		return "", sql.ErrNoRows
	}
	return v, nil
}
func (s *testSecrets) Set(_ context.Context, k, v string) error {
	s.Lock()
	defer s.Unlock()
	s.values[k] = v
	return nil
}
func (s *testSecrets) Delete(_ context.Context, k string) error {
	s.Lock()
	defer s.Unlock()
	delete(s.values, k)
	return nil
}

func managerFixture(t *testing.T, name string) (*Manager, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	var enabled atomic.Bool
	m, err := New(context.Background(), Config{Store: &testRecords{values: map[string]json.RawMessage{}}, Secrets: &testSecrets{values: map[string]string{}}, SecretPrefix: name, IdentityFile: filepath.Join(t.TempDir(), "identity"), Name: name, Epoch: randomID(), Handler: mux, Enabled: enabled.Load, Enable: func() error { enabled.Store(true); return nil }, CheckPassword: func(_ context.Context, password, address string) error {
		if password != "owner password" {
			return errors.New("invalid password")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("POST /api/v1/swarm/pair", func(w http.ResponseWriter, r *http.Request) {
		var p PairRequest
		if json.NewDecoder(r.Body).Decode(&p) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		result, err := m.Pair(r.Context(), p, r.RemoteAddr)
		if err != nil {
			http.Error(w, err.Error(), 401)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("GET /api/v1/swarm/link", m.LinkHTTP)
	mux.HandleFunc("/api/v1/echo", func(w http.ResponseWriter, r *http.Request) {
		if !Owner(r.Context()) {
			http.Error(w, "owner required", 401)
			return
		}
		_, _ = io.Copy(w, r.Body)
	})
	h := httptest.NewServer(mux)
	t.Cleanup(h.Close)
	t.Cleanup(m.Close)
	if err = m.Configure(name, h.URL); err != nil {
		t.Fatal(err)
	}
	return m, h
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition did not converge")
}
func echoRequest(m *Manager, other *Manager, body []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := m.RoundTrip(ctx, other.Node().ID, "POST", "/api/v1/echo", bytes.NewReader(body))
	if err != nil {
		return false
	}
	defer r.Body.Close()
	actual, err := io.ReadAll(r.Body)
	return err == nil && r.StatusCode == 200 && bytes.Equal(body, actual)
}

func TestCrossedPairingReconnectsAndRevokesAllGrants(t *testing.T) {
	a, ah := managerFixture(t, "a")
	b, bh := managerFixture(t, "b")
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, operation := range []func() error{func() error {
		_, err := a.Connect(context.Background(), bh.URL, "", "owner password", true)
		return err
	}, func() error {
		_, err := b.Connect(context.Background(), ah.URL, "", "owner password", true)
		return err
	}} {
		go func() { <-start; errs <- operation() }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	body := bytes.Repeat([]byte("bounded duplex stream\n"), 40000)
	eventually(t, func() bool { return echoRequest(a, b, body) && echoRequest(b, a, body) })
	a.disconnect(b.Node().ID)
	eventually(t, func() bool { return echoRequest(a, b, body) && echoRequest(b, a, body) })
	for _, m := range []*Manager{a, b} {
		for _, p := range m.Directory() {
			if p.Inbound != "" || p.Outbound != "" || p.Discovery != "" || len(p.Grants) > 0 || p.InboundHash != "" {
				t.Fatal("public directory exposed grants")
			}
		}
	}
	b.mu.Lock()
	grants := peerGrants(b.peers[a.Node().ID])
	b.mu.Unlock()
	if len(grants) != 2 {
		t.Fatalf("crossed pairings retained %d grants", len(grants))
	}
	if err := b.Revoke(a.Node().ID); err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if _, err := b.config.Secrets.Get(context.Background(), g.Inbound); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("revocation retained an inbound secret")
		}
		if _, err := b.config.Secrets.Get(context.Background(), g.Outbound); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("revocation retained an outbound secret")
		}
	}
	if echoRequest(a, b, []byte("revoked")) {
		t.Fatal("revoked owner request succeeded")
	}
}

func TestRoutingCycleAndDuplicateConnectionsStayDeduplicated(t *testing.T) {
	a, ah := managerFixture(t, "same")
	b, bh := managerFixture(t, "same")
	c, ch := managerFixture(t, "c")
	for _, pair := range []struct {
		from *Manager
		url  string
	}{{a, bh.URL}, {b, ch.URL}, {c, ah.URL}, {a, bh.URL}} {
		if _, err := pair.from.Connect(context.Background(), pair.url, "", "owner password", true); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool {
		return len(a.Directory()) == 2 && len(b.Directory()) == 2 && len(c.Directory()) == 2 && echoRequest(a, c, []byte("cycle")) && echoRequest(c, b, []byte("cycle"))
	})
	if a.Namespace(a.Node().ID) == a.Namespace(b.Node().ID) || !strings.HasPrefix(a.Namespace(a.Node().ID), "same-") {
		t.Fatal("duplicate machine names collided")
	}
	for _, m := range []*Manager{a, b, c} {
		m.mu.Lock()
		for _, route := range m.routesLocked() {
			if len(route.Path) > 2 {
				t.Errorf("did not prefer direct route: %v", route.Path)
			}
		}
		if len(m.links) != 2 {
			t.Errorf("duplicate links: %d", len(m.links))
		}
		m.mu.Unlock()
	}
}

func TestReceiverChallengeRejectsReplayedRequestFrames(t *testing.T) {
	m := &Manager{streams: map[string]*stream{}}
	s := newStream(m, randomID(), Node{}, nil)
	s.challenge = bytes.Repeat([]byte{2}, 32)
	frame := make([]byte, 8)
	binary.BigEndian.PutUint64(frame, 0)
	frame = append(frame, []byte("POST /api/v1/bots HTTP/1.1\r\n\r\n")...)
	s.receive("data", append(bytes.Repeat([]byte{1}, 32), frame...))
	s.receive("close", bytes.Repeat([]byte{1}, 32))
	if len(s.data) != 0 || s.received != 0 {
		t.Fatal("replayed owner request was accepted")
	}
	select {
	case <-s.done:
		t.Fatal("replayed close terminated new stream")
	default:
	}
	s.receive("data", append(bytes.Repeat([]byte{2}, 32), frame...))
	if len(s.data) != 1 || s.received != 1 {
		t.Fatal("fresh request was not accepted")
	}
}
