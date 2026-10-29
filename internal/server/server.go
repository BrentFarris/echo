package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
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
	"github.com/brent/echo/internal/development"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
	"github.com/gorilla/websocket"
)

type Server struct {
	jiraMu             sync.Mutex
	confluenceMu       sync.Mutex
	confluenceActive   map[string]context.CancelFunc
	notificationMu     sync.Mutex
	notificationWG     sync.WaitGroup
	notificationClient *http.Client // Injectable transport for push delivery tests.

	Swarm             *swarm.Manager
	closeOnce         sync.Once
	swarmMu           sync.Mutex
	swarmWG           sync.WaitGroup
	swarmWorkers      map[string]*swarmWorker
	EditorFS          *editorfs.Service
	EditorWatch       *editorfs.WatchManager
	editorMu          sync.Mutex
	editorClients     map[string]map[chan any]bool
	editorBookmarkMu  sync.Mutex
	Store             *store.Store
	Plugins           *plugins.Host
	Calendar          *calendar.Calendar
	Files             *broker.Files
	References        *broker.ReferenceIndex
	Process           *broker.ProcessBroker
	Model             *model.Client
	Secrets           credentials.SecretStore
	Dir               string
	Assets            fs.FS
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	dispatches        map[string]*dispatch
	botQueues         map[string][]*dispatch
	botReservations   map[string]map[string]bool
	streams           map[string]domain.Message
	subscribers       map[chan domain.Event]bool
	seq               atomic.Int64
	questionsMu       sync.Mutex
	pendingQuestions  map[string]*pendingQuestion
	authMu            sync.Mutex
	appDockMu         sync.Mutex
	loginAttempts     map[string][]time.Time
	sessionSockets    map[string]map[*websocket.Conn]bool
	revoking          map[string]int
	closingChats      map[string]int
	revealFile        func(broker.RevealTarget) error
	Development       *development.Manager
	rollouts          *rolloutManager
	maintenance       bool // Protected by mu; gates admission, not accepted work.
	shutdownRequested chan struct{}
}
type dispatch struct {
	id, chatID string
	group      coordination
	federation *swarmDispatch
	worker     *swarmWorker
	cancel     context.CancelFunc
	done       chan struct{}
	wake       map[string]chan struct{}
	active     map[string]bool
	mu         sync.Mutex
	wg         sync.WaitGroup
	err        error
	ctx        context.Context
	admitted   map[string]chan struct{}
	timer      *time.Timer // Protected by Server.mu; starts at first admission.
}
type SendRequest struct {
	GenerationByNode       map[string]domain.Generation `json:"generationByNode,omitempty"`
	OriginNodeID           string                       `json:"originNodeId,omitempty"`
	Permits                map[string]string            `json:"permits,omitempty"`
	Content                string                       `json:"content"`
	Segments               []domain.Segment             `json:"segments"`
	Generation             domain.Generation            `json:"generation"`
	RequestID              string                       `json:"requestId"`
	Attachments            []string                     `json:"attachments"`
	scheduledBot           string
	replaceMessageID       string
	replacementAttachments []domain.Attachment
	rerunMessageID         string
	rerunRecipients        []string
	rerunFingerprint       [32]byte
}

type coordination interface {
	Send(string, string, string, string) (coordinator.Request, error)
	Drain(string) []coordinator.Request
	Pending(string) bool
	Reply(string, string, string) error
	Wait(context.Context, string, string) (coordinator.Request, error)
	WaitTarget(string, string) (string, error)
	Close()
	Fail(string, error)
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
	s := &Server{Store: db, Dir: dir, Assets: assets, ctx: ctx, cancel: cancel, Files: &broker.Files{Private: dir}, Process: &broker.ProcessBroker{Dir: filepath.Join(dir, "runtime")}, Model: model.New(), Secrets: credentials.New(dir), dispatches: map[string]*dispatch{}, streams: map[string]domain.Message{}, subscribers: map[chan domain.Event]bool{}, pendingQuestions: map[string]*pendingQuestion{}, loginAttempts: map[string][]time.Time{}}
	s.revoking = map[string]int{}
	s.closingChats = map[string]int{}
	s.References = broker.NewReferenceIndex(ctx, s.Files)
	s.shutdownRequested = make(chan struct{}, 1)
	var developmentConfig development.Config
	if err := db.Get("settings", "development", &developmentConfig); err != nil && !errors.Is(err, sql.ErrNoRows) {
		db.Close()
		cancel()
		return nil, err
	}
	s.Development = development.New(ctx, dir, developmentConfig, func() {
		select {
		case s.shutdownRequested <- struct{}{}:
		default:
		}
	})
	s.Plugins, e = plugins.New(ctx, db, filepath.Join(dir, "plugins"), guest, s.pluginCall)
	if e != nil {
		db.Close()
		cancel()
		return nil, e
	}
	if e = s.initSwarm(); e != nil {
		s.Close()
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
	if e = s.recoverSwarm(); e != nil {
		s.Close()
		return nil, e
	}
	if e = s.Process.Recover(); e != nil {
		s.Close()
		return nil, e
	}
	if e = s.initRollouts(); e != nil {
		s.Close()
		return nil, e
	}
	s.Calendar = &calendar.Calendar{Store: db, Dispatch: s.scheduled, Deferred: s.maintenanceActive, Enabled: func(id string) bool { b, e := s.Bot(id); return e == nil && s.Plugins.Enabled(b, "calendar") }, Changed: func() { s.emit("changed", "", "", "", nil) }}
	if e = s.Calendar.Start(ctx); e != nil {
		s.Close()
		return nil, e
	}
	s.initEditor()
	s.startNotifications()
	s.Development.StartAutomaticChecks()
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
	s.closeOnce.Do(s.close)
}

func (s *Server) close() {
	s.cancel()
	if s.rollouts != nil {
		s.rollouts.wg.Wait()
	}
	s.swarmMu.Lock()
	prepared := []*swarmWorker{}
	for _, worker := range s.swarmWorkers {
		prepared = append(prepared, worker)
	}
	s.swarmMu.Unlock()
	for _, worker := range prepared {
		worker.mu.Lock()
		if worker.lease != nil {
			worker.lease.Stop()
		}
		worker.d.cancel()
		worker.mu.Unlock()
	}
	if s.Swarm != nil {
		s.Swarm.Close()
	}
	if s.EditorWatch != nil {
		s.EditorWatch.Close()
	}
	if s.EditorFS != nil {
		s.EditorFS.Close()
	}
	s.References.Close()
	if s.Development != nil {
		s.Development.Close()
	}
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
	s.swarmWG.Wait()
	s.notificationWG.Wait()
	s.Store.Close()
}
func (s *Server) Bot(id string) (domain.Bot, error) {
	var b domain.Bot
	e := s.Store.Get("bots", id, &b)
	return b, e
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
		}
	}
	s.mu.Unlock()
	for _, d := range ds {
		if d.federation != nil {
			_ = s.persistSwarmStop(d.federation)
		}
		d.cancel()
		d.group.Close()
	}
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
	runs, err := s.Store.ChatRuns(id)
	if err != nil {
		return nil, 0, err
	}
	inputs := rerunInputs(ms, runs)
	for i := range ms {
		if ms[i].Role == "assistant" {
			_, available := inputs[ms[i].RunID]
			ms[i].RerunUnavailable = !available
		}
	}
	return ms, s.seq.Load(), nil
}
func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		if errors.Is(e, errMaintenance) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
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
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	s.authRoutes(mux)
	mux.HandleFunc("GET /api/v1/events", s.websocket)
	if s.Assets != nil {
		static := http.FileServerFS(s.Assets)
		mux.Handle("/", static)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/index.html", "/sw.js", "/manifest.webmanifest", "/offline.html":
			w.Header().Set("Cache-Control", "no-cache")
		}
		// Windows MIME registrations do not consistently recognize PWA assets.
		switch r.URL.Path {
		case "/manifest.webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json")
		case "/sw.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
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
			if r.URL.Path != "/api/v1/session" && r.URL.Path != "/api/v1/swarm/pair" && r.URL.Path != "/api/v1/swarm/link" && !s.authenticated(r) {
				http.Error(w, "Sign in required", 401)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		if swarm.Owner(r.Context()) {
			return true
		}
		u, e := url.Parse(r.Header.Get("Origin"))
		return e == nil && u.Host == r.Host
	}}
	conn, e := up.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer conn.Close()
	s.authMu.Lock()
	if _, ok := s.savedSessionLocked(r); !ok && !swarm.Owner(r.Context()) {
		s.authMu.Unlock()
		return
	}
	id := sessionKey(r)
	if caller, ok := swarm.Caller(r.Context()); ok {
		id = "swarm:" + caller.NodeID
	}
	if s.sessionSockets == nil {
		s.sessionSockets = map[string]map[*websocket.Conn]bool{}
	}
	if s.sessionSockets[id] == nil {
		s.sessionSockets[id] = map[*websocket.Conn]bool{}
	}
	s.sessionSockets[id][conn] = true
	s.authMu.Unlock()
	defer func() {
		s.authMu.Lock()
		delete(s.sessionSockets[id], conn)
		if len(s.sessionSockets[id]) == 0 {
			delete(s.sessionSockets, id)
		}
		s.authMu.Unlock()
	}()
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
			if !ok || !s.authenticated(r) {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.normalizeBot(b)
	if err != nil {
		return b, err
	}
	return s.saveBotLocked(b)
}

func (s *Server) normalizeBot(b domain.Bot) (domain.Bot, error) {
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
	var existing *domain.Bot
	for _, other := range bots {
		if other.ID != b.ID && other.Handle == b.Handle {
			return b, errors.New("bot handle already exists")
		}
		if other.ID == b.ID {
			existing = &other
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
	if b.ID == "echo" {
		b.Handle = "echo"
	}
	b.Plugins = maps.Clone(b.Plugins)
	if existing != nil && b.Plugins == nil {
		b.Plugins = maps.Clone(existing.Plugins)
	}
	if b.Plugins == nil {
		b.Plugins = map[string]bool{}
	}
	if existing == nil && b.ID != "echo" {
		installed, err := s.Plugins.List()
		if err != nil {
			return b, err
		}
		for _, plugin := range installed {
			if plugin.Scope == "bot" || plugin.Scope == "" {
				if _, selected := b.Plugins[plugin.ID]; !selected {
					b.Plugins[plugin.ID] = true
				}
			}
		}
	}
	return b, nil
}
func (s *Server) key(ctx context.Context, e domain.Endpoint) (string, error) {
	if e.Credential == "" {
		return "", nil
	}
	return s.Secrets.Get(ctx, e.Credential)
}
func (s *Server) scheduled(ctx context.Context, o calendar.Occurrence) error {
	if s.maintenanceActive() {
		return calendar.ErrDeferred
	}
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
		if errors.Is(e, errMaintenance) {
			return calendar.ErrDeferred
		}
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
