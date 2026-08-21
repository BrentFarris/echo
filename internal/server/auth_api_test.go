package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newAuthTestServer(t *testing.T) *Server {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<!doctype html><title>Echo</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewWithSettingsPath("127.0.0.1:0", directory, filepath.Join(directory, "echo.json"))
}

func authRequest(t *testing.T, s *Server, method, target string, body any, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	request.Host = "echo.test"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	return response
}

func TestAuthenticationProtectsLegacyAPIsAndSetsSecurityHeaders(t *testing.T) {
	s := newAuthTestServer(t)
	defer s.Shutdown(t.Context())

	unauthorized := authRequest(t, s, http.MethodGet, "/api/settings", nil, nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("legacy API was not protected: %d %s", unauthorized.Code, unauthorized.Body.String())
	}
	if unauthorized.Header().Get("Content-Security-Policy") == "" || unauthorized.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers missing: %v", unauthorized.Header())
	}
	if policy := unauthorized.Header().Get("Permissions-Policy"); !strings.Contains(policy, "microphone=(self)") {
		t.Fatalf("first-party microphone access is not allowed: %q", policy)
	}

	setup := authRequest(t, s, http.MethodPost, "/api/auth/setup", map[string]any{
		"setupCode": s.auth.SetupCode(), "password": "correct horse battery", "deviceName": "Test browser",
	}, nil, "http://echo.test")
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", setup.Code, setup.Body.String())
	}
	cookies := setup.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}
	authorized := authRequest(t, s, http.MethodGet, "/api/settings", nil, cookies[0], "")
	if authorized.Code != http.StatusOK {
		t.Fatalf("authenticated API: %d %s", authorized.Code, authorized.Body.String())
	}
	if len(authorized.Result().Cookies()) == 0 {
		t.Fatal("authenticated request did not refresh the rolling cookie")
	}

	crossOrigin := authRequest(t, s, http.MethodPut, "/api/settings", map[string]any{"settings": map[string]any{}}, cookies[0], "http://evil.test")
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation was not rejected: %d", crossOrigin.Code)
	}
}

func TestLoginRateLimitAndOriginValidation(t *testing.T) {
	s := newAuthTestServer(t)
	defer s.Shutdown(t.Context())
	setup := authRequest(t, s, http.MethodPost, "/api/auth/setup", map[string]any{
		"setupCode": s.auth.SetupCode(), "password": "correct horse battery", "deviceName": "Owner",
	}, nil, "http://echo.test")
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup: %s", setup.Body.String())
	}
	for attempt := 0; attempt < 5; attempt++ {
		response := authRequest(t, s, http.MethodPost, "/api/auth/login", map[string]any{"password": "incorrect password"}, nil, "http://echo.test")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d", attempt+1, response.Code)
		}
	}
	limited := authRequest(t, s, http.MethodPost, "/api/auth/login", map[string]any{"password": "incorrect password"}, nil, "http://echo.test")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("login was not rate limited: %d %v", limited.Code, limited.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "http://echo.test/ws", nil)
	request.Host = "echo.test"
	request.Header.Set("Origin", "http://other.test")
	if requestOriginAllowed(request) {
		t.Fatal("cross-origin WebSocket request was accepted")
	}
	request.Header.Set("Origin", "http://echo.test")
	if !requestOriginAllowed(request) {
		t.Fatal("same-origin WebSocket request was rejected")
	}
	if !strings.Contains(setup.Header().Get("Set-Cookie"), "SameSite=Strict") {
		t.Fatalf("strict cookie attribute missing: %s", setup.Header().Get("Set-Cookie"))
	}
}
