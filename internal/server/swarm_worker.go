package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/coordinator"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

type swarmWorker struct {
	s         *Server
	request   workerRequest
	d         *dispatch
	mu        sync.Mutex
	started   bool
	lease     *time.Timer
	localHome bool
}
type workerControl struct {
	Home   string `json:"home"`
	ID     string `json:"id"`
	Token  string `json:"token"`
	Action string `json:"action,omitempty"`
	Bot    string `json:"bot,omitempty"`
}
type groupCall struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Action    string `json:"action"`
	Bot       string `json:"bot"`
	To        string `json:"to,omitempty"`
	Content   string `json:"content,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

func workerKey(home, id string) string      { return swarm.Key(home, id) }
func projectionID(home, chat string) string { return "swarm:" + home + ":" + chat }

func (w *swarmWorker) rosterPrompt(bot string) string {
	lines := []string{"You execute on machine @" + w.s.Swarm.Namespace(w.s.Swarm.Node().ID) + ". All your files, models and tools belong to this machine. Other machines can only be asked to act through their selected bots. This turn's participants:"}
	for _, p := range w.request.Participants {
		lines = append(lines, fmt.Sprintf("@%s (bot id %s)", p.Handle, p.key()))
		if p.Ref.NodeID == w.s.Swarm.Node().ID && p.Ref.ID == bot {
			lines = append(lines, "Your fully qualified identity is @"+p.Handle+".")
		}
	}
	return strings.Join(lines, "\n")
}
func (w *swarmWorker) recipient(value string) (string, error) {
	value = strings.TrimPrefix(strings.ToLower(value), "@")
	for _, p := range w.request.Participants {
		if value == p.key() || value == p.Handle || p.Ref.NodeID == w.s.Swarm.Node().ID && (value == p.Ref.ID || strings.HasSuffix(p.Handle, "."+value)) {
			return p.key(), nil
		}
	}
	return "", errors.New("handoffs must target another bot selected in this turn")
}

func (s *Server) prepareWorkerRemote(ctx context.Context, node string, req workerRequest) error {
	if node == s.Swarm.Node().ID {
		return s.prepareWorker(req)
	}
	return s.Swarm.JSON(ctx, node, "POST", "/api/v1/swarm/workers/prepare", req, nil)
}
func (s *Server) startWorkerRemote(ctx context.Context, node string, req workerRequest) error {
	if node == s.Swarm.Node().ID {
		return s.startWorker(workerControl{Home: req.Home, ID: req.ID, Token: req.Token})
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		child, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = s.Swarm.JSON(child, node, "POST", "/api/v1/swarm/workers/start", workerControl{Home: req.Home, ID: req.ID, Token: req.Token}, nil)
		cancel()
		if err == nil || ctx.Err() != nil {
			return err
		}
	}
	return err
}

func (s *Server) prepareWorker(req workerRequest) error {
	if err := s.validateWorker(req); err != nil {
		return err
	}
	key := workerKey(req.Home, req.ID)
	s.swarmMu.Lock()
	defer s.swarmMu.Unlock()
	if s.swarmWorkers == nil {
		s.swarmWorkers = map[string]*swarmWorker{}
	}
	if existing := s.swarmWorkers[key]; existing != nil {
		if subtle.ConstantTimeCompare([]byte(existing.request.Token), []byte(req.Token)) != 1 {
			return errors.New("worker identity collision")
		}
		return nil
	}
	var prior workerRequest
	if s.Store.Get("swarm-workers", key, &prior) == nil && prior.Status != "released" && prior.Status != "prepared" {
		if subtle.ConstantTimeCompare([]byte(prior.Token), []byte(req.Token)) != 1 {
			return errors.New("worker identity collision")
		}
		return nil // Idempotent acceptance never re-executes tools.
	}
	if s.Store.ChatDeleted(projectionID(req.Home, req.Chat.ID)) {
		return errors.New("chat was deleted")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	chatID := projectionID(req.Home, req.Chat.ID)
	local := req.Home == s.Swarm.Node().ID
	if local {
		chatID = req.Chat.ID
	}
	d := &dispatch{id: "worker:" + key, chatID: chatID, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: map[string]chan struct{}{}, admitted: map[string]chan struct{}{}, active: map[string]bool{}}
	w := &swarmWorker{s: s, request: req, d: d, localHome: local}
	d.worker = w
	d.group = &workerGroup{worker: w, drains: map[string]int{}}
	s.mu.Lock()
	if s.botReservations == nil {
		s.botReservations = map[string]map[string]bool{}
		s.botQueues = map[string][]*dispatch{}
	}
	for _, run := range req.Runs {
		bot, err := s.Bot(run.BotID)
		if err != nil || s.revoking[run.BotID] > 0 || !bot.EnableMultitasking && (len(s.botReservations[run.BotID]) > 0 || len(s.botQueues[run.BotID]) > 0) {
			s.mu.Unlock()
			cancel()
			return errors.New("worker bots are busy")
		}
	}
	for _, run := range req.Runs {
		if s.botReservations[run.BotID] == nil {
			s.botReservations[run.BotID] = map[string]bool{}
		}
		s.botReservations[run.BotID][d.id] = true
		d.admitted[run.BotID] = make(chan struct{})
		close(d.admitted[run.BotID])
		d.wake[run.BotID] = make(chan struct{}, 1)
		d.active[run.BotID] = true
	}
	s.mu.Unlock()
	req.Status = "prepared"
	w.request = req
	if err := s.Store.Put("swarm-workers", key, req); err != nil {
		s.mu.Lock()
		s.releaseLocked(d)
		s.mu.Unlock()
		cancel()
		return err
	}
	s.swarmWorkers[key] = w
	w.lease = time.AfterFunc(20*time.Second, func() {
		w.mu.Lock()
		started := w.started
		w.mu.Unlock()
		if !started {
			_, _ = s.controlWorker(workerControl{Home: req.Home, ID: req.ID, Token: req.Token, Action: "release"})
		}
	})
	return nil
}

func (s *Server) workerRecord(input workerControl) (workerRequest, error) {
	var req workerRequest
	err := s.Store.Get("swarm-workers", workerKey(input.Home, input.ID), &req)
	if err != nil {
		return req, err
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(input.Token)) != 1 {
		return req, errors.New("invalid worker grant")
	}
	return req, nil
}

func (s *Server) startWorker(input workerControl) error {
	key := workerKey(input.Home, input.ID)
	s.swarmMu.Lock()
	w := s.swarmWorkers[key]
	s.swarmMu.Unlock()
	if w == nil {
		prior, err := s.workerRecord(input)
		if err == nil && prior.Status != "prepared" && prior.Status != "released" {
			return nil
		}
		return errors.New("worker reservation expired")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(w.request.Token), []byte(input.Token)) != 1 {
		return errors.New("invalid worker grant")
	}
	if w.started {
		return nil
	}
	if err := w.d.ctx.Err(); err != nil {
		return err
	}
	if err := s.validateWorker(w.request); err != nil {
		return err
	}
	// A local worker shares the coordinator process. Keep its normalized bot
	// references and model defaults out of the immutable outbound requests.
	encoded, err := json.Marshal(w.request)
	if err != nil {
		return err
	}
	var req workerRequest
	if err = json.Unmarshal(encoded, &req); err != nil {
		return err
	}
	c := req.Chat
	c.ID = w.d.chatID
	c.Projection = !w.localHome
	c.OwnerNodeID = req.Home
	message := req.Message
	message.ChatID = c.ID
	message.Recipients = nil
	for _, run := range req.Runs {
		message.Recipients = append(message.Recipients, run.BotID)
	}
	for i := range message.Segments {
		seg := &message.Segments[i]
		if seg.Type == "bot" {
			node, id := swarm.Split(seg.BotID)
			if seg.NodeID != "" {
				node = seg.NodeID
			}
			if node == s.Swarm.Node().ID {
				seg.BotID = id
			} else {
				seg.Type = "text"
				seg.BotID = ""
			}
		}
		if seg.Type == "file" {
			seg.AllowedBots = nil
			if seg.NodeID == s.Swarm.Node().ID {
				for _, run := range req.Runs {
					bot, _ := s.Bot(run.BotID)
					if kind, err := s.Files.ReferenceKind(bot, seg.Path); err == nil {
						seg.AllowedBots = append(seg.AllowedBots, bot.ID)
						seg.Kind = kind
					}
				}
			}
		}
	}
	for i := range req.Runs {
		run := &req.Runs[i]
		run.ChatID = c.ID
		if run.Generation.EndpointID == "" {
			run.Generation = s.defaultGeneration()
		}
		if run.Generation.Model == "" {
			var ep domain.Endpoint
			_ = s.Store.Get("endpoints", run.Generation.EndpointID, &ep)
			run.Generation.Model = ep.Model
		}
	}
	if !w.localHome {
		if _, err := s.Store.Chat(c.ID); errors.Is(err, sql.ErrNoRows) {
			if err = s.Store.CreateChat(c); err != nil {
				return err
			}
		}
		for i := range message.Attachments {
			a := &message.Attachments[i]
			ctx, cancel := context.WithTimeout(w.d.ctx, 30*time.Second)
			response, err := s.Swarm.RoundTrip(ctx, req.Home, "GET", "/api/v1/chats/"+url.PathEscape(req.Chat.ID)+"/attachments/"+url.PathEscape(a.ID), nil)
			if err != nil {
				cancel()
				return err
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
			response.Body.Close()
			cancel()
			if err != nil {
				return err
			}
			if response.StatusCode != 200 || int64(len(data)) != a.Size {
				return errors.New("could not transfer attachment")
			}
			a.ChatID = c.ID
			a.NodeID = ""
			a.OriginChatID = ""
			if err = s.Store.AddAttachment(*a, data); err != nil {
				return err
			}
		}
		req.Status = "running"
		if err := s.Store.AcceptWithRecord(c, message, req.Runs, req.RequestID, req.ID, "swarm-workers", key, req); err != nil {
			return err
		}
	} else {
		req.Status = "running"
		if err := s.Store.Put("swarm-workers", key, req); err != nil {
			return err
		}
	}
	w.started = true
	if w.lease != nil {
		w.lease.Stop()
	}
	w.request = req
	s.mu.Lock()
	s.dispatches[w.d.id] = w.d
	s.mu.Unlock()
	for _, run := range req.Runs {
		w.d.wg.Add(1)
		go s.executeWorkerBot(w, run)
	}
	go s.finishWorker(w)
	return nil
}

func (s *Server) executeWorkerBot(w *swarmWorker, run domain.Run) {
	defer w.d.wg.Done()
	bot, err := s.Bot(run.BotID)
	if err != nil {
		w.d.cancel()
		return
	}
	var endpoint domain.Endpoint
	if err = s.Store.Get("endpoints", run.Generation.EndpointID, &endpoint); err != nil {
		w.d.cancel()
		return
	}
	for {
		if err = w.d.ctx.Err(); err == nil {
			run.Status = "running"
			run.UpdatedAt = domain.Now()
			_ = s.Store.Put("runs", run.ID, run)
			s.emit("run", run.ChatID, run.ID, run.BotID, run)
			err = s.runBot(w.d.ctx, w.d, bot, run, endpoint)
		}
		run.Status = "completed"
		run.Error = ""
		if err != nil {
			run.Status = "failed"
			run.Error = err.Error()
			if w.d.ctx.Err() != nil {
				run.Status = "cancelled"
			}
			w.d.group.Fail(bot.ID, err)
		}
		run.UpdatedAt = domain.Now()
		_ = s.Store.Put("runs", run.ID, run)
		s.emit("run", run.ChatID, run.ID, run.BotID, run)
		w.d.mu.Lock()
		w.d.active[bot.ID] = false
		w.d.mu.Unlock()
		_, reportErr := w.groupCall(context.Background(), groupCall{Action: "idle", Bot: bot.ID})
		if w.d.ctx.Err() != nil {
			return
		}
		if reportErr != nil {
			return
		} // Completed work remains durable for home reconciliation.
		select {
		case <-w.d.ctx.Done():
			return
		case <-w.d.wake[bot.ID]:
			w.d.mu.Lock()
			w.d.active[bot.ID] = true
			w.d.mu.Unlock()
			err = nil
		}
	}
}

func (s *Server) finishWorker(w *swarmWorker) {
	// A lost home does not cancel an executing tool. Once all local bots are
	// idle, release their reservations rather than waiting forever for the home.
	go func() {
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-w.d.ctx.Done():
				return
			case <-tick.C:
				_, err := w.groupCall(context.Background(), groupCall{Action: "alive"})
				if err != nil {
					w.d.mu.Lock()
					idle := true
					for _, active := range w.d.active {
						if active {
							idle = false
						}
					}
					w.d.mu.Unlock()
					if idle {
						w.d.cancel()
						return
					}
				}
			}
		}
	}()
	w.d.wg.Wait()
	w.d.cancel()
	w.mu.Lock()
	if s.ctx.Err() != nil {
		w.request.Status = "interrupted"
	} else if w.request.Status == "running" {
		w.request.Status = "completed"
	}
	_ = s.Store.Put("swarm-workers", workerKey(w.request.Home, w.request.ID), w.request)
	w.mu.Unlock()
	s.mu.Lock()
	delete(s.dispatches, w.d.id)
	s.releaseLocked(w.d)
	s.mu.Unlock()
	s.swarmMu.Lock()
	delete(s.swarmWorkers, workerKey(w.request.Home, w.request.ID))
	s.swarmMu.Unlock()
	close(w.d.done)
	s.emit("changed", w.d.chatID, "", "", nil)
}

func (s *Server) controlWorker(input workerControl) (any, error) {
	req, err := s.workerRecord(input)
	if err != nil {
		return nil, err
	}
	key := workerKey(input.Home, input.ID)
	s.swarmMu.Lock()
	w := s.swarmWorkers[key]
	s.swarmMu.Unlock()
	if w == nil {
		if input.Action == "delete" && req.Home != s.Swarm.Node().ID {
			_ = s.Store.DeleteChat(projectionID(req.Home, req.Chat.ID))
			req.Status = "deleted"
			_ = s.Store.Put("swarm-workers", key, req)
		}
		if input.Action == "stop" {
			req.Status = "cancelled"
			_ = s.Store.Put("swarm-workers", key, req)
		}
		return map[string]any{"status": req.Status}, nil
	}
	if input.Action == "wake" {
		_, bot := swarm.Split(input.Bot)
		if wake := w.d.wake[bot]; wake != nil {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		return true, nil
	}
	w.mu.Lock()
	started := w.started
	if input.Action == "release" && started {
		w.mu.Unlock()
		return true, nil
	}
	if input.Action == "finish" && started {
		w.d.mu.Lock()
		active := false
		for _, value := range w.d.active {
			active = active || value
		}
		w.d.mu.Unlock()
		if active {
			w.mu.Unlock()
			return true, nil
		}
	}
	if w.lease != nil {
		w.lease.Stop()
	}
	if input.Action == "stop" || input.Action == "delete" {
		w.request.Status = "cancelled"
		if err := s.Store.Put("swarm-workers", key, w.request); err != nil {
			w.mu.Unlock()
			return nil, err
		}
	}
	w.d.cancel()
	w.mu.Unlock()
	if !started {
		req.Status = "released"
		if input.Action != "release" {
			req.Status = "cancelled"
		}
		_ = s.Store.Put("swarm-workers", key, req)
		s.mu.Lock()
		s.releaseLocked(w.d)
		s.mu.Unlock()
		s.swarmMu.Lock()
		delete(s.swarmWorkers, key)
		s.swarmMu.Unlock()
	}
	if input.Action == "delete" && req.Home != s.Swarm.Node().ID {
		if started {
			<-w.d.done
		}
		_ = s.Store.DeleteChat(projectionID(req.Home, req.Chat.ID))
		req.Status = "deleted"
		_ = s.Store.Put("swarm-workers", key, req)
	}
	return true, nil
}

func (s *Server) snapshotWorker(input workerControl) (workerSnapshot, error) {
	req, err := s.workerRecord(input)
	if err != nil {
		return workerSnapshot{}, err
	}
	chatID := projectionID(req.Home, req.Chat.ID)
	if req.Home == s.Swarm.Node().ID {
		chatID = req.Chat.ID
	}
	messages, seq, err := s.snapshot(chatID)
	if err != nil {
		return workerSnapshot{}, err
	}
	runs := []domain.Run{}
	messages = slices.DeleteFunc(messages, func(m domain.Message) bool {
		return !slices.ContainsFunc(req.Runs, func(run domain.Run) bool { return run.ID == m.RunID })
	})
	for _, saved := range req.Runs {
		var run domain.Run
		if s.Store.Get("runs", saved.ID, &run) == nil {
			runs = append(runs, run)
		}
	}
	return workerSnapshot{Status: req.Status, Messages: messages, Runs: runs, Sequence: seq, Epoch: s.Swarm.Node().Epoch}, nil
}

func (w *swarmWorker) groupCall(ctx context.Context, input groupCall) (json.RawMessage, error) {
	input.ID, input.Token = w.request.ID, w.request.Token
	if input.Bot != "" && !strings.Contains(input.Bot, ":") {
		input.Bot = swarm.Key(w.s.Swarm.Node().ID, input.Bot)
	}
	ctx, cancel := context.WithTimeout(ctx, 125*time.Second)
	defer cancel()
	if input.Action != "wait" {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, 4*time.Second)
		defer stop()
	}
	if w.localHome {
		value, err := w.s.coordinateWorker(ctx, w.s.Swarm.Node().ID, input)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(value)
		return data, err
	}
	var out json.RawMessage
	err := w.s.Swarm.JSON(ctx, w.request.Home, "POST", "/api/v1/swarm/workers/group", input, &out)
	return out, err
}

type workerGroup struct {
	worker *swarmWorker
	mu     sync.Mutex
	drains map[string]int
}

func (g *workerGroup) Send(from, to, content, id string) (coordinator.Request, error) {
	var out coordinator.Request
	raw, err := g.worker.groupCall(g.worker.d.ctx, groupCall{Action: "send", Bot: from, To: to, Content: content, RequestID: id})
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (g *workerGroup) Drain(bot string) []coordinator.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []coordinator.Request
	id := fmt.Sprintf("%s:%d", bot, g.drains[bot])
	raw, err := g.worker.groupCall(g.worker.d.ctx, groupCall{Action: "drain", Bot: bot, RequestID: id})
	if err == nil && json.Unmarshal(raw, &out) == nil {
		g.drains[bot]++
	}
	return out
}
func (g *workerGroup) Pending(bot string) bool {
	raw, err := g.worker.groupCall(g.worker.d.ctx, groupCall{Action: "pending", Bot: bot})
	var out bool
	return err == nil && json.Unmarshal(raw, &out) == nil && out
}
func (g *workerGroup) Reply(bot, id, content string) error {
	_, err := g.worker.groupCall(g.worker.d.ctx, groupCall{Action: "reply", Bot: bot, RequestID: id, Content: content})
	return err
}
func (g *workerGroup) Wait(ctx context.Context, bot, id string) (coordinator.Request, error) {
	var out coordinator.Request
	raw, err := g.worker.groupCall(ctx, groupCall{Action: "wait", Bot: bot, RequestID: id})
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (g *workerGroup) WaitTarget(bot, id string) (string, error) {
	raw, err := g.worker.groupCall(g.worker.d.ctx, groupCall{Action: "target", Bot: bot, RequestID: id})
	var out string
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (g *workerGroup) Close() {}
func (g *workerGroup) Fail(bot string, err error) {
	_, _ = g.worker.groupCall(context.Background(), groupCall{Action: "fail", Bot: bot, Content: err.Error()})
}

func (s *Server) coordinateWorker(ctx context.Context, node string, input groupCall) (any, error) {
	s.mu.Lock()
	d := s.dispatches[input.ID]
	s.mu.Unlock()
	if d == nil || d.federation == nil {
		return nil, errors.New("chat home is not coordinating this request; finish current work independently")
	}
	f := d.federation
	host, ok := f.Hosts[node]
	if !ok || subtle.ConstantTimeCompare([]byte(input.Token), []byte(host.Token)) != 1 {
		return nil, errors.New("invalid dispatch grant")
	}
	if input.Action == "alive" {
		return true, nil
	}
	if !slices.ContainsFunc(host.Runs, func(run domain.Run) bool { return swarm.Key(node, run.BotID) == input.Bot }) {
		return nil, errors.New("worker cannot act as another bot")
	}
	switch input.Action {
	case "send":
		if input.RequestID == "" {
			input.RequestID = domain.ID()
		}
		return f.group.SendWithCommit(input.Bot, input.To, input.Content, input.RequestID, func(value coordinator.Request) error { return s.Store.Put("swarm-handoffs:"+f.ID, value.ID, value) })
	case "reply":
		err := f.group.ReplyWithCommit(input.Bot, input.RequestID, input.Content, func(value coordinator.Request) error { return s.Store.Put("swarm-replies:"+f.ID, value.ID, value) })
		return true, err
	case "wait":
		child, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return f.group.Wait(child, input.Bot, input.RequestID)
	case "target":
		return f.group.WaitTarget(input.Bot, input.RequestID)
	case "pending":
		return f.group.Pending(input.Bot), nil
	case "drain":
		f.journalMu.Lock()
		defer f.journalMu.Unlock()
		key := input.Bot + ":" + input.RequestID
		var prior []coordinator.Request
		if s.Store.Get("swarm-drains:"+f.ID, key, &prior) == nil {
			return prior, nil
		}
		return f.group.DrainWithCommit(input.Bot, func(requests []coordinator.Request) error { return s.Store.Put("swarm-drains:"+f.ID, key, requests) })
	case "fail":
		f.group.Fail(input.Bot, errors.New(input.Content))
		f.mu.Lock()
		d.active[input.Bot] = false
		f.mu.Unlock()
		return true, nil
	case "idle":
		f.mu.Lock()
		pending := f.group.Pending(input.Bot)
		d.active[input.Bot] = pending
		f.mu.Unlock()
		if pending {
			go s.workerControl(f, node, "wake", input.Bot)
		}
		return true, nil
	}
	return nil, errors.New("unknown coordinator action")
}

func (s *Server) swarmWorkerRoutes(mux *http.ServeMux) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			caller, ok := swarm.Caller(r.Context())
			if !ok || caller.Pairing {
				http.Error(w, "Peer-only worker endpoint", 403)
				return
			}
			handler(w, r)
		}
	}
	mux.HandleFunc("POST /api/v1/swarm/workers/stream", guard(func(w http.ResponseWriter, r *http.Request) {
		var req workerControl
		if err := decode(w, r, &req); err != nil {
			respond(w, nil, err)
			return
		}
		caller, _ := swarm.Caller(r.Context())
		if req.Home != caller.NodeID {
			http.Error(w, "Coordinator identity mismatch", 403)
			return
		}
		if _, err := s.workerRecord(req); err != nil {
			respond(w, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		encoder := json.NewEncoder(w)
		flush := http.NewResponseController(w)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		last := int64(-1)
		for {
			if seq := s.seq.Load(); seq != last {
				snapshot, err := s.snapshotWorker(req)
				if err != nil {
					return
				}
				_ = flush.SetWriteDeadline(time.Now().Add(15 * time.Second))
				if encoder.Encode(snapshot) != nil || flush.Flush() != nil {
					return
				}
				last = snapshot.Sequence
			}
			select {
			case <-r.Context().Done():
				return
			case <-s.ctx.Done():
				return
			case <-tick.C:
			}
		}
	}))
	for _, action := range []string{"validate", "prepare"} {
		mux.HandleFunc("POST /api/v1/swarm/workers/"+action, guard(func(w http.ResponseWriter, r *http.Request) {
			var req workerRequest
			if err := decode(w, r, &req); err != nil {
				respond(w, nil, err)
				return
			}
			caller, _ := swarm.Caller(r.Context())
			if req.Home != caller.NodeID {
				http.Error(w, "Coordinator identity mismatch", 403)
				return
			}
			var err error
			if action == "validate" {
				err = s.validateWorker(req)
			} else {
				err = s.prepareWorker(req)
			}
			respond(w, true, err)
		}))
	}
	for _, action := range []string{"start", "control", "snapshot"} {
		mux.HandleFunc("POST /api/v1/swarm/workers/"+action, guard(func(w http.ResponseWriter, r *http.Request) {
			var req workerControl
			if err := decode(w, r, &req); err != nil {
				respond(w, nil, err)
				return
			}
			caller, _ := swarm.Caller(r.Context())
			if req.Home != caller.NodeID {
				http.Error(w, "Coordinator identity mismatch", 403)
				return
			}
			var value any
			var err error
			switch action {
			case "start":
				err = s.startWorker(req)
				value = true
			case "control":
				value, err = s.controlWorker(req)
			case "snapshot":
				value, err = s.snapshotWorker(req)
			}
			respond(w, value, err)
		}))
	}
	mux.HandleFunc("POST /api/v1/swarm/workers/group", guard(func(w http.ResponseWriter, r *http.Request) {
		var req groupCall
		if err := decode(w, r, &req); err != nil {
			respond(w, nil, err)
			return
		}
		caller, _ := swarm.Caller(r.Context())
		value, err := s.coordinateWorker(r.Context(), caller.NodeID, req)
		respond(w, value, err)
	}))
}
