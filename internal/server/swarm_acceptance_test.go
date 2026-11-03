package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/coordinator"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/swarm"
)

func restartSwarmFixture(t *testing.T, previous *Server) *Server {
	t.Helper()
	dir, secrets := previous.Dir, previous.Secrets
	previous.Close()
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	current, err := New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	current.Secrets = secrets
	t.Cleanup(current.Close)
	return current
}

func TestSwarmHomeRestartReconcilesIndependentWorkerCompletion(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			stream(w, "completed without home", nil)
		case <-r.Context().Done():
		}
	})
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@linux.echo work", RequestID: "independent"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("worker did not start")
	}
	identity := a.Swarm.Node().ID
	a.Close()
	close(release)
	eventuallySwarm(t, func() bool {
		var req workerRequest
		_ = b.Store.Get("swarm-workers", workerKey(identity, d.id), &req)
		return req.Status == "completed"
	})
	a = restartSwarmFixture(t, a)
	if a.Swarm.Node().ID != identity {
		t.Fatal("restart changed stable identity")
	}
	eventuallySwarm(t, func() bool {
		a.reconcileSwarm()
		messages, _ := a.messages(c.ID)
		return slices.ContainsFunc(messages, func(m domain.Message) bool { return m.Content == "completed without home" })
	})
	duplicate, err := a.send(c.ID, SendRequest{Content: "@linux.echo work", RequestID: "independent"})
	if err != nil || duplicate.id != d.id {
		t.Fatal("accepted ID was lost", err)
	}
	if calls.Load() != 1 {
		t.Fatal("worker repeated accepted execution")
	}
}

func TestSwarmWorkerRestartIsInterruptedAndCannotRepeatAcceptance(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		entered <- struct{}{}
		<-r.Context().Done()
	})
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@linux.echo work"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("worker did not start")
	}
	req := d.federation.Hosts[b.Swarm.Node().ID]
	b = restartSwarmFixture(t, b)
	var record workerRequest
	if err = b.Store.Get("swarm-workers", workerKey(req.Home, req.ID), &record); err != nil || record.Status != "interrupted" {
		t.Fatalf("restart state %+v %v", record, err)
	}
	if err = b.prepareWorker(req); err != nil {
		t.Fatal(err)
	}
	if err = b.startWorker(workerControl{Home: req.Home, ID: req.ID, Token: req.Token}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("crashed execution was replayed")
	}
	a.stopChat(c.ID)()
}

func TestSwarmDisconnectRetainsCredentialsAndBlocksDiscovery(t *testing.T) {
	a, _ := swarmFixture(t, "a", remotePassword)
	b, bh := swarmFixture(t, "b", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	if err := a.Swarm.SetDisconnected(b.Swarm.Node().ID, true); err != nil {
		t.Fatal(err)
	}
	if a.Swarm.Authorized(b.Swarm.Node().ID) {
		t.Fatal("disconnected peer authorized")
	}
	input := swarm.PairRequest{Version: swarm.Version, Node: b.Swarm.Node(), ID: domain.ID() + domain.ID(), ReturnToken: domain.ID() + domain.ID(), Password: remotePassword}
	if _, err := a.Swarm.Pair(context.Background(), input, "local"); err == nil {
		t.Fatal("discovery reconnected a disconnected peer")
	}
	if err := a.Swarm.SetDisconnected(b.Swarm.Node().ID, false); err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return a.Swarm.JSON(ctx, b.Swarm.Node().ID, "GET", "/api/v1/state", nil, nil) == nil
	})
}

func TestSwarmRemoteHomeResolvesEntryAndAuthorizesEveryDestination(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	c, ch := swarmFixture(t, "mac", remotePassword+"other")
	connectSwarm(t, a, bh, remotePassword)
	connectSwarm(t, b, ch, remotePassword+"other")
	for _, s := range []*Server{a, b, c} {
		_, err := s.saveBot(domain.Bot{ID: "kaiju", Handle: "kaiju", Name: "Kaiju"})
		if err != nil {
			t.Fatal(err)
		}
		attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) })
	}
	chat := chatForSwarm(t, b)
	cookie := remoteLogin(t, a)
	send := func(content string) *http.Response {
		input, _ := json.Marshal(SendRequest{Content: content, RequestID: domain.ID()})
		return remoteRequest(a, "POST", "/swarm/instances/"+b.Swarm.Node().ID+"/chats/"+chat.ID+"/messages", string(input), cookie).Result()
	}
	response := send("@kaiju @linux.kaiju @mac.kaiju work")
	response.Body.Close()
	if response.StatusCode == 200 {
		t.Fatal("B's credentials substituted for A's destination authorization")
	}
	connectSwarm(t, a, ch, remotePassword+"other")
	response = send("@kaiju @linux.kaiju @mac.kaiju work")
	var value struct {
		ID string `json:"dispatchId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || value.ID == "" {
		t.Fatalf("remote home rejected dispatch: %d", response.StatusCode)
	}
	eventuallySwarm(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.dispatches[value.ID] == nil })
	messages, _ := b.messages(chat.ID)
	users := slices.DeleteFunc(messages, func(m domain.Message) bool { return m.Role != "user" })
	if len(users) != 1 || !slices.Contains(users[0].Recipients, swarm.Key(a.Swarm.Node().ID, "kaiju")) {
		t.Fatal("bare mention did not resolve at browser entry", users)
	}
}

func TestSwarmRemoteEchoCreatesBotOnlyAtWorker(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	root := t.TempDir()
	var calls atomic.Int32
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"handle": "kaiju", "name": "Kaiju", "roots": []string{root}})
			stream(w, "", []domain.ToolCall{{ID: "create", Type: "function", Function: domain.FunctionCall{Name: "bot_save", Arguments: string(args)}}})
			return
		}
		stream(w, "created", nil)
	})
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@linux.echo Create kaiju in " + root})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	bots, _ := b.Bots()
	if !slices.ContainsFunc(bots, func(bot domain.Bot) bool { return bot.Handle == "kaiju" && slices.Contains(bot.Roots, root) }) {
		t.Fatal("remote Echo did not create workspace bot", bots)
	}
	bots, _ = a.Bots()
	if slices.ContainsFunc(bots, func(bot domain.Bot) bool { return bot.Handle == "kaiju" }) {
		t.Fatal("remote Echo created bot on entry")
	}
}

func TestSwarmStableNamesFilesAndWorkerAuthority(t *testing.T) {
	a, _ := swarmFixture(t, "same", remotePassword)
	b, bh := swarmFixture(t, "same", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	if a.Swarm.Namespace(b.Swarm.Node().ID) == "same" || a.Swarm.Namespace(a.Swarm.Node().ID) == "same" {
		t.Fatal("name collision was not disambiguated")
	}
	bot, err := b.saveBot(domain.Bot{Handle: "kaiju", Name: "Kaiju", Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) })
	handle := "@" + a.Swarm.Namespace(b.Swarm.Node().ID) + ".kaiju"
	ref := domain.Segment{Type: "bot", NodeID: b.Swarm.Node().ID, BotID: bot.ID, Text: handle, Range: &domain.SegmentRange{Start: 0, End: len(handle)}}
	if err = b.Swarm.Configure("renamed", ""); err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool { return a.Swarm.Namespace(b.Swarm.Node().ID) == "renamed" })
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: handle + " work", Segments: []domain.Segment{ref}})
	if err != nil {
		t.Fatal("rename invalidated stable reference", err)
	}
	waitDispatch(t, d)
	forbidden := filepath.Join(a.Dir, "private.txt")
	if err = os.WriteFile(forbidden, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = a.send(c.ID, SendRequest{Content: "@renamed.kaiju read", Segments: []domain.Segment{{Type: "file", NodeID: a.Swarm.Node().ID, Path: forbidden, Text: "private.txt"}}})
	if err == nil {
		t.Fatal("cross-instance reference became worker filesystem access")
	}
	var record swarmDispatch
	if err = a.Store.Get("swarm-dispatches", d.id, &record); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://localhost/api/v1/state", nil)
	request.RemoteAddr = "192.0.2.10:5000"
	request.Header.Set("Authorization", "Bearer "+record.Hosts[b.Swarm.Node().ID].Token)
	response := httptest.NewRecorder()
	b.Handler().ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("worker grant acquired owner authority", response.Code)
	}
}

func TestSwarmAcceptedWorkerRestartNeverRepeatsTools(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	var calls atomic.Int32
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); stream(w, "once", nil) })
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@linux.echo work", RequestID: "once"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	var saved swarmDispatch
	if err = a.Store.Get("swarm-dispatches", d.id, &saved); err != nil {
		t.Fatal(err)
	}
	req := saved.Hosts[b.Swarm.Node().ID]
	// An acknowledged worker may be retransmitted after a lost start response.
	if err = b.prepareWorker(req); err != nil {
		t.Fatal(err)
	}
	if err = b.startWorker(workerControl{Home: req.Home, ID: req.ID, Token: req.Token}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("accepted execution was repeated", calls.Load())
	}
	finish := a.stopChat(c.ID)
	if err = a.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	finish()
	a.reconcileSwarm()
	if !b.Store.ChatDeleted(projectionID(a.Swarm.Node().ID, c.ID)) {
		t.Fatal("deletion was not propagated")
	}
	if err = b.prepareWorker(req); err == nil { // A tombstoned accepted ID is harmless but must never create history.
		_ = b.startWorker(workerControl{Home: req.Home, ID: req.ID, Token: req.Token})
	}
	if _, err = b.Store.Chat(projectionID(a.Swarm.Node().ID, c.ID)); err == nil {
		t.Fatal("deleted worker chat resurrected")
	}
	if calls.Load() != 1 {
		t.Fatal("deleted execution repeated")
	}
}

func TestSwarmDisabledAndVersionMismatchRequireExplicitPairing(t *testing.T) {
	a, _ := swarmFixture(t, "a", remotePassword)
	b, bh := swarmFixture(t, "b", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	plugin, _ := b.Plugins.Get("swarm")
	plugin.Enabled = false
	_ = b.Plugins.Save(plugin)
	b.Swarm.Refresh()
	input := swarm.PairRequest{Version: swarm.Version, ID: domain.ID() + domain.ID(), Node: a.Swarm.Node(), Password: remotePassword, ReturnToken: domain.ID() + domain.ID()}
	if _, err := b.Swarm.Pair(context.Background(), input, "local"); err == nil {
		t.Fatal("discovery enabled disabled plugin")
	}
	input.Version = 999
	input.Activate = true
	if _, err := b.Swarm.Pair(context.Background(), input, "local"); err == nil {
		t.Fatal("incompatible protocol accepted")
	}
}

func TestSwarmCancelDuringOutageIsDurable(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	entered := make(chan struct{}, 1)
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
			stream(w, "late", nil)
		}
	})
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@linux.echo wait"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("worker never started")
	}
	p, _ := a.Plugins.Get("swarm")
	p.Enabled = false
	_ = a.Plugins.Save(p)
	a.Swarm.Refresh()
	finish := a.stopChat(c.ID)
	finish()
	waitDispatch(t, d)
	pending, _ := a.Store.List("swarm-cancellations")
	if len(pending) == 0 {
		t.Fatal("offline stop was not persisted")
	}
	if err = a.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	p.Enabled = true
	_ = a.Plugins.Save(p)
	a.Swarm.Refresh()
	eventuallySwarm(t, func() bool {
		a.reconcileSwarm()
		pending, _ := a.Store.List("swarm-cancellations")
		return len(pending) == 0 && b.Store.ChatDeleted(projectionID(a.Swarm.Node().ID, c.ID))
	})
	if _, err = a.Store.Chat(c.ID); err == nil {
		t.Fatal("deleted home chat resurrected")
	}
}

func TestSwarmStopReconcilesToolAfterCancellationCleanup(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	b.Plugins.Close(context.Background())
	b.Plugins, err = plugins.New(b.ctx, b.Store, filepath.Join(b.Dir, "plugins"), guest, func(ctx context.Context, _ domain.ExecutionScope, _ string, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		// Native tools may need time to reap descendants and collect file changes.
		<-release
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		stream(w, "", []domain.ToolCall{{ID: "held-tool", Type: "function", Function: domain.FunctionCall{Name: "web_get", Arguments: `{"url":"https://example.com"}`}}})
	})
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@linux.echo use the tool"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("tool did not start")
	}
	a.stopChat(c.ID)()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the worker tool")
	}
	a.reconcileSwarm()
	var saved swarmDispatch
	if err = a.Store.Get("swarm-dispatches", d.id, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Reconciled[b.Swarm.Node().ID] {
		t.Fatal("marked worker reconciled while its tool was still cleaning up")
	}
	// Existing chats may already contain the premature checkpoint from an older build.
	saved.Reconciled = map[string]bool{b.Swarm.Node().ID: true}
	if err = a.Store.Put("swarm-dispatches", d.id, &saved); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	eventuallySwarm(t, func() bool {
		a.reconcileSwarm()
		messages, _ := a.messages(c.ID)
		if !slices.ContainsFunc(messages, func(m domain.Message) bool { return m.Role == "tool" && m.Status == "cancelled" }) {
			return false
		}
		for _, original := range d.federation.Hosts[b.Swarm.Node().ID].Runs {
			var run domain.Run
			if a.Store.Get("runs", original.ID, &run) != nil || run.Status != "cancelled" {
				return false
			}
		}
		return true
	})
	for _, original := range d.federation.Hosts[b.Swarm.Node().ID].Runs {
		var run domain.Run
		if err = a.Store.Get("runs", original.ID, &run); err != nil || run.Status != "cancelled" {
			t.Fatalf("run remained active after Stop: %+v, %v", run, err)
		}
	}
}

func TestSwarmCompetingHomesReserveWithoutDeadlockAndUseHostModels(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	entered, release := make(chan string, 4), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	generations := map[string]domain.Generation{}
	for _, host := range []*Server{a, b} {
		var active atomic.Int32
		name := host.Swarm.Node().Name
		g := attachModel(t, host, func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.Model != name+"-override" {
				t.Errorf("%s used %s", name, input.Model)
			}
			if active.Add(1) > 1 {
				t.Errorf("%s accepted competing bot execution", name)
			}
			defer active.Add(-1)
			entered <- name
			select {
			case <-release:
				stream(w, name+" done", nil)
			case <-r.Context().Done():
			}
		})
		g.Model = name + "-override"
		generations[host.Swarm.Node().ID] = g
	}
	dispatches := []*dispatch{}
	for _, home := range []*Server{a, b} {
		chat := chatForSwarm(t, home)
		d, err := home.send(chat.ID, SendRequest{Content: "@desktop.echo @linux.echo build", GenerationByNode: generations})
		if err != nil {
			t.Fatal(err)
		}
		dispatches = append(dispatches, d)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(8 * time.Second):
			t.Fatal("competing homes deadlocked while reserving")
		}
	}
	select {
	case name := <-entered:
		t.Fatalf("%s bypassed active reservation", name)
	case <-time.After(150 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	for _, d := range dispatches {
		waitDispatch(t, d)
	}
	if len(entered) != 2 {
		t.Fatalf("queued work did not run on both hosts: %d", len(entered))
	}
}

func TestSwarmRemoteWaitReplyAndUnmentionedRecipient(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	for _, s := range []*Server{a, b} {
		attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			entered <- struct{}{}
			select {
			case <-release:
				stream(w, "done", nil)
			case <-r.Context().Done():
			}
		})
	}
	// Release handlers before their httptest servers close, including on failure.
	defer once.Do(func() { close(release) })
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@desktop.echo @linux.echo coordinate"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(8 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	group := func(s *Server) *workerGroup {
		s.swarmMu.Lock()
		defer s.swarmMu.Unlock()
		return s.swarmWorkers[workerKey(a.Swarm.Node().ID, d.id)].d.group.(*workerGroup)
	}
	local, remote := group(a), group(b)
	if _, err = remote.Send("echo", "mac.echo", "unmentioned", "forbidden"); err == nil {
		t.Fatal("worker addressed an unmentioned participant")
	}
	target, err := remote.worker.recipient("desktop.echo")
	if err != nil {
		t.Fatal(err)
	}
	request, err := remote.Send("echo", target, "question", "remote-question")
	if err != nil {
		t.Fatal(err)
	}
	if target, e := remote.WaitTarget("echo", request.ID); e != nil || target != swarm.Key(a.Swarm.Node().ID, "echo") {
		t.Fatal("correlation target lost", target, e)
	}
	result := make(chan coordinator.Request, 1)
	failure := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		value, e := remote.Wait(ctx, "echo", request.ID)
		if e != nil {
			failure <- e
			return
		}
		result <- value
	}()
	if err = local.Reply("echo", request.ID, "answer"); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-result:
		if value.Reply != "answer" {
			t.Fatalf("reply lost: %+v", value)
		}
	case e := <-failure:
		t.Fatal(e)
	case <-time.After(8 * time.Second):
		t.Fatal("remote wait did not wake")
	}
	rows, _ := a.Store.List("swarm-replies:" + d.id)
	if len(rows) != 1 {
		t.Fatal("reply was not durable")
	}
	once.Do(func() { close(release) })
	waitDispatch(t, d)
}
