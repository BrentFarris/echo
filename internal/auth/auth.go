// Package auth implements Echo's single-owner password and remembered-device
// authentication. Raw session tokens are returned only to the caller and are
// never persisted; echo.json stores their SHA-256 digests.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/appdata"
	"golang.org/x/crypto/argon2"
)

const (
	minimumPasswordRunes = 12
	maximumPasswordRunes = 128
	sessionLifetime      = 30 * 24 * time.Hour
	sessionRefreshAfter  = time.Hour
)

var (
	ErrSetupRequired      = errors.New("authentication setup is required")
	ErrAlreadyConfigured  = errors.New("authentication is already configured")
	ErrInvalidSetupCode   = errors.New("the setup code is invalid or expired")
	ErrInvalidCredentials = errors.New("invalid password")
	ErrInvalidSession     = errors.New("invalid session")
)

type passwordRecord struct {
	Algorithm   string `json:"algorithm"`
	Salt        string `json:"salt"`
	Hash        string `json:"hash"`
	MemoryKiB   uint32 `json:"memoryKiB"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
	KeyLength   uint32 `json:"keyLength"`
}

type sessionRecord struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"tokenHash"`
	Device    string    `json:"device"`
	UserAgent string    `json:"userAgent,omitempty"`
	RemoteIP  string    `json:"remoteIp,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type persistedState struct {
	Password *passwordRecord `json:"password,omitempty"`
	Sessions []sessionRecord `json:"sessions,omitempty"`
}

// Session is safe to return to an authenticated browser. It deliberately
// excludes the stored token digest.
type Session struct {
	ID        string    `json:"id"`
	Device    string    `json:"device"`
	UserAgent string    `json:"userAgent,omitempty"`
	RemoteIP  string    `json:"remoteIp,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed"`
	ExpiresAt time.Time `json:"expiresAt"`
	Current   bool      `json:"current,omitempty"`
}

// DeviceInfo captures human-readable metadata for a remembered session.
type DeviceInfo struct {
	Name      string
	UserAgent string
	RemoteIP  string
}

// Manager coordinates auth operations with the shared application-data
// transaction boundary.
type Manager struct {
	data      *appdata.Store
	mu        sync.Mutex
	setupCode string
	now       func() time.Time
}

// New creates an authentication manager. If no password exists, a one-time
// setup code is generated and kept only in memory.
func New(data *appdata.Store) (*Manager, error) {
	m := &Manager{data: data, now: time.Now}
	state, err := m.load()
	if err != nil {
		return nil, err
	}
	if state.Password == nil {
		m.setupCode, err = randomToken(24)
		if err != nil {
			return nil, fmt.Errorf("generate setup code: %w", err)
		}
	}
	return m, nil
}

// SetupCode returns the current memory-only first-run code. It is empty after
// setup succeeds.
func (m *Manager) SetupCode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setupCode
}

// Reset removes the password and sessions and creates a fresh setup code.
func (m *Manager) Reset() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.save(persistedState{}); err != nil {
		return "", err
	}
	code, err := randomToken(24)
	if err != nil {
		return "", err
	}
	m.setupCode = code
	return code, nil
}

// SetupRequired reports whether Echo still needs its owner password.
func (m *Manager) SetupRequired() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.load()
	return state.Password == nil, err
}

// Setup consumes the one-time code, sets the initial password, and returns a
// raw token for the first remembered device.
func (m *Manager) Setup(code, password string, device DeviceInfo) (string, Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.load()
	if err != nil {
		return "", Session{}, err
	}
	if state.Password != nil {
		return "", Session{}, ErrAlreadyConfigured
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(code)), []byte(m.setupCode)) != 1 || m.setupCode == "" {
		return "", Session{}, ErrInvalidSetupCode
	}
	record, err := hashPassword(password)
	if err != nil {
		return "", Session{}, err
	}
	state.Password = &record
	token, session, stored, err := m.newSession(device)
	if err != nil {
		return "", Session{}, err
	}
	state.Sessions = []sessionRecord{stored}
	if err := m.save(state); err != nil {
		return "", Session{}, err
	}
	m.setupCode = ""
	return token, session, nil
}

// Login verifies the password and creates another concurrent remembered
// device session.
func (m *Manager) Login(password string, device DeviceInfo) (string, Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.load()
	if err != nil {
		return "", Session{}, err
	}
	if state.Password == nil {
		return "", Session{}, ErrSetupRequired
	}
	if !verifyPassword(password, *state.Password) {
		return "", Session{}, ErrInvalidCredentials
	}
	now := m.now().UTC()
	state.Sessions = liveSessions(state.Sessions, now)
	token, session, stored, err := m.newSession(device)
	if err != nil {
		return "", Session{}, err
	}
	state.Sessions = append(state.Sessions, stored)
	if err := m.save(state); err != nil {
		return "", Session{}, err
	}
	return token, session, nil
}

// Authenticate verifies a raw cookie token and rolls its expiry at most once
// per hour to avoid a config write on every API request.
func (m *Manager) Authenticate(token string) (Session, bool, error) {
	if strings.TrimSpace(token) == "" {
		return Session{}, false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.load()
	if err != nil {
		return Session{}, false, err
	}
	now := m.now().UTC()
	wanted := tokenDigest(token)
	live := liveSessions(state.Sessions, now)
	changed := len(live) != len(state.Sessions)
	for index := range live {
		if subtle.ConstantTimeCompare([]byte(live[index].TokenHash), []byte(wanted)) != 1 {
			continue
		}
		if now.Sub(live[index].LastUsed) >= sessionRefreshAfter {
			live[index].LastUsed = now
			live[index].ExpiresAt = now.Add(sessionLifetime)
			changed = true
		}
		state.Sessions = live
		if changed {
			if err := m.save(state); err != nil {
				return Session{}, false, err
			}
		}
		return publicSession(live[index]), true, nil
	}
	if changed {
		state.Sessions = live
		if err := m.save(state); err != nil {
			return Session{}, false, err
		}
	}
	return Session{}, false, nil
}

func (m *Manager) Logout(token string) error {
	return m.removeSessions(func(session sessionRecord) bool {
		return subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(tokenDigest(token))) == 1
	})
}

func (m *Manager) Revoke(id string) error {
	removed := false
	err := m.removeSessions(func(session sessionRecord) bool {
		if session.ID == id {
			removed = true
			return true
		}
		return false
	})
	if err == nil && !removed {
		return ErrInvalidSession
	}
	return err
}

func (m *Manager) Sessions(currentToken string) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.load()
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	live := liveSessions(state.Sessions, now)
	if len(live) != len(state.Sessions) {
		state.Sessions = live
		if err := m.save(state); err != nil {
			return nil, err
		}
	}
	currentHash := tokenDigest(currentToken)
	out := make([]Session, 0, len(live))
	for _, stored := range live {
		item := publicSession(stored)
		item.Current = subtle.ConstantTimeCompare([]byte(stored.TokenHash), []byte(currentHash)) == 1
		out = append(out, item)
	}
	return out, nil
}

// ChangePassword verifies the current password, stores a new hash, and
// revokes every session except the caller's current device.
func (m *Manager) ChangePassword(currentPassword, newPassword, currentToken string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.load()
	if err != nil {
		return err
	}
	if state.Password == nil || !verifyPassword(currentPassword, *state.Password) {
		return ErrInvalidCredentials
	}
	record, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	currentHash := tokenDigest(currentToken)
	kept := state.Sessions[:0]
	for _, session := range state.Sessions {
		if subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(currentHash)) == 1 {
			kept = append(kept, session)
		}
	}
	state.Password = &record
	state.Sessions = kept
	return m.save(state)
}

func (m *Manager) removeSessions(remove func(sessionRecord) bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.load()
	if err != nil {
		return err
	}
	kept := state.Sessions[:0]
	for _, session := range state.Sessions {
		if !remove(session) {
			kept = append(kept, session)
		}
	}
	state.Sessions = kept
	return m.save(state)
}

func (m *Manager) newSession(device DeviceInfo) (string, Session, sessionRecord, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", Session{}, sessionRecord{}, err
	}
	id, err := randomHex(12)
	if err != nil {
		return "", Session{}, sessionRecord{}, err
	}
	now := m.now().UTC()
	name := strings.TrimSpace(device.Name)
	if name == "" {
		name = "Browser"
	}
	stored := sessionRecord{
		ID: id, TokenHash: tokenDigest(token), Device: name,
		UserAgent: strings.TrimSpace(device.UserAgent), RemoteIP: strings.TrimSpace(device.RemoteIP),
		CreatedAt: now, LastUsed: now, ExpiresAt: now.Add(sessionLifetime),
	}
	return token, publicSession(stored), stored, nil
}

func (m *Manager) load() (persistedState, error) {
	f, err := m.data.Load()
	if err != nil {
		return persistedState{}, err
	}
	if len(f.Auth) == 0 {
		return persistedState{}, nil
	}
	var state persistedState
	if err := json.Unmarshal(f.Auth, &state); err != nil {
		return persistedState{}, fmt.Errorf("parse authentication state: %w", err)
	}
	return state, nil
}

func (m *Manager) save(state persistedState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return m.data.Update(func(file *appdata.File) error {
		file.Version = 1
		file.Auth = raw
		return nil
	})
}

func hashPassword(password string) (passwordRecord, error) {
	if err := validatePassword(password); err != nil {
		return passwordRecord{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return passwordRecord{}, err
	}
	record := passwordRecord{
		Algorithm: "argon2id", Salt: base64.RawStdEncoding.EncodeToString(salt),
		MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, KeyLength: 32,
	}
	digest := argon2.IDKey([]byte(password), salt, record.Iterations, record.MemoryKiB, record.Parallelism, record.KeyLength)
	record.Hash = base64.RawStdEncoding.EncodeToString(digest)
	return record, nil
}

func verifyPassword(password string, record passwordRecord) bool {
	if record.Algorithm != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(record.Salt)
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(record.Hash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, record.Iterations, record.MemoryKiB, record.Parallelism, record.KeyLength)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func validatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < minimumPasswordRunes || length > maximumPasswordRunes {
		return fmt.Errorf("password must be between %d and %d characters", minimumPasswordRunes, maximumPasswordRunes)
	}
	return nil
}

func randomToken(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func randomHex(bytes int) (string, error) {
	data := make([]byte, bytes)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func liveSessions(sessions []sessionRecord, now time.Time) []sessionRecord {
	live := make([]sessionRecord, 0, len(sessions))
	for _, session := range sessions {
		if session.ExpiresAt.After(now) {
			live = append(live, session)
		}
	}
	return live
}

func publicSession(stored sessionRecord) Session {
	return Session{
		ID: stored.ID, Device: stored.Device, UserAgent: stored.UserAgent, RemoteIP: stored.RemoteIP,
		CreatedAt: stored.CreatedAt, LastUsed: stored.LastUsed, ExpiresAt: stored.ExpiresAt,
	}
}
