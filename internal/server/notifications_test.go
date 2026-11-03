package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

type notificationTransport func(*http.Request) (*http.Response, error)

func (f notificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebPushEncryptionKeysAndRetryResponses(t *testing.T) {
	s := testServer(t)
	s.Secrets = &swarmTestSecrets{values: map[string]string{}}
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := json.Marshal(map[string]any{"endpoint": "https://push.example.com/token", "keys": map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(make([]byte, 16))}})
	d := store.NotificationDevice{Subscription: sub, Preferences: domain.NotificationPreferences{Completion: true, CompletionSound: true}}
	a := domain.ChatAlert{ID: "alert", Kind: "completion", Title: "Sensitive reply", Preview: "Private preview", OccurredAt: time.Now()}
	statusCode := 429
	s.notificationClient = &http.Client{Transport: notificationTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Sensitive reply") || strings.Contains(string(body), "Private preview") {
			t.Error("push payload was not encrypted")
		}
		if r.Header.Get("Authorization") == "" || r.Header.Get("Content-Encoding") != "aes128gcm" {
			t.Error("missing web push headers", r.Header)
		}
		return &http.Response{StatusCode: statusCode, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader("provider response"))}, nil
	})}
	status, retry, err := s.sendPush(context.Background(), d, a)
	if status != 429 || retry != 120*time.Second || err == nil {
		t.Fatal(status, retry, err)
	}
	first, err := s.pushKeys()
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.pushKeys()
	if err != nil || first != second {
		t.Fatal("VAPID keys changed", err)
	}
	statusCode = 201
	status, _, err = s.sendPush(context.Background(), d, a)
	if err != nil || status != 201 {
		t.Fatal(status, err)
	}
}

func TestNotificationSwarmDeliveryWithoutBrowser(t *testing.T) {
	a, _ := swarmFixture(t, "entry", remotePassword)
	b, bh := swarmFixture(t, "remote", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	cookie := remoteLogin(t, a)
	d := store.NotificationDevice{ID: "phone", Session: cookieKey(cookie)}
	if err := a.Store.SaveNotificationDevice(d); err != nil {
		t.Fatal(err)
	}
	g := attachModel(t, b, func(w http.ResponseWriter, r *http.Request) { stream(w, "Remote task finished", nil) })
	c := chat(t, b, g)
	run, err := b.send(c.ID, SendRequest{Content: "Complete without an open browser", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, run)
	deadline := time.Now().Add(12 * time.Second)
	var pending []domain.ChatAlert
	for time.Now().Before(deadline) {
		pending, _ = a.Store.PendingAlerts(d.ID)
		if len(pending) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(pending) != 1 || pending[0].HomeNodeID != b.Swarm.Node().ID || pending[0].ChatID != c.ID {
		t.Fatal("remote alert never reached entry", pending)
	}
	if err = b.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		pending, _ = a.Store.PendingAlerts(d.ID)
		if len(pending) == 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(pending) != 0 {
		t.Fatal("remote deletion did not cancel delivery", pending)
	}
}

func TestChatAlertKindsAndStableIdentity(t *testing.T) {
	s := testServer(t)
	base := domain.Message{ID: "m", ChatID: "c", BotID: "echo", Author: "Echo", Role: "assistant", Status: "completed", Content: strings.Repeat("hello ", 60), CreatedAt: time.Now()}
	a := s.messageAlert(base)
	if len(a) != 1 || a[0].Kind != "completion" || len([]rune(a[0].Preview)) > 160 {
		t.Fatal(a)
	}
	if next := s.messageAlert(base); next[0].ID != a[0].ID {
		t.Fatal("unstable identity")
	}
	for _, status := range []string{"running", "failed", "cancelled", "interrupted"} {
		m := base
		m.Status = status
		if len(s.messageAlert(m)) != 0 {
			t.Fatal(status)
		}
	}
	m := base
	m.Calls = []domain.ToolCall{{ID: "tool"}}
	if len(s.messageAlert(m)) != 0 {
		t.Fatal("tool round notified")
	}
	m = base
	m.Role = "tool"
	m.Status = "running"
	m.Author = "Echo · ask_user_questions"
	m.Content = `{"questions":[{"id":"q","question":"Which approach?"}]}`
	if a = s.messageAlert(m); len(a) != 1 || a[0].Kind != "question" || a[0].Preview != "Which approach?" {
		t.Fatal(a)
	}
	m.Content = `{"questions":[]}`
	if len(s.messageAlert(m)) != 0 {
		t.Fatal("invalid questions notified")
	}
}

func TestNotificationsIndependentCompletions(t *testing.T) {
	s := testServer(t)
	_, err := s.saveBot(domain.Bot{Handle: "second", Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "Finished independently", nil) })
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "@echo @second complete independently", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	feed, err := s.Store.AlertFeed(0, s.Swarm.Node().ID)
	if err != nil || len(feed) != 2 {
		t.Fatal(feed, err, d.err)
	}
	if feed[0].BotID == feed[1].BotID || feed[0].ID == feed[1].ID {
		t.Fatal("group completions collapsed", feed)
	}
}

func TestNotificationDeviceSessionIsolation(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	w := remoteRequest(s, "POST", "/notifications/devices", `{}`, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var value struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &value)
	second := remoteLogin(t, s)
	w = remoteRequest(s, "GET", "/notifications/devices/"+value.ID+"/pending", "", second)
	if w.Code != 404 {
		t.Fatal("cross-session device access", w.Code)
	}
	d, err := s.Store.NotificationDevice(value.ID, cookieKey(cookie))
	if err != nil || !s.notificationSessionValid(d) {
		t.Fatal(d, err)
	}
	w = remoteRequest(s, "DELETE", "/hosting/sessions/"+cookieKey(cookie), "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if s.notificationSessionValid(d) {
		t.Fatal("revoked session can receive push")
	}
	w = remoteRequest(s, "GET", "/notifications/devices/"+value.ID+"/pending", "", cookie)
	if w.Code != 401 {
		t.Fatal("revoked session can claim", w.Code)
	}
}

func TestNotificationRemoteReplicaHomeAndCancellation(t *testing.T) {
	s := testServer(t)
	c := chat(t, s, domain.Generation{})
	m := domain.Message{ID: "worker:m", ChatID: c.ID, BotID: "worker:b", Author: "Worker", Role: "assistant", Status: "completed", CreatedAt: time.Now()}
	if err := s.Store.MergeReplica(c.ID, "worker:dispatch", "epoch", 1, []domain.Message{m}, nil, s.messageAlert(m)...); err != nil {
		t.Fatal(err)
	}
	feed, _ := s.Store.AlertFeed(0, s.Swarm.Node().ID)
	if len(feed) != 1 || feed[0].HomeNodeID != s.Swarm.Node().ID || feed[0].ChatID != c.ID {
		t.Fatal(feed)
	}
	if err := s.Store.MergeReplica(c.ID, "worker:dispatch", "epoch", 2, []domain.Message{m}, nil, s.messageAlert(m)...); err != nil {
		t.Fatal(err)
	}
	feed, _ = s.Store.AlertFeed(0, s.Swarm.Node().ID)
	if len(feed) != 1 {
		t.Fatal("duplicate reconciliation", feed)
	}
	projection := domain.Chat{ID: "projection", Projection: true}
	_ = s.Store.CreateChat(projection)
	m.ID = "hidden"
	m.ChatID = projection.ID
	_ = s.Store.AddMessage(m)
	_ = s.Store.FinishMessage(m, s.messageAlert(m)...)
	feed, _ = s.Store.AlertFeed(0, s.Swarm.Node().ID)
	if len(feed) != 1 {
		t.Fatal("worker projection notified", feed)
	}
}

func TestPushEndpointValidation(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/push", "https://localhost/push", "https://127.0.0.1/push", "https://10.0.0.1/push", "https://[::1]/push", "https://user:pass@example.com/push", "https://example.com:8443/push", "https://example.com/#fragment"} {
		if _, err := validatePushEndpoint(endpoint); err == nil {
			t.Fatal(endpoint)
		}
	}
	if _, err := validatePushEndpoint("https://push.example.com/opaque-token"); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "fc00::1", "::ffff:192.168.1.1"} {
		if publicPushIP(net.ParseIP(ip)) {
			t.Fatal(ip)
		}
	}
	if pushHTTPClient().CheckRedirect(&http.Request{}, nil) == nil {
		t.Fatal("redirects accepted")
	}
}

func TestPendingNotificationsFilterDeletedChat(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	c := chat(t, s, domain.Generation{})
	d := store.NotificationDevice{ID: "d", Session: cookieKey(cookie)}
	_ = s.Store.SaveNotificationDevice(d)
	a := domain.ChatAlert{ID: "a", HomeNodeID: s.Swarm.Node().ID, ChatID: c.ID, Kind: "completion", Active: true, OccurredAt: time.Now()}
	_ = s.Store.PutAlert(a)
	_ = s.Store.DeleteChat(c.ID)
	w := remoteRequest(s, "POST", "/notifications/devices/d/claim", `{"alert":"a"}`, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"claimed":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
