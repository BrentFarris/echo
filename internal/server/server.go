package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/coordinator"
	"github.com/brent/echo/internal/credentials"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/store"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

type Server struct {
	Store         *store.Store
	Plugins       *plugins.Host
	Calendar      *calendar.Calendar
	Files         *broker.Files
	Process       *broker.ProcessBroker
	Model         *model.Client
	Secrets       credentials.SecretStore
	Dir           string
	Assets        fs.FS
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	dispatches    map[string]*dispatch
	streams       map[string]domain.Message
	subscribers   map[chan domain.Event]bool
	seq           atomic.Int64
	authMu        sync.Mutex
	loginAttempts map[string][]time.Time
	revoking      map[string]int
	closingChats  map[string]int
}
type dispatch struct {
	id, chatID string
	group      *coordinator.Group
	cancel     context.CancelFunc
	done       chan struct{}
	wake       map[string]chan struct{}
	active     map[string]bool
	mu         sync.Mutex
	wg         sync.WaitGroup
	err        error
	cloud      bool
}
type SendRequest struct {
	Content      string            `json:"content"`
	Segments     []domain.Segment  `json:"segments"`
	Generation   domain.Generation `json:"generation"`
	RequestID    string            `json:"requestId"`
	Attachments  []string          `json:"attachments"`
	scheduledBot string
}

func New(dir string, assets fs.FS, guest []byte) (*Server, error) {
	dir, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	db, e := store.Open(dir)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{Store: db, Dir: dir, Assets: assets, ctx: ctx, cancel: cancel, Files: &broker.Files{Private: dir}, Process: &broker.ProcessBroker{Dir: filepath.Join(dir, "runtime")}, Model: model.New(), Secrets: credentials.New(), dispatches: map[string]*dispatch{}, streams: map[string]domain.Message{}, subscribers: map[chan domain.Event]bool{}, loginAttempts: map[string][]time.Time{}}
	s.revoking = map[string]int{}
	s.closingChats = map[string]int{}
	s.Plugins, e = plugins.New(ctx, db, filepath.Join(dir, "plugins"), guest, s.pluginCall)
	if e != nil {
		db.Close()
		cancel()
		return nil, e
	}
	if shell, e := s.Plugins.Get("shell"); e == nil {
		if _, configured := shell.Config["toolchains"]; !configured {
			shell.Config["toolchains"] = broker.DefaultToolchains()
			s.Plugins.Save(shell)
		}
	}
	var echo domain.Bot
	if db.Get("bots", "echo", &echo) != nil {
		e = db.Put("bots", "echo", domain.Bot{ID: "echo", Handle: "echo", Name: "Echo", Prompt: "You are Echo, a chat-first assistant and bot manager. Use tools to complete the user's request. A bot's handle identifies it in @mentions. Ask only for missing information. Plugin source must be self-contained and use the documented capability bridge.", Plugins: map[string]bool{}, Roots: []string{}, Revision: 1})
		if e != nil {
			s.Close()
			return nil, e
		}
	}
	if e = db.Recover(); e != nil {
		s.Close()
		return nil, e
	}
	if e = s.Process.Recover(); e != nil {
		s.Close()
		return nil, e
	}
	s.Calendar = &calendar.Calendar{Store: db, Dispatch: s.scheduled, Enabled: func(id string) bool { b, e := s.Bot(id); return e == nil && s.Plugins.Enabled(b, "calendar") }, Changed: func() { s.emit("changed", "", "", "", nil) }}
	if e = s.Calendar.Start(ctx); e != nil {
		s.Close()
		return nil, e
	}
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				db.Reclaim()
			}
		}
	}()
	return s, nil
}
func (s *Server) Close() {
	s.cancel()
	s.mu.Lock()
	ds := []*dispatch{}
	for _, d := range s.dispatches {
		d.cancel()
		d.group.Close()
		ds = append(ds, d)
	}
	s.mu.Unlock()
	for _, d := range ds {
		<-d.done
	}
	if s.Calendar != nil {
		s.Calendar.Stop()
	}
	if s.Plugins != nil {
		s.Plugins.Close(context.Background())
	}
	s.Store.Close()
}
func (s *Server) Bot(id string) (domain.Bot, error) {
	var b domain.Bot
	e := s.Store.Get("bots", id, &b)
	return b, e
}
func (s *Server) stopCloudRuns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.dispatches {
		if d.cloud {
			d.cancel()
			d.group.Close()
		}
	}
}
func (s *Server) Bots() ([]domain.Bot, error) {
	r, e := s.Store.List("bots")
	if e != nil {
		return nil, e
	}
	return store.Decode[domain.Bot](r)
}
func (s *Server) emit(kind, chat, run, bot string, payload any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(kind, chat, run, bot, payload)
}
func (s *Server) emitLocked(kind, chat, run, bot string, payload any) {
	b, _ := json.Marshal(payload)
	e := domain.Event{Sequence: s.seq.Add(1), Type: kind, ChatID: chat, RunID: run, BotID: bot, Payload: b}
	for ch := range s.subscribers {
		select {
		case ch <- e:
		default:
			close(ch)
			delete(s.subscribers, ch)
		}
	}
}
func (s *Server) stopChat(id string) func() {
	s.mu.Lock()
	s.closingChats[id]++
	ds := []*dispatch{}
	for _, d := range s.dispatches {
		if d.chatID == id {
			ds = append(ds, d)
			d.cancel()
			d.group.Close()
		}
	}
	s.mu.Unlock()
	for _, d := range ds {
		<-d.done
	}
	return func() { s.mu.Lock(); s.closingChats[id]--; s.mu.Unlock() }
}
func (s *Server) stopBot(id string) func() {
	s.mu.Lock()
	s.revoking[id]++
	ds := []*dispatch{}
	for _, d := range s.dispatches {
		if _, ok := d.wake[id]; ok {
			ds = append(ds, d)
			d.cancel()
			d.group.Close()
		}
	}
	s.mu.Unlock()
	for _, d := range ds {
		<-d.done
	}
	return func() { s.mu.Lock(); s.revoking[id]--; s.mu.Unlock() }
}
func (s *Server) messages(id string) ([]domain.Message, error) {
	ms, _, e := s.snapshot(id)
	return ms, e
}
func (s *Server) snapshot(id string) ([]domain.Message, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms, e := s.Store.Messages(id)
	if e != nil {
		return nil, 0, e
	}
	for i, m := range ms {
		if active, ok := s.streams[m.ID]; ok {
			ms[i] = active
		}
	}
	return ms, s.seq.Load(), nil
}
func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": e.Error()})
		return
	}
	json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func localHost(host string) bool {
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
func (s *Server) authenticated(r *http.Request) bool {
	c, e := r.Cookie("echo2-session")
	if e != nil {
		return false
	}
	h := sha256.Sum256([]byte(c.Value))
	var expires time.Time
	return s.Store.Get("sessions", hex.EncodeToString(h[:]), &expires) == nil && expires.After(time.Now())
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	token := domain.ID() + domain.ID()
	h := sha256.Sum256([]byte(token))
	if e := s.Store.Put("sessions", hex.EncodeToString(h[:]), time.Now().Add(30*24*time.Hour)); e != nil {
		respond(w, nil, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "echo2-session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
	respond(w, map[string]any{"authenticated": true}, nil)
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	mux.HandleFunc("POST /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() && localHost(r.Host) {
			s.session(w, r)
			return
		}
		var a struct {
			Password string `json:"password"`
		}
		if e := decode(w, r, &a); e != nil {
			respond(w, nil, e)
			return
		}
		s.authMu.Lock()
		now := time.Now()
		attempts := s.loginAttempts[host]
		recent := []time.Time{}
		for _, t := range attempts {
			if now.Sub(t) < time.Minute {
				recent = append(recent, t)
			}
		}
		blocked := len(recent) >= 5
		s.loginAttempts[host] = append(recent, now)
		s.authMu.Unlock()
		if blocked {
			http.Error(w, "Too many login attempts", 429)
			return
		}
		var hash string
		if s.Store.Get("auth", "password", &hash) != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(a.Password)) != nil {
			http.Error(w, "Invalid password", 401)
			return
		}
		s.session(w, r)
	})
	mux.HandleFunc("GET /api/v1/events", s.websocket)
	if s.Assets != nil {
		static := http.FileServerFS(s.Assets)
		mux.Handle("/", static)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		origin := r.Header.Get("Origin")
		if strings.HasPrefix(r.URL.Path, "/api/") && origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || u.Scheme != "http" && u.Scheme != "https" {
				http.Error(w, "Origin rejected", 403)
				return
			}
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		if r.URL.Path == "/renderer.html" {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
			if r.URL.Path != "/api/v1/session" && !s.authenticated(r) {
				http.Error(w, "Sign in required", 401)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		u, e := url.Parse(r.Header.Get("Origin"))
		return e == nil && u.Host == r.Host
	}}
	conn, e := up.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer conn.Close()
	ch := make(chan domain.Event, 256)
	s.mu.Lock()
	s.subscribers[ch] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }()
	conn.WriteJSON(domain.Event{Type: "reset", Sequence: s.seq.Load(), Payload: json.RawMessage(`null`)})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.SetReadLimit(1024)
		for {
			if _, _, e := conn.ReadMessage(); e != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-s.ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteJSON(e) != nil {
				return
			}
		case <-ticker.C:
			if !s.authenticated(r) {
				return
			}
			if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}

var handlePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

func (s *Server) saveBot(b domain.Bot) (domain.Bot, error) {
	b.Handle = strings.ToLower(strings.TrimSpace(b.Handle))
	if !handlePattern.MatchString(b.Handle) {
		return b, errors.New("handle must start with a letter and contain letters, digits, - or _")
	}
	if b.Name == "" {
		b.Name = b.Handle
	}
	if b.ID == "" {
		b.ID = domain.ID()
	}
	if b.ID != "echo" && b.Handle == "echo" {
		return b, errors.New("echo is reserved")
	}
	bots, e := s.Bots()
	if e != nil {
		return b, e
	}
	for _, other := range bots {
		if other.ID != b.ID && other.Handle == b.Handle {
			return b, errors.New("bot handle already exists")
		}
	}
	b.Roots, e = broker.CanonicalRoots(b.Roots)
	if e != nil {
		return b, e
	}
	for _, root := range b.Roots {
		if broker.Within(root, s.Dir) || broker.Within(s.Dir, root) {
			return b, errors.New("workspace cannot contain Echo private application data")
		}
	}
	if old, e := s.Bot(b.ID); e == nil {
		b.Revision = old.Revision + 1
	} else {
		b.Revision = 1
	}
	if b.ID == "echo" {
		b.Handle = "echo"
	}
	if b.Plugins == nil {
		b.Plugins = map[string]bool{}
	}
	return b, s.Store.Put("bots", b.ID, b)
}
func (s *Server) key(ctx context.Context, e domain.Endpoint) (string, error) {
	if e.Credential == "" {
		return "", nil
	}
	return s.Secrets.Get(ctx, e.Credential)
}
func (s *Server) scheduled(ctx context.Context, o calendar.Occurrence) error {
	b, e := s.Bot(o.BotID)
	if e != nil {
		return e
	}
	c := domain.Chat{ID: domain.ID(), Title: "Scheduled: " + truncate(o.Prompt, 70), Bots: map[string]string{}, Generation: o.Generation, CreatedAt: domain.Now(), UpdatedAt: domain.Now()}
	if e = s.Store.CreateChat(c); e != nil {
		return e
	}
	request := SendRequest{Content: o.Prompt, Segments: []domain.Segment{{Type: "bot", BotID: b.ID}, {Type: "text", Text: o.Prompt}}, Generation: o.Generation, scheduledBot: b.ID}
	d, e := s.send(c.ID, request)
	if e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		d.cancel()
		<-d.done
		return ctx.Err()
	case <-d.done:
		return d.err
	}
}
func truncate(v string, n int) string {
	r := []rune(v)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return v
}
func (s *Server) ListenAddress(port int) (string, string, string, error) {
	m, e := s.Plugins.Get("hosted-server")
	if e != nil {
		return "", "", "", e
	}
	remote, _ := m.Config["remote"].(bool)
	if configured, ok := m.Config["port"].(float64); ok && configured != 0 {
		if configured < 1 || configured > 65535 || configured != float64(int(configured)) {
			return "", "", "", errors.New("port must be 1-65535")
		}
		port = int(configured)
	}
	if !m.Enabled || !remote {
		return fmt.Sprintf("127.0.0.1:%d", port), "", "", nil
	}
	var hash string
	if s.Store.Get("auth", "password", &hash) != nil || hash == "" {
		return "", "", "", errors.New("remote hosting requires a password in Hosted Server settings")
	}
	cert, _ := m.Config["certificate"].(string)
	key, _ := m.Config["key"].(string)
	if (cert == "") != (key == "") {
		return "", "", "", errors.New("TLS requires both certificate and key")
	}
	return fmt.Sprintf("0.0.0.0:%d", port), cert, key, nil
}

var _ = os.ErrNotExist
