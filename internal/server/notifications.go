package server

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/brent/echo/internal/credentials"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

func boundedPreview(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return string(r)
}

func (s *Server) messageAlert(m domain.Message) []domain.ChatAlert {
	kind, preview, title := "", m.Content, m.Author+" finished"
	if m.Role == "assistant" && m.Status == "completed" && len(m.Calls) == 0 {
		kind = "completion"
	}
	if m.Role == "tool" && m.Status == "running" && strings.HasSuffix(m.Author, " · "+AskUserQuestionsToolName) {
		args, err := ParseAskUserQuestionsArgs(json.RawMessage(m.Content))
		if err != nil {
			return nil
		}
		kind = "question"
		preview = args.Questions[0].Question
		title = strings.TrimSuffix(m.Author, " · "+AskUserQuestionsToolName) + " has a question"
	}
	if kind == "" || s.Swarm == nil {
		return nil
	}
	home := s.Swarm.Node().ID
	return []domain.ChatAlert{{ID: home + ":" + kind + ":" + m.ID, Kind: kind, HomeNodeID: home, ChatID: m.ChatID, BotID: m.BotID, MessageID: m.ID, OccurredAt: domain.Now(), Title: boundedPreview(title, 100), Preview: boundedPreview(preview, 160), Active: true}}
}

func publicPushIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}
func validatePushEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return nil, errors.New("push endpoint must be a public HTTPS URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !publicPushIP(ip) {
		return nil, errors.New("private push endpoint is not allowed")
	}
	if strings.EqualFold(u.Hostname(), "localhost") || strings.HasSuffix(strings.ToLower(u.Hostname()), ".localhost") {
		return nil, errors.New("private push endpoint is not allowed")
	}
	return u, nil
}

// Resolve and pin public IPs at connection time, preventing DNS rebinding and
// private-network requests. No environment proxy or redirect can bypass this.
func pushHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("push redirects are not allowed") }, Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if port != "443" {
			return nil, errors.New("invalid push port")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("push host has no address")
		}
		for _, ip := range ips {
			if !publicPushIP(ip.IP) {
				return nil, errors.New("private push address is not allowed")
			}
		}
		var conn net.Conn
		for _, ip := range ips {
			conn, err = (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, err
	}}}
}

type notificationKeys struct {
	Public  string `json:"public"`
	Private string `json:"private"`
}

func (s *Server) pushKeys() (notificationKeys, error) {
	s.notificationMu.Lock()
	defer s.notificationMu.Unlock()
	key := "Echo2/" + s.Dir + "/notifications/vapid"
	value, err := s.Secrets.Get(s.ctx, key)
	var keys notificationKeys
	if err == nil {
		err = json.Unmarshal([]byte(value), &keys)
		return keys, err
	}
	if !errors.Is(err, credentials.ErrSecretNotFound) {
		return keys, err
	}
	keys.Private, keys.Public, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return keys, err
	}
	data, _ := json.Marshal(keys)
	err = s.Secrets.Set(s.ctx, key, string(data))
	return keys, err
}

func (s *Server) notificationRoutes(mux *http.ServeMux) {
	// Only this journal feed is available to authorized peers. Device settings
	// and subscriptions always belong to the browser's entry origin/session.
	mux.HandleFunc("GET /api/v1/notifications/feed", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		alerts, err := s.Store.AlertFeed(after, s.Swarm.Node().ID)
		respond(w, alerts, err)
	})
	local := func(w http.ResponseWriter, r *http.Request) bool {
		if _, ok := swarm.Caller(r.Context()); ok {
			http.Error(w, "Device notifications are local to the entry instance", 403)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/v1/notifications/config", func(w http.ResponseWriter, r *http.Request) {
		if !local(w, r) {
			return
		}
		keys, err := s.pushKeys()
		if err != nil {
			respond(w, map[string]any{"available": false, "error": "Push credentials could not be opened. In-app alerts remain available."}, nil)
			return
		}
		respond(w, map[string]any{"available": true, "publicKey": keys.Public}, nil)
	})
	mux.HandleFunc("POST /api/v1/notifications/devices", func(w http.ResponseWriter, r *http.Request) {
		if !local(w, r) {
			return
		}
		var input struct {
			ID           string                          `json:"id"`
			Subscription json.RawMessage                 `json:"subscription"`
			Preferences  *domain.NotificationPreferences `json:"preferences"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		d := store.NotificationDevice{ID: input.ID, Session: sessionKey(r), Preferences: domain.NotificationPreferences{Completion: true, Questions: true, CompletionSound: true, QuestionSound: true}}
		if input.ID != "" {
			if prior, err := s.Store.NotificationDevice(input.ID, d.Session); err == nil {
				d = prior
			} else {
				d.ID = ""
			}
		}
		if d.ID == "" {
			d.ID = domain.ID()
		}
		if input.Preferences != nil {
			d.Preferences = *input.Preferences
		}
		if input.Subscription != nil {
			if len(input.Subscription) > 8192 {
				respond(w, nil, errors.New("push subscription is too large"))
				return
			}
			if string(input.Subscription) == "null" {
				d.Subscription = nil
			} else {
				var sub webpush.Subscription
				if err := json.Unmarshal(input.Subscription, &sub); err != nil {
					respond(w, nil, errors.New("invalid push subscription"))
					return
				}
				if _, err := validatePushEndpoint(sub.Endpoint); err != nil {
					respond(w, nil, err)
					return
				}
				key, e := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
				if e != nil {
					respond(w, nil, errors.New("invalid push key"))
					return
				}
				if _, e = ecdh.P256().NewPublicKey(key); e != nil {
					respond(w, nil, errors.New("invalid push key"))
					return
				}
				auth, e := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
				if e != nil || len(auth) != 16 {
					respond(w, nil, errors.New("invalid push authentication key"))
					return
				}
				d.Subscription = input.Subscription
			}
		}
		respond(w, map[string]any{"id": d.ID, "preferences": d.Preferences, "push": len(d.Subscription) > 0}, s.Store.SaveNotificationDevice(d))
	})
	device := func(w http.ResponseWriter, r *http.Request) (store.NotificationDevice, bool) {
		if !local(w, r) {
			return store.NotificationDevice{}, false
		}
		d, err := s.Store.NotificationDevice(r.PathValue("id"), sessionKey(r))
		if err != nil {
			http.Error(w, "Notification device not found", 404)
			return d, false
		}
		return d, true
	}
	mux.HandleFunc("DELETE /api/v1/notifications/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := device(w, r)
		if !ok {
			return
		}
		respond(w, map[string]bool{"ok": true}, s.Store.DeleteNotificationDevice(d.ID, d.Session))
	})
	mux.HandleFunc("GET /api/v1/notifications/devices/{id}/pending", func(w http.ResponseWriter, r *http.Request) {
		d, ok := device(w, r)
		if !ok {
			return
		}
		alerts, err := s.Store.PendingAlerts(d.ID)
		visible := []domain.ChatAlert{}
		for _, a := range alerts {
			if s.notificationAuthorized(a) {
				visible = append(visible, a)
			}
		}
		respond(w, visible, err)
	})
	mux.HandleFunc("POST /api/v1/notifications/devices/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		d, ok := device(w, r)
		if !ok {
			return
		}
		var input struct {
			Alert string `json:"alert"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		a, err := s.Store.NotificationAlert(input.Alert)
		if err != nil || !s.notificationAuthorized(a) {
			respond(w, map[string]bool{"claimed": false}, nil)
			return
		}
		claimed, err := s.Store.ClaimAlert(d.ID, input.Alert, "live", false)
		respond(w, map[string]bool{"claimed": claimed}, err)
	})
	mux.HandleFunc("POST /api/v1/notifications/devices/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		d, ok := device(w, r)
		if !ok {
			return
		}
		var input struct {
			Kind string `json:"kind"`
		}
		if err := decode(w, r, &input); err != nil {
			respond(w, nil, err)
			return
		}
		if input.Kind != "question" {
			input.Kind = "completion"
		}
		a := domain.ChatAlert{ID: "test:" + domain.ID(), Kind: input.Kind, HomeNodeID: s.Swarm.Node().ID, OccurredAt: domain.Now(), Title: "Echo notification test", Preview: "Notifications are working on this device.", Active: true}
		// Tests go directly to this device's push service, including while focused.
		if len(d.Subscription) == 0 {
			respond(w, nil, errors.New("enable notifications on this device first"))
			return
		}
		_, _, err := s.sendPush(r.Context(), d, a)
		respond(w, map[string]bool{"sent": err == nil}, err)
	})
}

func (s *Server) notificationAuthorized(a domain.ChatAlert) bool {
	if a.HomeNodeID == s.Swarm.Node().ID {
		_, err := s.Store.Chat(a.ChatID)
		return a.ChatID == "" || err == nil
	}
	if !s.Swarm.Enabled() {
		return false
	}
	for _, p := range s.Swarm.Directory() {
		if p.Node.ID == a.HomeNodeID {
			return p.Authorized && !p.Blocked && !p.Disabled
		}
	}
	return false
}

func notificationURL(a domain.ChatAlert) string {
	if a.ChatID == "" {
		return "/"
	}
	qualify := func(id string) string {
		if id == "" {
			return ""
		}
		if len(id) > 33 && id[32] == ':' {
			return id
		}
		return a.HomeNodeID + ":" + id
	}
	q := url.Values{}
	q.Set("bot", qualify(a.BotID))
	q.Set("message", qualify(a.MessageID))
	return "/#" + qualify(a.ChatID) + "?" + q.Encode()
}

func (s *Server) sendPush(ctx context.Context, d store.NotificationDevice, a domain.ChatAlert) (int, time.Duration, error) {
	var sub webpush.Subscription
	if err := json.Unmarshal(d.Subscription, &sub); err != nil {
		return 0, 0, err
	}
	if _, err := validatePushEndpoint(sub.Endpoint); err != nil {
		return 0, 0, err
	}
	keys, err := s.pushKeys()
	if err != nil {
		return 0, 0, err
	}
	sound := d.Preferences.CompletionSound
	if a.Kind == "question" {
		sound = d.Preferences.QuestionSound
	}
	data, _ := json.Marshal(map[string]any{"alert": a, "url": notificationURL(a), "silent": !sound})
	hash := sha256.Sum256([]byte(a.ID))
	ttl := int(time.Until(a.OccurredAt.Add(24 * time.Hour)).Seconds())
	if ttl < 0 {
		ttl = 0
	}
	client := s.notificationClient
	if client == nil {
		client = pushHTTPClient()
	}
	response, err := webpush.SendNotificationWithContext(ctx, data, &sub, &webpush.Options{HTTPClient: client, Subscriber: "https://github.com/brent/echo", VAPIDPublicKey: keys.Public, VAPIDPrivateKey: keys.Private, TTL: ttl, Topic: hex.EncodeToString(hash[:16])})
	if err != nil {
		return 0, 0, errors.New("push service could not be reached")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	delay := time.Duration(0)
	if v := response.Header.Get("Retry-After"); v != "" {
		if n, e := strconv.Atoi(v); e == nil {
			delay = time.Duration(n) * time.Second
		} else if at, e := http.ParseTime(v); e == nil {
			delay = time.Until(at)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, delay, fmt.Errorf("push service returned HTTP %d", response.StatusCode)
	}
	return response.StatusCode, delay, nil
}

func (s *Server) notificationSessionValid(d store.NotificationDevice) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	var data json.RawMessage
	if s.Store.Get("sessions", d.Session, &data) != nil {
		return false
	}
	session, err := readDeviceSession(d.Session, data)
	return err == nil && session.ExpiresAt.After(time.Now())
}
func (s *Server) deliverPush(d store.NotificationDevice, a domain.ChatAlert, attempt int) {
	current, err := s.Store.NotificationDevice(d.ID, d.Session)
	if err != nil {
		return
	}
	d = current
	latest, err := s.Store.NotificationAlert(a.ID)
	if err != nil || !latest.Active || time.Since(a.OccurredAt) > 24*time.Hour || len(d.Subscription) == 0 || !d.Preferences.Popup(a.Kind) {
		_ = s.Store.FinishPush(d.ID, a.ID, time.Time{})
		return
	}
	if !s.notificationSessionValid(d) || !s.notificationAuthorized(a) {
		return
	}
	status, retry, err := s.sendPush(s.ctx, d, a)
	if status == 404 || status == 410 {
		_ = s.Store.ClearNotificationSubscription(d)
		_ = s.Store.FinishPush(d.ID, a.ID, time.Time{})
		return
	}
	if err != nil && (status == 0 || status == 429 || status >= 500) && attempt < 8 {
		backoff := time.Duration(1<<min(attempt, 8)) * time.Second
		if retry < backoff {
			retry = backoff
		}
		if retry > 24*time.Hour {
			retry = 24 * time.Hour
		}
		_ = s.Store.FinishPush(d.ID, a.ID, time.Now().Add(retry))
		return
	}
	_ = s.Store.FinishPush(d.ID, a.ID, time.Time{})
}

func (s *Server) startNotifications() {
	s.notificationWG.Add(2)
	go func() {
		defer s.notificationWG.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		var cursor int64
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
			}
			alerts, err := s.Store.AlertFeed(cursor, "")
			if err == nil {
				for _, a := range alerts {
					cursor = a.Sequence
					s.emit("notification", a.ChatID, "", a.BotID, a)
				}
			}
			devices, err := s.Store.NotificationDevices()
			if err != nil {
				continue
			}
			var wg sync.WaitGroup
			slots := make(chan struct{}, 4)
			for _, device := range devices {
				if !s.notificationSessionValid(device) {
					_ = s.Store.DeleteNotificationDevice(device.ID, device.Session)
					continue
				}
				if len(device.Subscription) == 0 {
					continue
				}
				select {
				case slots <- struct{}{}:
				case <-s.ctx.Done():
					wg.Wait()
					return
				}
				wg.Add(1)
				go func(d store.NotificationDevice) {
					defer wg.Done()
					defer func() { <-slots }()
					pending, _ := s.Store.PendingAlerts(d.ID, true)
					for _, a := range pending {
						if !s.notificationAuthorized(a) {
							_, _ = s.Store.ClaimAlert(d.ID, a.ID, "cancelled", true)
							continue
						}
						ok, _ := s.Store.ClaimAlert(d.ID, a.ID, "push", true)
						if ok {
							s.deliverPush(d, a, 1)
						}
					}
					retries, _ := s.Store.RetryPush(d.ID)
					for _, r := range retries {
						if !d.Preferences.Popup(r.Alert.Kind) || !s.notificationAuthorized(r.Alert) {
							continue
						}
						_ = s.Store.BeginPushRetry(d.ID, r.Alert.ID)
						s.deliverPush(d, r.Alert, r.Attempts+1)
					}
				}(device)
			}
			wg.Wait()
			_ = s.Store.PruneNotifications()
		}
	}()
	go func() {
		defer s.notificationWG.Done()
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
			}
			if !s.Swarm.Enabled() {
				continue
			}
			for _, p := range s.Swarm.Directory() {
				if !p.Authorized || p.Blocked || p.Disabled {
					continue
				}
				var cursor int64
				_ = s.Store.Get("notification-cursors", p.Node.ID, &cursor)
				ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
				var alerts []domain.ChatAlert
				err := s.Swarm.JSON(ctx, p.Node.ID, "GET", "/api/v1/notifications/feed?after="+strconv.FormatInt(cursor, 10), nil, &alerts)
				cancel()
				if err != nil {
					continue
				}
				for _, a := range alerts {
					if a.HomeNodeID != p.Node.ID {
						continue
					}
					if err = s.Store.PutAlert(a); err != nil {
						break
					}
					cursor = a.Sequence
				}
				_ = s.Store.Put("notification-cursors", p.Node.ID, cursor)
			}
		}
	}()
}
