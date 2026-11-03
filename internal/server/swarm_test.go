package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/coordinator"
	"github.com/brent/echo/internal/credentials"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/swarm"
	"golang.org/x/crypto/bcrypt"
)

type swarmTestSecrets struct {
	sync.Mutex
	values map[string]string
}

func TestSwarmThreeMachineDispatchAndRemoteEcho(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	c, ch := swarmFixture(t, "mac", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	connectSwarm(t, a, ch, remotePassword)
	for _, host := range []*Server{a, b, c} {
		_, err := host.saveBot(domain.Bot{Handle: "kaiju", Name: "Kaiju"})
		if err != nil {
			t.Fatal(err)
		}
	}
	ga := attachModel(t, a, func(w http.ResponseWriter, r *http.Request) { stream(w, "desktop built", nil) })
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) { stream(w, "linux built", nil) })
	attachModel(t, c, func(w http.ResponseWriter, r *http.Request) { stream(w, "mac built", nil) })
	chat := chat(t, a, ga)
	d, err := a.send(chat.ID, SendRequest{Content: "@kaiju @linux.kaiju @mac.kaiju build", Generation: ga, RequestID: "three-hosts"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	messages, err := a.messages(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"desktop built", "linux built", "mac built"} {
		if !slices.ContainsFunc(messages, func(m domain.Message) bool { return m.Content == want }) {
			t.Fatalf("missing %q: %+v", want, messages)
		}
	}
	before := len(messages)
	duplicate, err := a.send(chat.ID, SendRequest{Content: "@kaiju @linux.kaiju @mac.kaiju build", Generation: ga, RequestID: "three-hosts"})
	if err != nil || duplicate.id != d.id {
		t.Fatal("retry", err)
	}
	messages, _ = a.messages(chat.ID)
	if len(messages) != before {
		t.Fatal("retry repeated execution")
	}
	// Remote-only dispatch has no dependency on a local model connection.
	entry, _ := swarmFixture(t, "entry", remotePassword)
	connectSwarm(t, entry, bh, remotePassword)
	remoteChat := chatForSwarm(t, entry)
	d, err = entry.send(remoteChat.ID, SendRequest{Content: "@linux.echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
}

func chatForSwarm(t *testing.T, s *Server) domain.Chat {
	t.Helper()
	return chat(t, s, domain.Generation{})
}

func TestSwarmBidirectionalHandoffs(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	_, _ = a.saveBot(domain.Bot{Handle: "kaiju", Name: "Desktop"})
	_, _ = b.saveBot(domain.Bot{Handle: "kaiju", Name: "Linux"})
	var mu sync.Mutex
	seen := map[string]bool{}
	handler := func(machine, target string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Messages []model.Message `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			mu.Lock()
			first := !seen[machine]
			seen[machine] = true
			mu.Unlock()
			if first {
				stream(w, "", []domain.ToolCall{{ID: "send", Type: "function", Function: domain.FunctionCall{Name: "bot_send", Arguments: fmt.Sprintf(`{"to":%q,"content":"status please","requestId":%q}`, target, machine+"-question")}}})
				return
			}
			stream(w, machine+" finished", nil)
		}
	}
	ga := attachModel(t, a, handler("desktop", "linux.kaiju"))
	attachModel(t, b, handler("linux", "desktop.kaiju"))
	c := chat(t, a, ga)
	d, err := a.send(c.ID, SendRequest{Content: "@kaiju @linux.kaiju coordinate", Generation: ga})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	entries, err := a.Store.List("swarm-handoffs:" + d.id)
	if err != nil || len(entries) != 2 {
		t.Fatal("expected two cross-machine handoffs", len(entries), err)
	}
	for _, raw := range entries {
		var request coordinator.Request
		_ = json.Unmarshal(raw, &request)
		if !strings.Contains(request.From, ":") || !strings.Contains(request.To, ":") {
			t.Fatal("handoff lost machine identity", request)
		}
	}
}

func (s *swarmTestSecrets) Available(context.Context) bool { return true }
func (s *swarmTestSecrets) Get(_ context.Context, key string) (string, error) {
	s.Lock()
	defer s.Unlock()
	v, ok := s.values[key]
	if !ok {
		return "", credentials.ErrSecretNotFound
	}
	return v, nil
}
func (s *swarmTestSecrets) Set(_ context.Context, key, value string) error {
	s.Lock()
	defer s.Unlock()
	s.values[key] = value
	return nil
}
func (s *swarmTestSecrets) Delete(_ context.Context, key string) error {
	s.Lock()
	defer s.Unlock()
	delete(s.values, key)
	return nil
}

func swarmFixture(t *testing.T, name, password string) (*Server, *httptest.Server) {
	t.Helper()
	s := testServer(t)
	s.Secrets = &swarmTestSecrets{values: map[string]string{}}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Put("auth", "password", string(hash)); err != nil {
		t.Fatal(err)
	}
	if err = s.Swarm.Configure(name, ""); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(s.Handler())
	t.Cleanup(host.Close)
	return s, host
}
func connectSwarm(t *testing.T, a *Server, b *httptest.Server, password string) swarm.Node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	node, err := a.Swarm.Connect(ctx, b.URL, "", password, true)
	if err != nil {
		t.Fatal(err)
	}
	return node
}
func eventuallySwarm(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("Swarm state did not converge")
}

func TestSwarmPairingRequiresPasswordOnLoopback(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.Swarm.Connect(ctx, bh.URL, "", "wrong-password", true); err == nil {
		t.Fatal("wrong password accepted on loopback")
	}
	if b.Swarm.Enabled() {
		t.Fatal("failed pairing enabled remote Swarm")
	}
	node := connectSwarm(t, a, bh, remotePassword)
	if node.ID != b.Swarm.Node().ID || !b.Swarm.Enabled() {
		t.Fatal("pairing did not enable remote")
	}
	var state struct {
		Bots []domain.Bot `json:"bots"`
	}
	if err := a.Swarm.JSON(ctx, node.ID, "GET", "/api/v1/state", nil, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Bots) != 1 || state.Bots[0].ID != "echo" {
		t.Fatal(state)
	}
	if err := b.Swarm.JSON(ctx, a.Swarm.Node().ID, "GET", "/api/v1/state", nil, &state); err != nil {
		t.Fatal("return path", err)
	}
	if err := a.Swarm.JSON(ctx, node.ID, "GET", "/api/v1/swarm/state", nil, &state); err == nil {
		t.Fatal("peer exported aggregated state")
	}
	if err := a.Swarm.JSON(ctx, node.ID, "POST", "/api/v1/session", nil, &state); err == nil {
		t.Fatal("peer minted a browser session")
	}
}

func TestSwarmRelayDiscoveryAndPasswordBoundary(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	c, ch := swarmFixture(t, "mac", remotePassword)
	d, dh := swarmFixture(t, "other", "another-password")
	connectSwarm(t, a, bh, remotePassword)
	connectSwarm(t, b, ch, remotePassword)
	connectSwarm(t, b, dh, "another-password")
	eventuallySwarm(t, func() bool { return a.Swarm.Authorized(c.Swarm.Node().ID) })
	if a.Swarm.Authorized(d.Swarm.Node().ID) {
		t.Fatal("different password transitively authorized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var state map[string]any
	if err := a.Swarm.JSON(ctx, c.Swarm.Node().ID, "GET", "/api/v1/state", nil, &state); err != nil {
		t.Fatal("relay", err)
	}
	if err := a.Swarm.JSON(ctx, d.Swarm.Node().ID, "GET", "/api/v1/state", nil, &state); err == nil {
		t.Fatal("unauthorized target accessed through B")
	}
	if _, err := a.Swarm.Connect(ctx, "", d.Swarm.Node().ID, "another-password", true); err != nil {
		t.Fatal("manual relay pairing", err)
	}
	if err := a.Swarm.JSON(ctx, d.Swarm.Node().ID, "GET", "/api/v1/state", nil, &state); err != nil {
		t.Fatal(err)
	}
	if err := a.Swarm.Revoke(c.Swarm.Node().ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Swarm.JSON(ctx, c.Swarm.Node().ID, "GET", "/api/v1/state", nil, &state); err == nil {
		t.Fatal("revoked target remained usable")
	}
}

func TestSwarmStreamAndWebSocket(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := a.Swarm.Watch(ctx, b.Swarm.Node().ID, "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var event domain.Event
	if err = conn.ReadJSON(&event); err != nil || event.Type != "reset" {
		t.Fatal(event, err)
	}
	b.emit("changed", "", "", "", map[string]string{"value": "remote"})
	if err = conn.ReadJSON(&event); err != nil || event.Type != "changed" {
		t.Fatal(event, err)
	}
	large := strings.Repeat("streamed body ", 50000)
	bot, err := b.saveBot(domain.Bot{Handle: "large", Prompt: large})
	if err != nil {
		t.Fatal(err)
	}
	response, err := a.Swarm.RoundTrip(ctx, b.Swarm.Node().ID, "GET", "/api/v1/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), bot.ID) || !strings.Contains(string(data), large) {
		t.Fatal("streamed response was truncated", len(data))
	}
	var valid any
	if json.Unmarshal(data, &valid) != nil {
		t.Fatal("invalid streamed JSON")
	}
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
}
