package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/brent/echo/internal/coordinator"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

// Once a concrete turn fails final validation, surface that failure on its
// queued input so the user can inspect it and retry.
type groupAssignmentError struct{ error }
type pendingGroupRecord struct {
	RequestID string      `json:"requestId"`
	ChatID    string      `json:"chatId"`
	MessageID string      `json:"messageId"`
	Groups    []swarm.Ref `json:"groups"`
}
type groupTurn struct {
	id      string
	chat    domain.Chat
	message domain.Message
	groups  []domain.Segment // sorted by owner and group ID for ordered acquisition
	fixed   []swarmParticipant
	files   []domain.Segment
	pending *dispatch
}

func oneGroupMentions(req SendRequest) bool {
	for _, g := range req.groupMentions {
		if g.DeliveryMode == "one" {
			return true
		}
	}
	return false
}
func refOf(m domain.BotGroupMember) swarm.Ref { return swarm.Ref{NodeID: m.NodeID, ID: m.BotID} }
func groupKey(g domain.Segment) string        { return swarm.Key(g.NodeID, g.GroupID) }
func (s *Server) previousTurn(chat string, req SendRequest) string {
	id := s.Store.Accepted(chat, req.RequestID)
	if req.routing != nil && req.routing.pending != nil && req.routing.id == id {
		return ""
	}
	return id
}
func (req SendRequest) promotes(d *dispatch) bool {
	return req.routing != nil && req.routing.pending == d && d.ctx.Err() == nil
}
func (s *Server) acceptTurn(req SendRequest, c domain.Chat, m domain.Message, runs []domain.Run, id string, f *swarmDispatch) error {
	records := map[string]map[string]any{}
	if f != nil {
		records["swarm-dispatches"] = map[string]any{id: f}
	}
	assigning := req.routing != nil && req.routing.pending != nil
	if assigning && (req.routing.pending.ctx.Err() != nil || s.dispatches[id] != req.routing.pending) {
		return errors.New("queued group turn was stopped")
	}
	if req.routing != nil {
		commits := map[string]any{}
		for _, g := range req.groupMentions {
			if g.DeliveryMode == "one" {
				commits[swarm.Key(id, groupKey(g))] = groupCommit{Owner: g.NodeID, Query: rotationQuery{Group: g.GroupID, Turn: id, Action: "commit", Winner: refOf(g.Members[0])}}
			}
		}
		records["group-commits"] = commits
	}
	return s.storeTurn(req, c, m, runs, id, records)
}
func (s *Server) checkOnePeer(node string) error {
	if !s.oneGroupCapable(node) {
		return fmt.Errorf("update Echo on %s to use One available bot delivery", s.Swarm.Namespace(node))
	}
	return nil
}
func (s *Server) routeGroupTurn(chatID string, req SendRequest) (*dispatch, error) {
	if req.RequestID == "" {
		req.RequestID = domain.ID()
	}
	if len(req.RequestID) > 128 {
		return nil, errors.New("request id exceeds 128 characters")
	}
	if req.OriginNodeID == "" {
		req.OriginNodeID = s.Swarm.Node().ID
	}
	if err := s.checkOnePeer(req.OriginNodeID); err != nil {
		return nil, err
	}
	c, err := s.chatForSend(chatID, req)
	if err != nil {
		return nil, err
	}
	if req.Generation.EndpointID == "" {
		req.Generation = c.Generation
	}
	if generation := req.GenerationByNode[s.Swarm.Node().ID]; generation.EndpointID != "" {
		req.Generation = generation
	} else if req.Generation.EndpointID == "" {
		req.Generation = s.defaultGeneration()
	}
	if req.Generation.EndpointID != "" {
		req.GenerationByNode = maps.Clone(req.GenerationByNode)
		if req.GenerationByNode == nil {
			req.GenerationByNode = map[string]domain.Generation{}
		}
		req.GenerationByNode[s.Swarm.Node().ID] = req.Generation
	}
	attachments, err := s.sendAttachments(chatID, req)
	if err != nil {
		return nil, err
	}
	fixed, segments, err := s.resolveSwarm(s.ctx, req)
	if err != nil {
		return nil, err
	}
	// Bind typed direct mentions as well. A handle may be reused while this
	// turn is waiting, but the explicitly addressed bot must keep its identity.
	originalSegments := req.Segments
	req.Segments = slices.Clone(segments)
	boundaries := referenceBoundaries(req.Content)
	for i, seg := range segments {
		if seg.Type == "file" {
			// Candidate grants are only a preview. Record grants for the chosen
			// recipients when assignment commits.
			req.Segments[i].AllowedBots = nil
		}
		if seg.Type == "bot" && seg.Range != nil {
			req.botMentions[boundaries[seg.Range.Start]] = seg
		}
	}
	for _, seg := range originalSegments {
		if seg.Type == "bot" && seg.Range == nil {
			req.Segments = append(req.Segments, seg)
		}
	}
	t := &groupTurn{id: domain.ID(), chat: c, fixed: fixed}
	t.message = req.userMessage(chatID, "queued", slices.Clone(req.Segments), attachments)
	seen := map[string]bool{}
	for _, seg := range segments {
		if seg.Type == "file" {
			t.files = append(t.files, seg)
		}
		if seg.Type != "group" || seg.DeliveryMode != "one" || seen[groupKey(seg)] {
			continue
		}
		if err := s.checkOnePeer(seg.NodeID); err != nil {
			return nil, err
		}
		seen[groupKey(seg)] = true
		t.groups = append(t.groups, seg)
	}
	sort.Slice(t.groups, func(i, j int) bool { return groupKey(t.groups[i]) < groupKey(t.groups[j]) })
	req.routing = t
	d, err := s.tryGroupTurn(s.ctx, req)
	if d != nil || err != nil {
		return d, err
	}
	d, err = s.queueGroupTurn(req)
	if err != nil || d.id != t.id {
		s.finishRotations(t, nil)
		return d, err
	}
	req.newChat = nil // future assignment uses the committed chat and the queue's own context
	go s.waitGroupTurn(req)
	return d, nil
}

func (s *Server) queueGroupTurn(req SendRequest) (*dispatch, error) {
	t := req.routing
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior := s.Store.Accepted(t.chat.ID, req.RequestID); prior != "" {
		return acceptedDispatch(prior), nil
	}
	if err := s.idleChatGuard(t.chat.ID); err != nil {
		return nil, err
	}
	if err := s.checkRerunSnapshot(t.chat.ID, req); err != nil {
		return nil, err
	}
	c, err := s.chatForSend(t.chat.ID, req)
	if err != nil {
		return nil, err
	}
	if c.Projection {
		return nil, errors.New("edit messages on the chat's home machine")
	}
	if c.Title == "" || c.Title == "New chat" {
		c.Title = truncate(req.Content, 70)
	}
	c.UpdatedAt = domain.Now()
	c.Generation = req.Generation
	if c.GenerationByNode == nil {
		c.GenerationByNode = map[string]domain.Generation{}
	}
	maps.Copy(c.GenerationByNode, req.GenerationByNode)
	record := pendingGroupRecord{RequestID: req.RequestID, ChatID: c.ID, MessageID: t.message.ID}
	for _, g := range t.groups {
		record.Groups = append(record.Groups, swarm.Ref{NodeID: g.NodeID, ID: g.GroupID})
	}
	if err = s.storeTurn(req, c, t.message, nil, t.id, map[string]map[string]any{"pending-groups": {t.id: record}}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	d := &dispatch{id: t.id, chatID: c.ID, group: coordinator.New(nil), ctx: ctx, cancel: cancel, done: make(chan struct{}), waitingGroup: true}
	t.pending = d
	s.dispatches[t.id] = d
	s.emitLocked("message", c.ID, "", "", t.message)
	s.emitLocked("changed", c.ID, "", "", map[string]bool{"messagesChanged": true})
	return d, nil
}
func (s *Server) waitGroupTurn(req SendRequest) {
	t := req.routing
	d := t.pending
	defer close(d.done)
	defer d.cancel()
	var failed *groupAssignmentError
	for d.ctx.Err() == nil {
		wake := s.groupWakeChannel()
		actual, err := s.tryGroupTurn(d.ctx, req)
		if actual != nil {
			<-actual.done
			d.mu.Lock()
			d.err = actual.err
			d.mu.Unlock()
			return
		}
		if errors.As(err, &failed) || d.ctx.Err() != nil {
			break
		}
		select {
		case <-d.ctx.Done():
		case <-wake:
		case <-time.After(400 * time.Millisecond): // remote slot, roster or limit changed
		}
	}
	s.finishRotations(t, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispatches[d.id] == d {
		delete(s.dispatches, d.id)
		t.message.Status = "cancelled"
		if failed != nil && d.ctx.Err() == nil {
			t.message.Status, t.message.DeliveryError = "failed", failed.Error()
			d.err = failed
		}
		if s.ctx.Err() != nil {
			t.message.Status = "interrupted"
		}
		if err := s.Store.FinishMessage(t.message); err == nil {
			_ = s.Store.Delete("pending-groups", t.id)
		}
		s.emitLocked("message", d.chatID, "", "", t.message)
		s.emitLocked("changed", d.chatID, "", "", nil)
	}
}

type groupChoice struct {
	member domain.BotGroupMember
	slot   slotInfo
}
type groupPool struct {
	group   domain.Segment
	choices []groupChoice
}

func (s *Server) slotQueryFor(req SendRequest, ref swarm.Ref) slotQuery {
	t := req.routing
	g := t.chat.GenerationByNode[ref.NodeID]
	if custom, ok := req.GenerationByNode[ref.NodeID]; ok {
		g = custom
	}
	if ref.NodeID == s.Swarm.Node().ID && g.EndpointID == "" {
		g = req.Generation
	}
	q := slotQuery{Queued: t.pending != nil, Action: "probe", Turn: t.id, Bot: ref.ID, Origin: req.OriginNodeID, Generation: g}
	for _, f := range t.files {
		if f.NodeID == ref.NodeID {
			q.Paths = append(q.Paths, f.Path)
		}
	}
	return q
}
func (s *Server) finishRotations(t *groupTurn, winners map[string]domain.BotGroupMember) {
	for _, g := range t.groups {
		q := rotationQuery{Action: "leave", Group: g.GroupID, Turn: t.id}
		if m, ok := winners[groupKey(g)]; ok {
			q.Action = "commit"
			q.Winner = refOf(m)
		}
		id := swarm.Key(t.id, groupKey(g))
		_, err := s.rotation(s.ctx, g.NodeID, q)
		if err == nil && q.Action == "commit" {
			_ = s.Store.Delete("group-commits", id)
		} else if err != nil && t.pending != nil && q.Action == "leave" {
			_ = s.Store.Put("group-commits", id, groupCommit{Owner: g.NodeID, Query: q})
		}
	}
}

// Choose in rotation order, preferring idle members. A mixed turn's final
// union must cover each file. Backtrack only when the preferred combination
// cannot satisfy those file grants.
func groupWinners(pools []groupPool, fixed []swarmParticipant, slots map[swarm.Ref]slotInfo, files []domain.Segment, freeOnly bool) (map[string]domain.BotGroupMember, bool) {
	selected := map[string]domain.BotGroupMember{}
	covers := func(ref swarm.Ref, f domain.Segment) bool {
		return ref.NodeID == f.NodeID && slices.Contains(slots[ref].Paths, f.Path)
	}
	if freeOnly && slices.ContainsFunc(pools, func(pool groupPool) bool {
		return !slices.ContainsFunc(pool.choices, func(c groupChoice) bool { return c.slot.Free })
	}) {
		return nil, false
	}
	var choose func(int) bool
	choose = func(at int) bool {
		for _, f := range files {
			covered := slices.ContainsFunc(fixed, func(p swarmParticipant) bool { return covers(p.Ref, f) })
			for _, m := range selected {
				if covers(refOf(m), f) {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
			possible := false
			for _, pool := range pools[at:] {
				if slices.ContainsFunc(pool.choices, func(c groupChoice) bool { return (!freeOnly || c.slot.Free) && covers(refOf(c.member), f) }) {
					possible = true
					break
				}
			}
			if !possible {
				return false
			}
		}
		if at == len(pools) {
			return true
		}
		p := pools[at]
		k := groupKey(p.group)
		for _, c := range p.choices {
			if freeOnly && !c.slot.Free {
				continue
			}
			selected[k] = c.member
			if choose(at + 1) {
				return true
			}
		}
		delete(selected, k)
		return false
	}
	return selected, choose(0)
}

func (s *Server) tryGroupTurn(ctx context.Context, req SendRequest) (actual *dispatch, err error) {
	t := req.routing
	defer func() {
		if err != nil && t.pending == nil {
			s.finishRotations(t, nil)
		}
	}()
	slots := map[swarm.Ref]slotInfo{}
	problems := map[swarm.Ref]error{}
	probe := func(ref swarm.Ref) (slotInfo, error) {
		if info, ok := slots[ref]; ok {
			return info, nil
		}
		if problem := problems[ref]; problem != nil {
			return slotInfo{}, problem
		}
		if !s.swarmAvailable(ref.NodeID) {
			return slotInfo{}, s.offlineMachine(ref.NodeID, ref.ID)
		}
		info, err := s.querySlot(ctx, ref.NodeID, s.slotQueryFor(req, ref))
		if err != nil {
			problems[ref] = err
			return info, err
		}
		slots[ref] = info
		return info, nil
	}
	fixedFree := true
	for _, p := range t.fixed {
		if err := s.checkOnePeer(p.Ref.NodeID); err != nil {
			return nil, err
		}
		info, err := probe(p.Ref)
		if err != nil {
			return nil, fmt.Errorf("@%s: %w", p.Handle, err)
		}
		fixedFree = fixedFree && info.Free
	}
	head := true
	pools := []groupPool{}
	for _, g := range t.groups {
		var last swarm.Ref
		if head {
			state, err := s.rotation(ctx, g.NodeID, rotationQuery{Action: "wait", Group: g.GroupID, Turn: t.id})
			if err != nil {
				return nil, err
			}
			head, last = state.Head, state.Last
		}
		start := slices.IndexFunc(g.Candidates, func(m domain.BotGroupMember) bool { return refOf(m) == last }) + 1
		p := groupPool{group: g}
		var reasons []string
		for i := range g.Candidates {
			m := g.Candidates[(start+i)%len(g.Candidates)]
			if s.swarmAvailable(m.NodeID) {
				if err := s.checkOnePeer(m.NodeID); err != nil {
					return nil, err
				}
			}
			info, err := probe(refOf(m))
			if err != nil {
				reasons = append(reasons, fmt.Sprintf("@%s: %s", m.Handle, err))
				continue
			}
			m.Handle = info.Handle
			if m.NodeID != req.OriginNodeID {
				m.Handle = s.Swarm.Namespace(m.NodeID) + "." + m.Handle
			}
			p.choices = append(p.choices, groupChoice{m, info})
		}
		if len(p.choices) == 0 {
			return nil, fmt.Errorf("no eligible bot in %s (%s)", g.Text, strings.Join(reasons, ", "))
		}
		sort.SliceStable(p.choices, func(i, j int) bool { return p.choices[i].slot.Active == 0 && p.choices[j].slot.Active != 0 })
		pools = append(pools, p)
	}
	if _, ok := groupWinners(pools, t.fixed, slots, t.files, false); !ok {
		return nil, errors.New("no eligible group assignment can access every referenced file")
	}
	if !head {
		return nil, nil
	}
	winners, ok := groupWinners(pools, t.fixed, slots, t.files, true)
	if !ok {
		return nil, nil
	}
	req.groupMentions = maps.Clone(req.groupMentions)
	for start, g := range req.groupMentions {
		if m, ok := winners[groupKey(g)]; ok {
			g.Members = []domain.BotGroupMember{m}
			req.groupMentions[start] = g
		}
	}
	needed := map[swarm.Ref]bool{}
	// Local dispatch admits each explicit recipient independently. Federated
	// dispatch prepares its hosts together and needs every slot to proceed.
	if s.needsSwarm(req) {
		if !fixedFree {
			return nil, nil
		}
		for _, p := range t.fixed {
			needed[p.Ref] = true
		}
	}
	for _, m := range winners {
		needed[refOf(m)] = true
	}
	var claimed []swarm.Ref
	defer func() {
		if actual != nil && actual.id == t.id {
			return
		} // dispatch adopts the reservations
		for _, ref := range claimed {
			_, _ = s.querySlot(s.ctx, ref.NodeID, slotQuery{Turn: t.id, Bot: ref.ID, Action: "release"})
		}
	}()
	for ref := range needed {
		q := s.slotQueryFor(req, ref)
		q.Action, q.Revision = "claim", slots[ref].Revision
		info, err := s.querySlot(ctx, ref.NodeID, q)
		if err != nil {
			return nil, err
		}
		if !info.Free {
			return nil, nil
		}
		claimed = append(claimed, ref)
		if !slices.Equal(info.Paths, slots[ref].Paths) {
			return nil, errors.New("file permissions changed during assignment")
		}
	}
	actual, err = s.sendResolved(t.chat.ID, req)
	if err != nil && t.pending != nil {
		return nil, &groupAssignmentError{err}
	}
	if actual != nil {
		if actual.id != t.id {
			winners = nil
		}
		s.finishRotations(t, winners)
	}
	return actual, err
}
