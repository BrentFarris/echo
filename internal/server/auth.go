package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

const sessionLifetime = 30 * 24 * time.Hour

type deviceSession struct {
	ID        string    `json:"id"`
	UserAgent string    `json:"userAgent"`
	Address   string    `json:"address"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
	ExpiresAt time.Time `json:"expiresAt"`
	Current   bool      `json:"current,omitempty"`
	Connected bool      `json:"connected,omitempty"`
}

func sessionKey(r *http.Request) string {
	c, err := r.Cookie("echo2-session")
	if err != nil || c.Value == "" {
		return ""
	}
	h := sha256.Sum256([]byte(c.Value))
	return hex.EncodeToString(h[:])
}

func readDeviceSession(id string, data json.RawMessage) (deviceSession, error) {
	var session deviceSession
	err := json.Unmarshal(data, &session)
	if err != nil {
		// Earlier releases stored only an expiration timestamp. Keep those
		// cookies usable and visible in the session list until they expire.
		err = json.Unmarshal(data, &session.ExpiresAt)
	}
	session.ID = id
	return session, err
}

func requestAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// All session reads/updates and revocations share authMu so a concurrent
// activity update cannot recreate a revoked session.
func (s *Server) savedSessionLocked(r *http.Request) (deviceSession, bool) {
	id := sessionKey(r)
	var data json.RawMessage
	if id == "" || s.Store.Get("sessions", id, &data) != nil {
		return deviceSession{}, false
	}
	session, err := readDeviceSession(id, data)
	return session, err == nil && session.ExpiresAt.After(time.Now())
}

func (s *Server) authenticated(r *http.Request) bool {
	if caller, ok := swarm.Caller(r.Context()); ok {
		return !caller.Pairing && s.Swarm != nil && s.Swarm.Enabled() && s.Swarm.Authorized(caller.NodeID)
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	session, ok := s.savedSessionLocked(r)
	if ok && time.Since(session.LastSeen) >= time.Minute {
		session.LastSeen = time.Now().UTC()
		session.Address = requestAddress(r)
		session.UserAgent = r.UserAgent()
		_ = s.Store.Put("sessions", session.ID, session)
	}
	return ok
}

func sessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: "echo2-session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds()), Expires: expires})
}

func (s *Server) newSessionLocked(w http.ResponseWriter, r *http.Request) {
	token := domain.ID() + domain.ID()
	h := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	session := deviceSession{ID: hex.EncodeToString(h[:]), UserAgent: r.UserAgent(), Address: requestAddress(r), CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(sessionLifetime)}
	if err := s.Store.Put("sessions", session.ID, session); err != nil {
		respond(w, nil, err)
		return
	}
	sessionCookie(w, r, token, session.ExpiresAt)
	respond(w, map[string]any{"authenticated": true}, nil)
}

func (s *Server) closeSessionSocketsLocked(id string) {
	for conn := range s.sessionSockets[id] {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "Session closed"), time.Now().Add(time.Second))
		_ = conn.Close()
	}
	delete(s.sessionSockets, id)
}

func (s *Server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			http.Error(w, "Sign in required", http.StatusUnauthorized)
			return
		}
		respond(w, map[string]any{"authenticated": true}, nil)
	})
	mux.HandleFunc("POST /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		s.authMu.Lock()
		defer s.authMu.Unlock()
		// Startup must honor the persistent cookie before checking a password
		// or counting a failed login. Reuse one session per browser profile.
		if session, ok := s.savedSessionLocked(r); ok {
			session.LastSeen = time.Now().UTC()
			session.ExpiresAt = session.LastSeen.Add(sessionLifetime)
			session.Address, session.UserAgent = requestAddress(r), r.UserAgent()
			if err := s.Store.Put("sessions", session.ID, session); err != nil {
				respond(w, nil, err)
				return
			}
			cookie, _ := r.Cookie("echo2-session")
			sessionCookie(w, r, cookie.Value, session.ExpiresAt)
			respond(w, map[string]any{"authenticated": true}, nil)
			return
		}
		host := requestAddress(r)
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && localHost(r.Host) {
			s.newSessionLocked(w, r)
			return
		}
		var a struct {
			Password string `json:"password"`
		}
		if err := decode(w, r, &a); err != nil {
			respond(w, nil, err)
			return
		}
		if a.Password == "" {
			http.Error(w, "Sign in required", http.StatusUnauthorized)
			return
		}
		now := time.Now()
		recent := []time.Time{}
		for _, at := range s.loginAttempts[host] {
			if now.Sub(at) < time.Minute {
				recent = append(recent, at)
			}
		}
		if len(recent) >= 5 {
			http.Error(w, "Too many login attempts", http.StatusTooManyRequests)
			return
		}
		var hash string
		if s.Store.Get("auth", "password", &hash) != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(a.Password)) != nil {
			s.loginAttempts[host] = append(recent, now)
			http.Error(w, "Invalid password", http.StatusUnauthorized)
			return
		}
		delete(s.loginAttempts, host)
		s.newSessionLocked(w, r)
	})
	mux.HandleFunc("GET /api/v1/hosting/sessions", func(w http.ResponseWriter, r *http.Request) {
		s.authMu.Lock()
		defer s.authMu.Unlock()
		entries, err := s.Store.Entries("sessions")
		if err != nil {
			respond(w, nil, err)
			return
		}
		sessions := []deviceSession{}
		for id, data := range entries {
			session, err := readDeviceSession(id, data)
			if err != nil || !session.ExpiresAt.After(time.Now()) {
				_ = s.Store.Delete("sessions", id)
				s.closeSessionSocketsLocked(id)
				continue
			}
			session.Current = id == sessionKey(r)
			session.Connected = len(s.sessionSockets[id]) > 0
			sessions = append(sessions, session)
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastSeen.After(sessions[j].LastSeen) })
		respond(w, sessions, nil)
	})
	mux.HandleFunc("DELETE /api/v1/hosting/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.authMu.Lock()
		defer s.authMu.Unlock()
		id := r.PathValue("id")
		if err := s.Store.Delete("sessions", id); err != nil {
			respond(w, nil, err)
			return
		}
		s.closeSessionSocketsLocked(id)
		if id == sessionKey(r) {
			http.SetCookie(w, &http.Cookie{Name: "echo2-session", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		}
		respond(w, map[string]any{"ok": true}, nil)
	})
	mux.HandleFunc("POST /api/v1/hosting/password", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Password string `json:"password"`
		}
		if err := decode(w, r, &a); err != nil {
			respond(w, nil, err)
			return
		}
		if len(a.Password) < 12 || len(a.Password) > 72 {
			respond(w, nil, errors.New("use a password of 12-72 bytes"))
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(a.Password), bcrypt.DefaultCost)
		if err != nil {
			respond(w, nil, err)
			return
		}
		s.authMu.Lock()
		defer s.authMu.Unlock()
		// Recheck after hashing: a different request may have revoked us.
		if _, ok := s.savedSessionLocked(r); !ok && !swarm.Owner(r.Context()) {
			http.Error(w, "Sign in required", http.StatusUnauthorized)
			return
		}
		err = s.Store.Put("auth", "password", string(hash))
		if err == nil {
			err = s.Store.ClearNamespace("sessions")
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		if s.Swarm != nil {
			if err := s.Swarm.RevokeAll(); err != nil {
				respond(w, nil, err)
				return
			}
		}
		if swarm.Owner(r.Context()) {
			respond(w, map[string]bool{"changed": true}, nil)
		} else {
			s.newSessionLocked(w, r)
		}
		for id := range s.sessionSockets {
			s.closeSessionSocketsLocked(id)
		}
	})
}
