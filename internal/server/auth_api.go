package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/auth"
)

const sessionCookieName = "echo_session"

type authContextKey struct{}

type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{attempts: make(map[string][]time.Time)}
}

func (l *loginRateLimiter) Allow(address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	recent := l.attempts[address][:0]
	for _, attempt := range l.attempts[address] {
		if attempt.After(cutoff) {
			recent = append(recent, attempt)
		}
	}
	if len(recent) >= 5 {
		l.attempts[address] = recent
		return false
	}
	l.attempts[address] = append(recent, now)
	return true
}

func (s *Server) requireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authDisabled || isPublicRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !requestOriginAllowed(r) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || s.auth == nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		session, ok, err := s.auth.Authenticate(cookie.Value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authentication is unavailable")
			return
		}
		if !ok {
			clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		// Refresh the browser cookie alongside the persisted rolling expiry. The
		// token itself is unchanged and only its SHA-256 digest is stored.
		setSessionCookie(w, r, cookie.Value)
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isPublicRequest(r *http.Request) bool {
	if r.URL.Path == "/api/health" {
		return true
	}
	switch r.URL.Path {
	case "/api/auth/status", "/api/auth/setup", "/api/auth/login":
		return true
	}
	return !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/ws"
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/plugin-ui/") {
			assets := pluginAssetCSPSource(r)
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src "+assets+"; style-src "+assets+" 'unsafe-inline'; img-src "+assets+" data:; media-src "+assets+" data: blob:; font-src "+assets+" data:; connect-src 'none'; worker-src 'none'; child-src 'none'; frame-src 'none'; object-src 'none'; manifest-src 'none'; base-uri 'none'; form-action 'none'; navigate-to 'none'; frame-ancestors 'self'")
			// A sandboxed iframe without allow-same-origin has an opaque `null`
			// origin. Module scripts consequently require CORS even though their
			// tokenized URLs are served by Echo. The session token remains the
			// capability boundary and credentials are never permitted here.
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' data: blob:; connect-src 'self' ws: wss:; font-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

func pluginAssetCSPSource(r *http.Request) string {
	remainder := strings.TrimPrefix(r.URL.Path, "/plugin-ui/")
	token, _, _ := strings.Cut(remainder, "/")
	path := "/plugin-ui/" + url.PathEscape(token) + "/"
	httpSource := (&url.URL{Scheme: "http", Host: r.Host, Path: path}).String()
	httpsSource := (&url.URL{Scheme: "https", Host: r.Host, Path: path}).String()
	return httpSource + " " + httpsSource
}

func requestOriginAllowed(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	setupRequired := true
	if s.auth != nil {
		setupRequired, _ = s.auth.SetupRequired()
	}
	authenticated := false
	var session auth.Session
	if cookie, err := r.Cookie(sessionCookieName); err == nil && s.auth != nil {
		session, authenticated, _ = s.auth.Authenticate(cookie.Value)
	}
	writeData(w, http.StatusOK, map[string]any{
		"setupRequired":   setupRequired,
		"authenticated":   authenticated,
		"session":         session,
		"transportSecure": r.TLS != nil,
	})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "authentication is unavailable")
		return
	}
	if !requestOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	var body struct {
		SetupCode string `json:"setupCode"`
		Password  string `json:"password"`
		Device    string `json:"deviceName"`
	}
	if err := decodeLimitedJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, session, err := s.auth.Setup(body.SetupCode, body.Password, requestDevice(r, body.Device))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrAlreadyConfigured) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	setSessionCookie(w, r, token)
	writeData(w, http.StatusCreated, map[string]any{"session": session})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "authentication is unavailable")
		return
	}
	if !requestOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	ip := remoteIP(r)
	if !s.loginLimiter.Allow(ip) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts; try again shortly")
		return
	}
	var body struct {
		Password string `json:"password"`
		Device   string `json:"deviceName"`
	}
	if err := decodeLimitedJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, session, err := s.auth.Login(body.Password, requestDevice(r, body.Device))
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrSetupRequired) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	setSessionCookie(w, r, token)
	writeData(w, http.StatusOK, map[string]any{"session": session})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.auth.Logout(cookie.Value)
	}
	clearSessionCookie(w, r)
	writeData(w, http.StatusOK, map[string]any{"loggedOut": true})
}

func (s *Server) handleAuthSessions(w http.ResponseWriter, r *http.Request) {
	token := currentToken(r)
	sessions, err := s.auth.Sessions(token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load sessions")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) handleAuthRevokeSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.auth.Revoke(id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if current, ok := r.Context().Value(authContextKey{}).(auth.Session); ok && current.ID == id {
		clearSessionCookie(w, r)
	}
	writeData(w, http.StatusOK, map[string]any{"revoked": id})
}

func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if err := decodeLimitedJSON(w, r, &body, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.auth.ChangePassword(body.Current, body.New, currentToken(r)); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, auth.ErrInvalidCredentials) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err.Error())
		return
	}
	writeData(w, http.StatusOK, map[string]any{"changed": true})
}

func decodeLimitedJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request body: " + err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid request body: expected one JSON value")
	}
	return nil
}

func requestDevice(r *http.Request, name string) auth.DeviceInfo {
	return auth.DeviceInfo{Name: name, UserAgent: r.UserAgent(), RemoteIP: remoteIP(r)}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
		MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: -1,
	})
}

func currentToken(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}
