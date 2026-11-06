package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/swarm"
)

func makeOneGroup(t *testing.T, s *Server, members ...domain.BotGroupMember) domain.BotGroup {
	t.Helper()
	g, err := s.saveBotGroup(t.Context(), "", domain.BotGroup{Handle: "pool", DeliveryMode: "one", Members: members})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func TestOneGroupRotationQueueAndStop(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	cap := 2
	b, err := s.saveBot(domain.Bot{Handle: "second", MaxParallelPrompts: &cap})
	if err != nil {
		t.Fatal(err)
	}
	group := makeOneGroup(t, s, domain.BotGroupMember{BotID: "echo"}, domain.BotGroupMember{BotID: b.ID})
	legacy := group
	legacy.DeliveryMode = ""
	saved, err := s.saveBotGroup(t.Context(), group.ID, legacy)
	if err != nil || saved.DeliveryMode != "one" {
		t.Fatal(saved, err)
	}
	a := sendWork(t, s, g, "@pool first")
	first := nextInvocation(t, started, "echo", "@echo first")
	busy := sendWork(t, s, g, "@pool second")
	second := nextInvocation(t, started, "second", "@second second")
	mixed := sendWork(t, s, g, "@second @pool @pool third")
	third := nextInvocation(t, started, "second", "@second @second @second third")
	if runs := dispatchRuns(t, s, mixed); len(runs) != 1 {
		t.Fatal(runs)
	}
	q := sendWork(t, s, g, "@pool fourth")
	cancelled := sendWork(t, s, g, "@pool cancel")
	q2 := sendWork(t, s, g, "@pool fifth")
	original, _ := s.Store.Messages(q.chatID)
	if len(original) != 1 || original[0].Status != "queued" || len(original[0].Recipients) != 0 || len(dispatchRuns(t, s, q)) != 0 {
		t.Fatal(original)
	}
	if _, err := s.send(q.chatID, SendRequest{Content: "@echo not allowed", Generation: g}); err == nil {
		t.Fatal("second active turn")
	}
	duplicate, err := s.send(q.chatID, SendRequest{Content: "@pool fourth", RequestID: "request"})
	if err != nil || duplicate.id != q.id {
		t.Fatal(duplicate, err)
	}
	release := s.stopChat(cancelled.chatID)
	release()
	stopped, _ := s.Store.Messages(cancelled.chatID)
	if stopped[0].Status != "cancelled" || len(stopped[0].Recipients) != 0 {
		t.Fatal(stopped)
	}
	// Remove the second bot from the live group. Both queued turns still use
	// their original pool. Increasing its cap should immediately admit one.
	group.Members = group.Members[:1]
	if _, err = s.saveBotGroup(t.Context(), group.ID, group); err != nil {
		t.Fatal(err)
	}
	cap = 3
	b.MaxParallelPrompts = &cap
	if _, err = s.saveBot(b); err != nil {
		t.Fatal(err)
	}
	fourth := nextInvocation(t, started, "second", "fourth")
	noInvocation(t, started)
	m, _ := s.Store.Messages(q.chatID)
	if m[0].ID != original[0].ID || !slices.Equal(m[0].Recipients, []string{b.ID}) || len(m[0].Segments[0].Candidates) != 2 || len(m[0].Segments[0].Members) != 1 {
		t.Fatal(m)
	}
	first.finish <- "ok"
	waitDispatch(t, a)
	fifth := nextInvocation(t, started, "echo", "fifth")
	for _, call := range []modelInvocation{second, third, fourth, fifth} {
		call.finish <- "ok"
	}
	for _, d := range []*dispatch{busy, mixed, q, q2} {
		waitDispatch(t, d)
	}
	rerun, err := s.send(q.chatID, SendRequest{RequestID: "rerun", rerunMessageID: m[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	call := nextInvocation(t, started, "second", "fourth")
	call.finish <- "ok"
	waitDispatch(t, rerun)
	m, _ = s.Store.Messages(q.chatID)
	edit, err := s.send(q.chatID, SendRequest{RequestID: "edit", replaceMessageID: m[0].ID, Content: "@pool edited"})
	if err != nil {
		t.Fatal(err)
	}
	call = nextInvocation(t, started, "echo", "edited")
	call.finish <- "ok"
	waitDispatch(t, edit)
}
func TestOneGroupMixedTurnUsesDirectBotQueue(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	b, err := s.saveBot(domain.Bot{Handle: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	makeOneGroup(t, s, domain.BotGroupMember{BotID: b.ID})
	block := sendWork(t, s, g, "@echo direct")
	hold := nextInvocation(t, started, "echo", "direct")
	mixed := sendWork(t, s, g, "@echo @pool review")
	review := nextInvocation(t, started, "reviewer", "@echo @reviewer review")
	if mixed.waitingGroup || len(dispatchRuns(t, s, mixed)) != 2 {
		t.Fatal("mixed turn did not enter the normal per-bot queues")
	}
	hold.finish <- "done"
	waitDispatch(t, block)
	direct := nextInvocation(t, started, "echo", "review")
	direct.finish <- "done"
	review.finish <- "done"
	waitDispatch(t, mixed)
}

func TestOneGroupUnavailableModelKeepsDraft(t *testing.T) {
	s := testServer(t)
	g, _ := controlledModel(t, s)
	makeOneGroup(t, s, domain.BotGroupMember{BotID: "echo"})
	g.Model = "removed-model"
	c := chat(t, s, g)
	_, err := s.send(c.ID, SendRequest{Content: "@pool review", Generation: g})
	if err == nil || !strings.Contains(err.Error(), "removed-model") {
		t.Fatal(err)
	}
	ms, _ := s.Store.Messages(c.ID)
	if len(ms) != 0 {
		t.Fatal(ms)
	}
}

func TestSwarmGroupSkipsExternalAgentWithRestrictedAccess(t *testing.T) {
	entry, _ := swarmFixture(t, "desktop", remotePassword)
	worker, host := swarmFixture(t, "worker", remotePassword)
	node := connectSwarm(t, entry, host, remotePassword)
	g := attachModel(t, worker, func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) })
	cli, log := externalFixture(t, worker, "codex-cli")
	bot, err := worker.saveBot(domain.Bot{Handle: "cli", ModelOverride: &domain.BotModelOverride{EndpointID: cli.EndpointID}})
	if err != nil {
		t.Fatal(err)
	}
	// CLI is first in rotation, but only the HTTP bot can run for this peer.
	if err := worker.saveAccessPolicy(entry.Swarm.Node().ID, domain.SwarmPolicy{Permissions: map[string]bool{domain.AccessMachine: false}}); err != nil {
		t.Fatal(err)
	}
	group := makeOneGroup(t, worker, domain.BotGroupMember{BotID: bot.ID}, domain.BotGroupMember{BotID: "echo"})
	c := chatForSwarm(t, entry)
	req := SendRequest{Content: "@worker.pool hello", GenerationByNode: map[string]domain.Generation{node.ID: g}}
	d, err := entry.send(c.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	runs := dispatchRuns(t, entry, d)
	if len(runs) != 1 || runs[0].BotID != swarm.Key(node.ID, "echo") || runs[0].Status != "completed" {
		t.Fatalf("expected only the HTTP bot to complete, got %+v", runs)
	}

	for _, mode := range []string{"all", "one"} {
		group.DeliveryMode = mode
		if mode == "one" {
			group.Members = group.Members[:1]
		}
		if _, err := worker.saveBotGroup(t.Context(), group.ID, group); err != nil {
			t.Fatal(err)
		}
		c = chatForSwarm(t, entry)
		_, err = entry.send(c.ID, req)
		w := httptest.NewRecorder()
		respond(w, nil, err)
		var denial swarm.AccessDenied
		if json.Unmarshal(w.Body.Bytes(), &denial) != nil || w.Code != http.StatusForbidden || denial.Permission != "fullAccess" || !strings.Contains(denial.Reason, "@cli") || !strings.Contains(denial.Reason, "External fixture") {
			t.Fatalf("%s delivery: got %d %s", mode, w.Code, w.Body.String())
		}
		if ms, err := entry.Store.Messages(c.ID); err != nil || len(ms) != 0 {
			t.Fatalf("%s delivery accepted a denied turn: %v %v", mode, ms, err)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("restricted group invoked the external CLI: %v", err)
	}
}

func TestOneGroupFileEligibility(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.saveBot(domain.Bot{Handle: "blocked", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := s.saveBot(domain.Bot{Handle: "reader", Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	group := makeOneGroup(t, s, domain.BotGroupMember{BotID: blocked.ID}, domain.BotGroupMember{BotID: reader.ID})
	c := chat(t, s, g)
	req := SendRequest{Content: "@pool review", Generation: g, Segments: []domain.Segment{{Type: "file", Path: path}}}
	d, err := s.send(c.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	call := nextInvocation(t, started, "reader", "review")
	call.finish <- "ok"
	waitDispatch(t, d)
	group.Members = group.Members[:1]
	if _, err = s.saveBotGroup(t.Context(), group.ID, group); err != nil {
		t.Fatal(err)
	}
	c = chat(t, s, g)
	if _, err = s.send(c.ID, req); err == nil || !strings.Contains(err.Error(), "file") {
		t.Fatal(err)
	}
	ms, _ := s.Store.Messages(c.ID)
	if len(ms) != 0 {
		t.Fatal(ms)
	}
}
func TestOneGroupSwarmOwnerRotation(t *testing.T) {
	entry, _ := swarmFixture(t, "desktop", remotePassword)
	home, host := swarmFixture(t, "linux", remotePassword)
	node := connectSwarm(t, entry, host, remotePassword)
	reply := func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) }
	eg := attachModel(t, entry, reply)
	hg := attachModel(t, home, reply)
	makeOneGroup(t, entry, domain.BotGroupMember{BotID: "echo"}, domain.BotGroupMember{NodeID: node.ID, BotID: "echo"})
	c := chat(t, entry, eg)
	d, err := entry.send(c.ID, SendRequest{Content: "@pool first"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	remote := func(content, id string) domain.Message {
		c := chat(t, home, hg)
		body, _ := json.Marshal(SendRequest{Content: content, RequestID: id, Generation: hg})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
		r.SetPathValue("nodeId", node.ID)
		r.SetPathValue("path", "chats/"+c.ID+"/messages")
		w := httptest.NewRecorder()
		entry.swarmProxy(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		t.Cleanup(func() {
			if t.Failed() {
				ms, _ := home.Store.Messages(c.ID)
				runs, _ := home.Store.ChatRuns(c.ID)
				t.Logf("%s messages: %+v runs: %+v", id, ms, runs)
			}
		})
		eventuallySwarm(t, func() bool {
			runs, _ := home.Store.ChatRuns(c.ID)
			if len(runs) > 0 && (runs[0].Status == "failed" || runs[0].Status == "interrupted") {
				t.Fatalf("%s: %+v", content, runs)
			}
			return len(runs) == 1 && runs[0].Status == "completed"
		})
		msgs, _ := home.Store.Messages(c.ID)
		return msgs[0]
	}
	second := remote("@pool second", "second")
	if !slices.Equal(second.Recipients, []string{"echo"}) {
		t.Fatal(second)
	}
	third := remote("@pool third", "third")
	if !slices.Equal(third.Recipients, []string{swarm.Key(entry.Swarm.Node().ID, "echo")}) {
		t.Fatal(third)
	}
	if err = entry.Swarm.SetDisconnected(node.ID, true); err != nil {
		t.Fatal(err)
	}
	c = chat(t, entry, eg)
	d, err = entry.send(c.ID, SendRequest{Content: "@pool offline"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	ms, _ := entry.Store.Messages(c.ID)
	if !slices.Equal(ms[0].Recipients, []string{"echo"}) {
		t.Fatal(ms[0])
	}
}

func TestOneGroupConcurrentClaimAndInterruptedRetry(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	group := makeOneGroup(t, s, domain.BotGroupMember{BotID: "echo"})
	start := make(chan struct{})
	type result struct {
		d   *dispatch
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		c := chat(t, s, g)
		go func() {
			<-start
			d, err := s.send(c.ID, SendRequest{Content: "@pool concurrent", Generation: g, RequestID: "race"})
			results <- result{d, err}
		}()
	}
	close(start)
	var queued *dispatch
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.d.waitingGroup {
			if queued != nil {
				t.Fatal("both turns queued")
			}
			queued = r.d
		}
	}
	if queued == nil {
		t.Fatal("two turns claimed the last slot")
	}
	nextInvocation(t, started, "echo", "concurrent")
	noInvocation(t, started)
	if err := s.Store.Delete("bot-groups", group.ID); err != nil {
		t.Fatal(err)
	}
	reopened := restartSwarmFixture(t, s)
	ms, err := reopened.Store.Messages(queued.chatID)
	if err != nil || len(ms) != 1 || ms[0].Status != "interrupted" || len(ms[0].Recipients) != 0 {
		t.Fatal(ms, err)
	}
	g, started = controlledModel(t, reopened)
	retry, err := reopened.send(queued.chatID, SendRequest{RequestID: "retry", rerunMessageID: ms[0].ID, Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	call := nextInvocation(t, started, "echo", "concurrent")
	call.finish <- "ok"
	waitDispatch(t, retry)
}

func TestOneGroupRemoteQueuedPermit(t *testing.T) {
	entry, eh := swarmFixture(t, "desktop", remotePassword)
	home, hh := swarmFixture(t, "home", remotePassword)
	worker, wh := swarmFixture(t, "worker", remotePassword)
	homeNode := connectSwarm(t, entry, hh, remotePassword)
	workerNode := connectSwarm(t, home, wh, remotePassword)
	connectSwarm(t, worker, eh, remotePassword)
	g, started := controlledModel(t, worker)
	block := sendWork(t, worker, g, "direct blocker")
	hold := nextInvocation(t, started, "echo", "blocker")
	group := makeOneGroup(t, entry, domain.BotGroupMember{NodeID: workerNode.ID, BotID: "echo"})
	c := chat(t, home, g)
	send := func() string {
		t.Helper()
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"content":"@pool private","requestId":"deferred"}`))
		r.SetPathValue("nodeId", homeNode.ID)
		r.SetPathValue("path", "chats/"+c.ID+"/messages")
		w := httptest.NewRecorder()
		entry.swarmProxy(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var reply struct {
			ID string `json:"dispatchId"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.ID == "" {
			t.Fatal(w.Body.String(), err)
		}
		return reply.ID
	}
	accepted := send()
	ms, _ := home.Store.Messages(c.ID)
	if len(ms) != 1 || ms[0].Status != "queued" || len(ms[0].Recipients) != 0 {
		t.Fatal(ms)
	}
	// A non-selected host receives only probes. The worker has no projection
	// or new run until entry authorization and slot assignment both succeed.
	if _, err := worker.Store.Chat(projectionID(homeNode.ID, c.ID)); err == nil {
		t.Fatal("prompt was delivered before assignment")
	}
	if err := entry.Store.Delete("bot-groups", group.ID); err != nil {
		t.Fatal(err)
	}
	if repeated := send(); repeated != accepted {
		t.Fatalf("duplicate group send got %s, want %s", repeated, accepted)
	}
	hold.finish <- "ok"
	waitDispatch(t, block)
	call := nextInvocation(t, started, "echo", "private")
	call.finish <- "ok"
	eventuallySwarm(t, func() bool {
		runs, _ := home.Store.ChatRuns(c.ID)
		return len(runs) == 1 && runs[0].Status == "completed"
	})
	final, _ := home.Store.Messages(c.ID)
	if ms[0].ID != final[0].ID || !slices.Equal(final[0].Recipients, []string{swarm.Key(workerNode.ID, "echo")}) {
		t.Fatal(final)
	}
}

func TestOneGroupIdleFirstUnlimitedAndOverlap(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	unlimited := 0
	first, err := s.saveBot(domain.Bot{Handle: "first", MaxParallelPrompts: &unlimited})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.saveBot(domain.Bot{Handle: "second"})
	if err != nil {
		t.Fatal(err)
	}
	pool := makeOneGroup(t, s, domain.BotGroupMember{BotID: first.ID}, domain.BotGroupMember{BotID: second.ID})
	if _, err = s.saveBotGroup(t.Context(), "", domain.BotGroup{Handle: "also", DeliveryMode: "one", Members: pool.Members[:1]}); err != nil {
		t.Fatal(err)
	}
	// Move the cursor so that the busy unlimited bot is next in rotation.
	a := sendWork(t, s, g, "@pool warmup")
	call := nextInvocation(t, started, "first", "warmup")
	call.finish <- "ok"
	waitDispatch(t, a)
	b := sendWork(t, s, g, "@pool warmup")
	call = nextInvocation(t, started, "second", "warmup")
	call.finish <- "ok"
	waitDispatch(t, b)
	h := sendWork(t, s, g, "@first direct")
	hold := nextInvocation(t, started, "first", "direct")
	idle := sendWork(t, s, g, "@pool idle")
	idleCall := nextInvocation(t, started, "second", "idle")
	d := sendWork(t, s, g, "@pool @also @first overlap")
	overlap := nextInvocation(t, started, "first", "@first @first @first overlap")
	if runs := dispatchRuns(t, s, d); len(runs) != 1 || runs[0].BotID != first.ID {
		t.Fatal(runs)
	}
	for _, c := range []modelInvocation{hold, idleCall, overlap} {
		c.finish <- "ok"
	}
	for _, d := range []*dispatch{h, idle, d} {
		waitDispatch(t, d)
	}
}

func TestOneGroupOwnerRecoveryAndLegacyPeer(t *testing.T) {
	s := testServer(t)
	self := s.Swarm.Node().ID
	g := makeOneGroup(t, s, domain.BotGroupMember{BotID: "echo"})
	q := rotationQuery{Action: "wait", Group: g.ID, Turn: "accepted"}
	if state, err := s.localRotation(self, q); err != nil || !state.Head {
		t.Fatal(state, err)
	}
	q.Action, q.Winner = "commit", swarm.Ref{NodeID: self, ID: "echo"}
	key := swarm.Key(q.Turn, swarm.Key(self, g.ID))
	if err := s.Store.Put("group-commits", key, groupCommit{Owner: self, Query: q}); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverGroupTurns(); err != nil {
		t.Fatal(err)
	}
	state, err := s.localRotation(self, rotationQuery{Action: "wait", Group: g.ID, Turn: "next"})
	if err != nil || !state.Head || state.Last != q.Winner {
		t.Fatal(state, err)
	}
	// Recovery frees a ticket that was obtained but never assigned.
	if err := s.recoverGroupTurns(); err != nil {
		t.Fatal(err)
	}
	state, err = s.localRotation(self, rotationQuery{Action: "wait", Group: g.ID, Turn: "after-restart"})
	if err != nil || !state.Head || state.Last != q.Winner {
		t.Fatal(state, err)
	}

	entry, _ := swarmFixture(t, "desktop", remotePassword)
	old, _ := swarmFixture(t, "legacy", remotePassword)
	node := old.Swarm.Node()
	node.Capabilities = []string{"owner-api/1", "dispatch/1", "stream/1", "bot-groups/1"}
	_, err = entry.Swarm.Pair(t.Context(), swarm.PairRequest{Node: node, Version: swarm.Version, Password: remotePassword, Activate: true, ID: strings.Repeat("a", 64), ReturnToken: strings.Repeat("b", 64)}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	makeOneGroup(t, entry, domain.BotGroupMember{BotID: "echo"})
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"content":"@pool review"}`))
	if ok, err := entry.prepareForwardedMessage(httptest.NewRecorder(), r, node.ID, "chat", "", ""); ok || err == nil || !strings.Contains(err.Error(), "update Echo on legacy") {
		t.Fatal(ok, err)
	}
	if err := entry.checkGroupWorkers([]domain.Segment{{Type: "group", DeliveryMode: "one"}}, []swarmParticipant{{Ref: swarm.Ref{NodeID: node.ID, ID: "echo"}}}); err == nil {
		t.Fatal("accepted a legacy worker")
	}
}
