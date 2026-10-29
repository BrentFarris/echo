package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/development"
	"golang.org/x/crypto/bcrypt"
)

func TestDevelopmentRemoteAuthenticationAndPersistence(t *testing.T) {
	s := testServer(t)
	handler := s.Handler()
	hash, _ := bcrypt.GenerateFromPassword([]byte("remote-owner-password"), bcrypt.MinCost)
	if err := s.Store.Put("auth", "password", string(hash)); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://echo.example"+path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.10:54321"
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/development", "/api/v1/development/update-status", "/api/v1/health", "/api/v1/development/operations/unknown"} {
		if w := request("GET", path, "", nil, ""); w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	if w := request("POST", "/api/v1/development/terminate", "{}", nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	login := request("POST", "/api/v1/session", `{"password":"remote-owner-password"}`, nil, "http://echo.example")
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	w := request("PUT", "/api/v1/development", `{"repositoryPath":"","autoCheckUpdates":true}`, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var preference development.Config
	if err := s.Store.Get("settings", "development", &preference); err != nil || !preference.AutoCheckUpdates {
		t.Fatal(preference, err)
	}
	w = request("GET", "/api/v1/development/update-status", "", cookie, "")
	var notice development.UpdateNotice
	if err := json.Unmarshal(w.Body.Bytes(), &notice); err != nil || !notice.AutoCheckUpdates || notice.UpdateAvailable {
		t.Fatal(w.Body.String(), err)
	}
	if w := request("POST", "/api/v1/development/terminate", "{}", cookie, "http://foreign.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("PUT", "/api/v1/development", `{"repositoryPath":"relative"}`, cookie, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(development.Config{RepositoryPath: root})
	w = request("PUT", "/api/v1/development", string(data), cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var stored development.Config
	if err := s.Store.Get("settings", "development", &stored); err != nil || stored.RepositoryPath != root {
		t.Fatal(stored, err)
	}
	var state development.Snapshot
	w = request("GET", "/api/v1/development", "", cookie, "")
	if err = json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state.RepositoryValid || state.InstanceID == "" {
		t.Fatal(w.Body.String(), err)
	}
	w = request("GET", "/api/v1/health", "", cookie, "")
	if !strings.Contains(w.Body.String(), state.InstanceID) {
		t.Fatal(w.Body.String())
	}
	w = request("POST", "/api/v1/development/terminate", "{}", cookie, "")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("POST", "/api/v1/development/relaunch", "{}", cookie, "")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("PUT", "/api/v1/development", `{"repositoryPath":""}`, cookie, "")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-s.ShutdownRequested():
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown was not requested")
	}
}
