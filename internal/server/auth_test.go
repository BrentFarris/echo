package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

const remotePassword = "remote-owner-password"

func remoteRequest(s *Server, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://echo.example/api/v1"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:54321"
	r.Header.Set("Origin", "https://echo.example")
	r.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0) Chrome/130.0")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func remoteLogin(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	w := remoteRequest(s, "POST", "/session", `{"password":"`+remotePassword+`"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	return cookies[0]
}

func setRemotePassword(t *testing.T, s *Server) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(remotePassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("auth", "password", string(hash)); err != nil {
		t.Fatal(err)
	}
}

func cookieKey(cookie *http.Cookie) string {
	r := httptest.NewRequest("GET", "http://echo.example", nil)
	r.AddCookie(cookie)
	return sessionKey(r)
}

func TestRemoteSessionSurvivesRestartAndReusesCookie(t *testing.T) {
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != int(sessionLifetime.Seconds()) || !cookie.Expires.After(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("cookie is not persistent/protected: %+v", cookie)
	}
	s.Close()
	s, err = New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		w := remoteRequest(s, "POST", "/session", `{}`, cookie)
		if w.Code != http.StatusOK || w.Result().Cookies()[0].Value != cookie.Value {
			t.Fatalf("resume %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	rows, _ := s.Store.List("sessions")
	if len(rows) != 1 {
		t.Fatalf("launches created duplicate sessions: %d", len(rows))
	}
	if strings.Contains(string(rows[0]), cookie.Value) || strings.Contains(string(rows[0]), remotePassword) {
		t.Fatal("session record contains credentials")
	}
	w := remoteRequest(s, "GET", "/hosting/sessions", "", cookie)
	var sessions []deviceSession
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || !sessions[0].Current || sessions[0].Address != "192.0.2.10" || sessions[0].UserAgent == "" || sessions[0].CreatedAt.IsZero() {
		t.Fatalf("metadata: %+v", sessions)
	}
	if w := remoteRequest(s, "GET", "/state", "", &http.Cookie{Name: cookie.Name, Value: sessions[0].ID}); w.Code != 401 {
		t.Fatal("public session ID authenticates requests")
	}
}

func TestRemoteSessionLegacyExpiryAndFailedLogins(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	cookie := &http.Cookie{Name: "echo2-session", Value: "previous-version-session-token"}
	id := cookieKey(cookie)
	if err := s.Store.Put("sessions", id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A legacy cookie must both resume and appear in the device list.
	w := remoteRequest(s, "POST", "/session", `{}`, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved deviceSession
	if err := s.Store.Get("sessions", id, &saved); err != nil || saved.LastSeen.IsZero() {
		t.Fatal("legacy session was not upgraded", err)
	}
	saved.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.Store.Put("sessions", id, saved); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if w := remoteRequest(s, "POST", "/session", `{}`, cookie); w.Code != 401 {
			t.Fatalf("expired startup probe: %d", w.Code)
		}
	}
	for i := 0; i < 5; i++ {
		if w := remoteRequest(s, "POST", "/session", `{"password":"wrong"}`, nil); w.Code != 401 {
			t.Fatalf("failed attempt: %d", w.Code)
		}
	}
	if w := remoteRequest(s, "POST", "/session", `{"password":"wrong"}`, nil); w.Code != 429 {
		t.Fatal("missing login throttle", w.Code)
	}
	rows, _ := s.Store.List("sessions")
	if len(rows) != 1 {
		t.Fatal("failed login created a session")
	}
}

func TestRemoteSessionRevocationAndPasswordChange(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	owner, device := remoteLogin(t, s), remoteLogin(t, s)
	for _, method := range []string{"GET", "DELETE"} {
		path := "/hosting/sessions"
		if method == "DELETE" {
			path += "/" + cookieKey(device)
		}
		if w := remoteRequest(s, method, path, "", nil); w.Code != 401 {
			t.Fatal("unauthorized session access", w.Code)
		}
	}
	// Include an untouched older session, so it can also be listed/revoked.
	legacy := &http.Cookie{Name: owner.Name, Value: "legacy-device"}
	if err := s.Store.Put("sessions", cookieKey(legacy), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	w := remoteRequest(s, "GET", "/hosting/sessions", "", owner)
	var sessions []deviceSession
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil || len(sessions) != 3 {
		t.Fatal(w.Body.String(), err)
	}
	for _, cookie := range []*http.Cookie{device, legacy} {
		if w := remoteRequest(s, "DELETE", "/hosting/sessions/"+cookieKey(cookie), "", owner); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if w := remoteRequest(s, "GET", "/state", "", cookie); w.Code != 401 {
			t.Fatal("revoked session still authorized", w.Code)
		}
		if w := remoteRequest(s, "POST", "/session", `{}`, cookie); w.Code != 401 {
			t.Fatal("revoked session resumed", w.Code)
		}
	}
	device = remoteLogin(t, s)
	w = remoteRequest(s, "POST", "/hosting/password", `{"password":"replacement-password"}`, owner)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, cookie := range []*http.Cookie{owner, device} {
		if w := remoteRequest(s, "GET", "/session", "", cookie); w.Code != 401 {
			t.Fatal("old cookie survived password change")
		}
	}
	replacement := w.Result().Cookies()[0]
	if w := remoteRequest(s, "POST", "/session", `{}`, replacement); w.Code != 200 {
		t.Fatal("requesting browser lost access")
	}
	w = remoteRequest(s, "DELETE", "/hosting/sessions/"+cookieKey(replacement), "", replacement)
	if w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("closing current session did not clear cookie")
	}
}

func TestRemoteSessionRevocationClosesLiveSockets(t *testing.T) {
	for _, passwordChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "close-session", true: "change-password"}[passwordChange], func(t *testing.T) {
			s := testServer(t)
			setRemotePassword(t, s)
			owner, device := remoteLogin(t, s), remoteLogin(t, s)
			host := httptest.NewServer(s.Handler())
			defer host.Close()
			header := http.Header{"Origin": []string{host.URL}, "Cookie": []string{device.Name + "=" + device.Value}}
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(host.URL, "http")+"/api/v1/events", header)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			w := remoteRequest(s, "GET", "/hosting/sessions", "", owner)
			var sessions []deviceSession
			if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
				t.Fatal(err)
			}
			connected := false
			for _, session := range sessions {
				if session.ID == cookieKey(device) {
					connected = session.Connected
				}
			}
			if !connected {
				t.Fatal("live device not marked connected")
			}
			if passwordChange {
				w = remoteRequest(s, "POST", "/hosting/password", `{"password":"replacement-password"}`, owner)
			} else {
				w = remoteRequest(s, "DELETE", "/hosting/sessions/"+cookieKey(device), "", owner)
			}
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, 4001) {
				t.Fatalf("socket was not revoked immediately: %v", err)
			}
		})
	}
}
