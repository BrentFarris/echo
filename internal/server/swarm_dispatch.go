package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/coordinator"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/swarm"
)

type swarmParticipant struct {
	Ref      swarm.Ref `json:"ref"`
	Handle   string    `json:"handle"`
	Name     string    `json:"name"`
	Revision int64     `json:"revision"`
}

func (p swarmParticipant) key() string { return swarm.Key(p.Ref.NodeID, p.Ref.ID) }

type swarmDispatch struct {
	ID           string                   `json:"id"`
	ChatID       string                   `json:"chatId"`
	Origin       string                   `json:"origin"`
	RequestID    string                   `json:"requestId"`
	Participants []swarmParticipant       `json:"participants"`
	Hosts        map[string]workerRequest `json:"hosts"`
	Status       string                   `json:"status"`
	Reconciled   map[string]bool          `json:"reconciled,omitempty"`
	group        *coordinator.Group
	d            *dispatch
	mu           sync.Mutex
	journalMu    sync.Mutex
	replicas     map[string]string
}
type workerRequest struct {
	ID           string             `json:"id"`
	Home         string             `json:"home"`
	Origin       string             `json:"origin"`
	Token        string             `json:"token"`
	Chat         domain.Chat        `json:"chat"`
	Message      domain.Message     `json:"message"`
	Runs         []domain.Run       `json:"runs"`
	Participants []swarmParticipant `json:"participants"`
	RequestID    string             `json:"requestId"`
	Status       string             `json:"status"`
	CreatedAt    time.Time          `json:"createdAt"`
	Permit       string             `json:"permit,omitempty"`
}
type workerSnapshot struct {
	Status   string           `json:"status"`
	Messages []domain.Message `json:"messages"`
	Runs     []domain.Run     `json:"runs"`
	Sequence int64            `json:"sequence"`
	Epoch    string           `json:"epoch"`
}

func (s *Server) displayBot(node, id string) string {
	if node == s.Swarm.Node().ID {
		return id
	}
	return swarm.Key(node, id)
}
func (s *Server) needsSwarm(req SendRequest) bool {
	if req.OriginNodeID != "" && req.OriginNodeID != s.Swarm.Node().ID {
		return true
	}
	for _, seg := range req.Segments {
		if seg.Type == "file" && seg.NodeID != "" && seg.NodeID != s.Swarm.Node().ID {
			return true
		}
		if seg.Type == "bot" && (seg.NodeID != "" && seg.NodeID != s.Swarm.Node().ID || strings.Contains(seg.BotID, ":")) {
			return true
		}
	}
	content, _ := referenceContent(req)
	for _, match := range mentions.FindAllStringSubmatch(codeBlocks.ReplaceAllString(content, ""), -1) {
		if strings.Contains(match[1], ".") {
			return true
		}
	}
	return false
}

func (s *Server) swarmRoster(ctx context.Context) ([]swarmParticipant, error) {
	self := s.Swarm.Node().ID
	bots, err := s.Bots()
	if err != nil {
		return nil, err
	}
	out := []swarmParticipant{}
	var rosterMu sync.Mutex
	add := func(node string, bots []domain.Bot) {
		rosterMu.Lock()
		defer rosterMu.Unlock()
		for _, b := range bots {
			out = append(out, swarmParticipant{Ref: swarm.Ref{NodeID: node, ID: b.ID}, Handle: s.Swarm.Namespace(node) + "." + b.Handle, Name: b.Name, Revision: b.Revision})
		}
	}
	add(self, bots)
	if !s.Swarm.Enabled() {
		return out, nil
	}
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for _, peer := range s.Swarm.Directory() {
		if !peer.Authorized || peer.Blocked {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
				defer func() { <-limit }()
			case <-ctx.Done():
				return
			}
			var state struct {
				Bots []domain.Bot `json:"bots"`
			}
			child, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.Swarm.JSON(child, peer.Node.ID, "GET", "/api/v1/state", nil, &state)
			cancel()
			if err != nil {
				_ = s.Store.Get("swarm-state", peer.Node.ID, &state)
			}
			add(peer.Node.ID, state.Bots)
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out, nil
}

func (s *Server) resolveSwarm(ctx context.Context, req SendRequest) ([]swarmParticipant, []domain.Segment, error) {
	content, err := referenceContent(req)
	if err != nil {
		return nil, nil, err
	}
	roster, err := s.swarmRoster(ctx)
	if err != nil {
		return nil, nil, err
	}
	origin := req.OriginNodeID
	if origin == "" {
		origin = s.Swarm.Node().ID
	}
	byName := map[string]swarmParticipant{}
	byID := map[string]swarmParticipant{}
	for _, p := range roster {
		byID[p.key()] = p
		byName[p.Handle] = p
		if p.Ref.NodeID == origin {
			_, handle, _ := strings.Cut(p.Handle, ".")
			byName[handle] = p
		}
	}
	selected := []swarmParticipant{}
	structured := map[int]swarmParticipant{}
	boundaries := referenceBoundaries(req.Content)
	seen := map[string]bool{}
	add := func(p swarmParticipant) {
		if !seen[p.key()] {
			selected = append(selected, p)
			seen[p.key()] = true
		}
	}
	for _, seg := range req.Segments {
		if seg.Type != "bot" {
			continue
		}
		node, id := swarm.Split(seg.BotID)
		if seg.NodeID != "" {
			node = seg.NodeID
		}
		if node == "" {
			node = origin
		}
		p, ok := byID[swarm.Key(node, id)]
		if !ok {
			return nil, nil, fmt.Errorf("mentioned bot is unavailable: %s", seg.Text)
		}
		add(p)
		if seg.Range != nil {
			start, startOK := boundaries[seg.Range.Start]
			end, endOK := boundaries[seg.Range.End]
			if !startOK || !endOK || end <= start || content[start:end] != seg.Text || !strings.HasPrefix(seg.Text, "@") {
				return nil, nil, errors.New("bot reference no longer matches its message location")
			}
			structured[start] = p
		}
	}
	segments := []domain.Segment{}
	last := 0
	masked := codeBlocks.ReplaceAllStringFunc(content, func(v string) string { return strings.Repeat(" ", len(v)) })
	for _, match := range mentions.FindAllStringSubmatchIndex(masked, -1) {
		name := strings.ToLower(content[match[2]:match[3]])
		p, ok := byName[name]
		if saved, exists := structured[match[2]-1]; exists {
			p, ok = saved, true
		}
		if !ok {
			return nil, nil, fmt.Errorf("unknown bot @%s", name)
		}
		add(p)
		start, end := match[2]-1, match[3]
		if start > last {
			segments = append(segments, domain.Segment{Type: "text", Text: req.Content[last:start]})
		}
		segments = append(segments, domain.Segment{Type: "bot", BotID: s.displayBot(p.Ref.NodeID, p.Ref.ID), NodeID: p.Ref.NodeID, Text: req.Content[start:end], Range: &domain.SegmentRange{Start: utf16Offset(req.Content[:start]), End: utf16Offset(req.Content[:end])}})
		last = end
	}
	if last < len(req.Content) {
		segments = append(segments, domain.Segment{Type: "text", Text: req.Content[last:]})
	}
	for _, seg := range req.Segments {
		if seg.Type == "file" {
			if seg.NodeID == "" {
				seg.NodeID = origin
			}
			segments = append(segments, seg)
		}
	}
	if len(selected) == 0 {
		p, ok := byID[swarm.Key(origin, "echo")]
		if !ok {
			return nil, nil, errors.New("entry instance Echo is unavailable")
		}
		add(p)
	}
	return selected, segments, nil
}

func (s *Server) defaultGeneration() domain.Generation {
	entries, _ := s.Store.List("endpoints")
	g := domain.Generation{Temperature: .6}
	if len(entries) > 0 {
		var ep domain.Endpoint
		_ = json.Unmarshal(entries[0], &ep)
		g.EndpointID, g.Model = ep.ID, ep.Model
		if ep.Temperature != nil {
			g.Temperature = *ep.Temperature
		}
	}
	return g
}

func requestFingerprint(req workerRequest) string {
	participants := []string{}
	for _, p := range req.Participants {
		participants = append(participants, fmt.Sprintf("%s:%d", p.key(), p.Revision))
	}
	sort.Strings(participants)
	files := []string{}
	for _, seg := range req.Message.Segments {
		if seg.Type == "file" {
			files = append(files, swarm.Key(seg.NodeID, seg.Path))
		}
	}
	sort.Strings(files)
	data, _ := json.Marshal(struct {
		Home, Chat, Request, Content string
		Participants, Files          []string
	}{req.Home, req.Chat.ID, req.RequestID, req.Message.Content, participants, files})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (s *Server) sendSwarm(chatID string, req SendRequest) (*dispatch, error) {
	if !s.Swarm.Enabled() {
		return nil, errors.New("enable Swarm before addressing another instance")
	}
	if s.ctx.Err() != nil {
		return nil, s.ctx.Err()
	}
	if len(req.RequestID) > 128 {
		return nil, errors.New("request id exceeds 128 characters")
	}
	if prior := s.Store.Accepted(chatID, req.RequestID); prior != "" {
		done := make(chan struct{})
		close(done)
		return &dispatch{id: prior, done: done}, nil
	}
	if strings.TrimSpace(req.Content) == "" && len(req.Attachments) == 0 {
		return nil, errors.New("message is empty")
	}
	if len(req.Content) > 1<<20 {
		return nil, errors.New("message is too large")
	}
	ctx, cancelCheck := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancelCheck()
	participants, segments, err := s.resolveSwarm(ctx, req)
	if err != nil {
		return nil, err
	}
	c, err := s.Store.Chat(chatID)
	if err != nil {
		return nil, err
	}
	attachments, err := s.resolveAttachments(chatID, req.Attachments)
	if err != nil {
		return nil, err
	}
	for _, seg := range segments {
		if seg.Type == "file" && !slices.ContainsFunc(participants, func(p swarmParticipant) bool { return p.Ref.NodeID == seg.NodeID }) {
			return nil, errors.New("file reference requires a mentioned bot on its owning machine")
		}
	}
	if req.RequestID == "" {
		req.RequestID = domain.ID()
	}
	self := s.Swarm.Node().ID
	if req.OriginNodeID == "" {
		req.OriginNodeID = self
	}
	id := domain.ID()
	ids := []string{}
	for _, p := range participants {
		ids = append(ids, p.key())
	}
	runCtx, cancel := context.WithCancel(s.ctx)
	group := coordinator.New(ids)
	d := &dispatch{id: id, chatID: chatID, group: group, ctx: runCtx, cancel: cancel, done: make(chan struct{}), wake: map[string]chan struct{}{}, active: map[string]bool{}, admitted: map[string]chan struct{}{}}
	f := &swarmDispatch{ID: id, ChatID: chatID, Origin: req.OriginNodeID, RequestID: req.RequestID, Participants: participants, Hosts: map[string]workerRequest{}, Status: "queued", group: group, d: d}
	d.federation = f
	message := domain.Message{ID: domain.ID(), ChatID: chatID, Author: "You", Role: "user", Content: req.Content, Segments: segments, Attachments: attachments, Status: "completed", CreatedAt: domain.Now()}
	// Local history reads only paths which this host independently validated.
	for i := range message.Segments {
		seg := &message.Segments[i]
		if seg.Type != "file" {
			continue
		}
		seg.AllowedBots = nil
		if seg.NodeID != self {
			continue
		}
		for _, p := range participants {
			if p.Ref.NodeID == self {
				bot, _ := s.Bot(p.Ref.ID)
				if kind, e := s.Files.ReferenceKind(bot, seg.Path); e == nil {
					seg.AllowedBots = append(seg.AllowedBots, p.Ref.ID)
					seg.Kind = kind
				}
			}
		}
	}
	if c.Bots == nil {
		c.Bots = map[string]string{}
	}
	if c.GenerationByNode == nil {
		c.GenerationByNode = map[string]domain.Generation{}
	}
	for node, g := range req.GenerationByNode {
		c.GenerationByNode[node] = g
	}
	if req.Generation.EndpointID != "" {
		c.Generation = req.Generation
	}
	if c.Generation.EndpointID == "" {
		c.Generation = s.defaultGeneration()
	}
	c.OwnerNodeID = self
	c.UpdatedAt = domain.Now()
	if c.Title == "New chat" || c.Title == "" {
		c.Title = truncate(req.Content, 70)
	}
	runs := []domain.Run{}
	for _, p := range participants {
		key := s.displayBot(p.Ref.NodeID, p.Ref.ID)
		message.Recipients = append(message.Recipients, key)
		if key != "echo" {
			c.Bots[key] = p.Handle
		}
		g := c.GenerationByNode[p.Ref.NodeID]
		if p.Ref.NodeID == self && g.EndpointID == "" {
			g = c.Generation
		}
		run := domain.Run{ID: domain.ID(), ChatID: chatID, DispatchID: id, BotID: key, Status: "queued", Generation: g, CreatedAt: domain.Now()}
		runs = append(runs, run)
		host := f.Hosts[p.Ref.NodeID]
		if host.ID == "" {
			host = workerRequest{ID: id, Home: self, Origin: req.OriginNodeID, Token: domain.ID() + domain.ID(), Chat: c, Participants: participants, RequestID: req.RequestID, Status: "prepared", CreatedAt: domain.Now(), Permit: req.Permits[p.Ref.NodeID]}
		}
		run.BotID = p.Ref.ID
		host.Runs = append(host.Runs, run)
		f.Hosts[p.Ref.NodeID] = host
		d.active[p.key()] = true
	}
	for node, host := range f.Hosts {
		host.Chat = c
		host.Message = message
		f.Hosts[node] = host
		if err = s.validateWorkerRemote(ctx, node, host); err != nil {
			cancel()
			return nil, err
		}
	}
	s.mu.Lock()
	if s.closingChats[chatID] > 0 {
		s.mu.Unlock()
		cancel()
		return nil, errors.New("chat is stopping")
	}
	for _, active := range s.dispatches {
		if active.chatID == chatID {
			s.mu.Unlock()
			cancel()
			return nil, errors.New("this chat is still running; stop it or wait before sending")
		}
	}
	if err = s.Store.AcceptWithRecord(c, message, runs, req.RequestID, id, "swarm-dispatches", id, f); err != nil {
		s.mu.Unlock()
		cancel()
		return nil, err
	}
	s.dispatches[id] = d
	s.emitLocked("message", chatID, "", "", message)
	for _, run := range runs {
		s.emitLocked("run", chatID, run.ID, run.BotID, run)
	}
	s.mu.Unlock()
	group.OnSend = func(request coordinator.Request) {
		s.emit("handoff", chatID, "", request.From, request)
		f.mu.Lock()
		d.active[request.To] = true
		f.mu.Unlock()
		node, _ := swarm.Split(request.To)
		go s.workerControl(f, node, "wake", request.To)
	}
	go s.runSwarm(f)
	s.emit("changed", chatID, "", "", nil)
	return d, nil
}

func (s *Server) validateWorkerRemote(ctx context.Context, node string, req workerRequest) error {
	if node == s.Swarm.Node().ID {
		return s.validateWorker(req)
	}
	return s.Swarm.JSON(ctx, node, "POST", "/api/v1/swarm/workers/validate", req, nil)
}

func (s *Server) validateWorker(req workerRequest) error {
	if req.Home == "" || req.ID == "" || len(req.Token) != 64 {
		return errors.New("invalid worker request")
	}
	if req.Origin != s.Swarm.Node().ID && !s.Swarm.Authorized(req.Origin) {
		return errors.New("the entry instance must authenticate this machine before dispatching its bots")
	}
	if req.Origin != req.Home && req.Origin != s.Swarm.Node().ID {
		if err := s.Swarm.VerifyProof(req.Origin, req.Permit, requestFingerprint(req)); err != nil {
			return err
		}
	}
	for i := range req.Runs {
		run := &req.Runs[i]
		bot, err := s.Bot(run.BotID)
		if err != nil {
			return err
		}
		found := false
		for _, p := range req.Participants {
			if p.Ref.NodeID == s.Swarm.Node().ID && p.Ref.ID == bot.ID && p.Revision == bot.Revision {
				found = true
			}
		}
		if !found {
			return errors.New("bot scope changed; retry with the current bot settings")
		}
		g := run.Generation
		if g.EndpointID == "" {
			g = s.defaultGeneration()
		}
		var ep domain.Endpoint
		if err = s.Store.Get("endpoints", g.EndpointID, &ep); err != nil {
			return fmt.Errorf("select a model on %s", s.Swarm.Node().Name)
		}
		if err = model.ValidateEndpoint(ep); err != nil {
			return err
		}
		if g.Model == "" {
			g.Model = ep.Model
		}
		if g.Model == "" {
			return errors.New("select a model name")
		}
		if ep.Provider != "" {
			if _, err = s.Plugins.Provider(bot, ep.Provider); err != nil {
				return err
			}
		}
		if g.Temperature < 0 || g.Temperature > 2 {
			return errors.New("temperature must be between 0 and 2")
		}
	}
	for _, seg := range req.Message.Segments {
		if seg.Type != "file" || seg.NodeID != s.Swarm.Node().ID {
			continue
		}
		allowed := false
		for _, run := range req.Runs {
			bot, _ := s.Bot(run.BotID)
			if _, err := s.Files.ReferenceKind(bot, seg.Path); err == nil {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("file reference is unavailable to the selected bots on %s: %s", s.Swarm.Node().Name, seg.Path)
		}
	}
	return nil
}

func (s *Server) runSwarm(f *swarmDispatch) {
	d := f.d
	nodes := []string{}
	for node := range f.Hosts {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	defer func() {
		for _, node := range nodes {
			action := "finish"
			f.mu.Lock()
			if f.Status == "cancelled" {
				action = "stop"
			}
			f.mu.Unlock()
			if s.ctx.Err() == nil {
				s.workerControl(f, node, action, "")
			}
		}
		f.mu.Lock()
		if s.ctx.Err() != nil && f.Status != "completed" && f.Status != "cancelled" {
			f.Status = "interrupted"
		}
		_ = s.Store.Put("swarm-dispatches", f.ID, f)
		f.mu.Unlock()
		d.cancel()
		d.group.Close()
		s.mu.Lock()
		delete(s.dispatches, d.id)
		s.mu.Unlock()
		close(d.done)
		s.emit("changed", d.chatID, "", "", nil)
	}()
	for d.ctx.Err() == nil {
		prepared := []string{}
		ready := true
		for _, node := range nodes {
			ctx, cancel := context.WithTimeout(d.ctx, 8*time.Second)
			err := s.prepareWorkerRemote(ctx, node, f.Hosts[node])
			cancel()
			if err != nil {
				if !strings.Contains(err.Error(), "worker bots are busy") {
					s.failSwarmHost(f, node, err)
					nodes = slices.DeleteFunc(nodes, func(id string) bool { return id == node })
				}
				ready = false
				break
			}
			prepared = append(prepared, node)
		}
		if ready {
			break
		}
		for _, node := range prepared {
			s.workerControl(f, node, "release", "")
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(400 * time.Millisecond):
		}
	}
	if d.ctx.Err() != nil {
		return
	}
	f.mu.Lock()
	f.Status = "running"
	_ = s.Store.Put("swarm-dispatches", f.ID, f)
	f.mu.Unlock()
	for _, node := range nodes {
		if err := s.startWorkerRemote(d.ctx, node, f.Hosts[node]); err != nil {
			s.failSwarmHost(f, node, err)
		}
	}
	var wg sync.WaitGroup
	for _, node := range nodes {
		if node != s.Swarm.Node().ID {
			wg.Add(1)
			go func(node string) { defer wg.Done(); s.followWorker(d.ctx, f, node) }(node)
		}
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-d.ctx.Done():
			wg.Wait()
			return
		case <-tick.C:
			f.mu.Lock()
			idle := true
			for _, active := range d.active {
				if active {
					idle = false
					break
				}
			}
			if idle && f.Status != "cancelled" {
				f.Status = "completed"
			}
			f.mu.Unlock()
			if idle {
				d.cancel()
				wg.Wait()
				return
			}
		}
	}
}

func (s *Server) failSwarmHost(f *swarmDispatch, node string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Participants {
		if p.Ref.NodeID != node {
			continue
		}
		f.group.Fail(p.key(), err)
		f.d.active[p.key()] = false
	}
	for _, run := range f.Hosts[node].Runs {
		run.ChatID = f.ChatID
		run.BotID = s.displayBot(node, run.BotID)
		run.Status = "failed"
		run.Error = err.Error()
		run.UpdatedAt = domain.Now()
		_ = s.Store.Put("runs", run.ID, run)
		s.emit("run", run.ChatID, run.ID, run.BotID, run)
	}
}

func (s *Server) workerControl(f *swarmDispatch, node, action, bot string) {
	req := f.Hosts[node]
	input := workerControl{Home: req.Home, ID: req.ID, Token: req.Token, Action: action, Bot: bot}
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	if node == s.Swarm.Node().ID {
		_, _ = s.controlWorker(input)
	} else {
		if err := s.Swarm.JSON(ctx, node, "POST", "/api/v1/swarm/workers/control", input, nil); err != nil && (action == "delete" || action == "stop") {
			_ = s.Store.Put("swarm-cancellations", swarm.Key(node, f.ID), pendingSwarmControl{Node: node, Request: input})
		}
	}
}

func (s *Server) followWorker(ctx context.Context, f *swarmDispatch, node string) {
	req := f.Hosts[node]
	input := workerControl{Home: req.Home, ID: req.ID, Token: req.Token}
	body, _ := json.Marshal(input)
	failures := 0
	for ctx.Err() == nil {
		response, err := s.Swarm.RoundTrip(ctx, node, "POST", "/api/v1/swarm/workers/stream", bytes.NewReader(body))
		if err == nil && response.StatusCode == 200 {
			decoder := json.NewDecoder(response.Body)
			for ctx.Err() == nil {
				var snapshot workerSnapshot
				if err = decoder.Decode(&snapshot); err != nil {
					break
				}
				failures = 0
				s.mergeWorker(f, node, snapshot)
			}
		}
		if response != nil {
			response.Body.Close()
		}
		if ctx.Err() != nil {
			break
		}
		failures++
		if failures >= 3 {
			s.failSwarmHost(f, node, swarm.ErrUnavailable)
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(failures) * time.Second):
		}
	}
	// The idle acknowledgement may beat the final streamed snapshot.
	child, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	var snapshot workerSnapshot
	if s.Swarm.JSON(child, node, "POST", "/api/v1/swarm/workers/snapshot", input, &snapshot) == nil {
		s.mergeWorker(f, node, snapshot)
	}
}

func (s *Server) mergeWorker(f *swarmDispatch, node string, snapshot workerSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replicas == nil {
		f.replicas = map[string]string{}
	}
	messages := []domain.Message{}
	runs := []domain.Run{}
	signatures := map[string]string{}
	changed := func(id string, value any) bool {
		data, _ := json.Marshal(value)
		hash := sha256.Sum256(data)
		signature := hex.EncodeToString(hash[:])
		signatures[id] = signature
		return f.replicas[id] != signature
	}

	if _, err := s.Store.Chat(f.ChatID); err != nil {
		return
	}
	for _, m := range snapshot.Messages {
		if m.BotID == "" || !slices.ContainsFunc(f.Participants, func(p swarmParticipant) bool { return p.Ref.NodeID == node && p.Ref.ID == m.BotID }) {
			continue
		}
		m.ChatID = f.ChatID
		m.ID = swarm.Key(node, m.ID)
		m.NodeID = node
		m.BotID = s.displayBot(node, m.BotID)
		for i, id := range m.Recipients {
			m.Recipients[i] = s.displayBot(node, id)
		}
		for i := range m.Attachments {
			m.Attachments[i].NodeID = node
			m.Attachments[i].OriginChatID = m.Attachments[i].ChatID
		}
		if changed("message:"+m.ID, m) {
			messages = append(messages, m)
		}
	}
	for _, run := range snapshot.Runs {
		if !slices.ContainsFunc(f.Hosts[node].Runs, func(r domain.Run) bool { return r.ID == run.ID }) {
			continue
		}
		run.ChatID = f.ChatID
		run.BotID = s.displayBot(node, run.BotID)
		if changed("run:"+run.ID, run) {
			runs = append(runs, run)
		}
	}
	if err := s.Store.MergeReplica(f.ChatID, swarm.Key(node, f.ID), snapshot.Epoch, snapshot.Sequence, messages, runs); err != nil {
		return
	}
	if f.d != nil && snapshot.Status != "running" && snapshot.Status != "prepared" {
		for _, p := range f.Participants {
			if p.Ref.NodeID == node {
				f.d.active[p.key()] = false
			}
		}
	}
	for id, signature := range signatures {
		f.replicas[id] = signature
	}
	for _, m := range messages {
		s.emit("message", m.ChatID, m.RunID, m.BotID, m)
	}
	for _, run := range runs {
		s.emit("run", run.ChatID, run.ID, run.BotID, run)
	}
}
