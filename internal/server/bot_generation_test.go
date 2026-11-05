package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
)

func TestBotGenerationSaveAndInheritance(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "ok", nil) })
	w := scmAPI(t, s, "POST", "/bots", map[string]any{"handle": "pinned", "modelOverride": map[string]any{"endpointId": g.EndpointID, "model": "pinned-model"}, "thinkingOverride": "none"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var b domain.Bot
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	base := domain.Generation{EndpointID: "other", Model: "chat", Thinking: "high", Temperature: .23}
	want := domain.Generation{EndpointID: g.EndpointID, Model: "pinned-model", Thinking: "none", Temperature: .23}
	if got := b.OverrideGeneration(base); got != want {
		t.Fatal(got)
	}
	for _, field := range []string{"api", "tool"} {
		raw, _ := json.Marshal(map[string]any{"id": b.ID, "handle": b.Handle, "name": field})
		var err error
		if field == "tool" {
			echo, _ := s.Bot("echo")
			_, err = s.tool(t.Context(), nil, domain.ExecutionScope{Bot: echo}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_save", Arguments: string(raw)}})
		} else {
			_, err = s.saveBotInput(raw, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ = s.Bot(b.ID)
		if b.OverrideGeneration(base) != want {
			t.Fatal("omitted override lost", b)
		}
	}
	// Replacing the object uses its connection default, not the old model name.
	raw, _ := json.Marshal(map[string]any{"id": b.ID, "modelOverride": map[string]any{"endpointId": g.EndpointID}, "thinkingOverride": ""})
	b, err := s.saveBotInput(raw, nil)
	if err != nil || b.ModelOverride.Model != "" || b.ThinkingOverride != nil {
		t.Fatal(b, err)
	}
	raw, _ = json.Marshal(map[string]any{"id": b.ID, "modelOverride": nil, "thinkingOverride": nil})
	b, err = s.saveBotInput(raw, nil)
	if err != nil || b.OverrideGeneration(base) != base {
		t.Fatal(b, err)
	}
	for _, body := range []map[string]any{
		{"handle": "bad", "modelOverride": map[string]any{"endpointId": "missing"}},
		{"handle": "bad", "thinkingOverride": "unknown"},
	} {
		if w := scmAPI(t, s, "POST", "/bots", body); w.Code == 200 {
			t.Fatal("accepted invalid override", body)
		}
	}
}

func TestBotGenerationMixedRecipientsAndNoChatModel(t *testing.T) {
	s := testServer(t)
	seen := make(chan domain.Generation, 8)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model       string
			Thinking    string `json:"reasoning_effort"`
			Temperature float64
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		seen <- domain.Generation{Model: body.Model, Thinking: body.Thinking, Temperature: body.Temperature}
		stream(w, "ok", nil)
	})
	effort := "none"
	_, err := s.saveBot(domain.Bot{Handle: "pinned", ModelOverride: &domain.BotModelOverride{EndpointID: g.EndpointID, Model: "fixed"}, ThinkingOverride: &effort})
	if err != nil {
		t.Fatal(err)
	}
	for _, bot := range []domain.Bot{
		{Handle: "modelonly", ModelOverride: &domain.BotModelOverride{EndpointID: g.EndpointID}},
		{Handle: "thinkingonly", ThinkingOverride: &effort},
	} {
		if _, err := s.saveBot(bot); err != nil {
			t.Fatal(err)
		}
	}
	g.Thinking, g.Temperature = "high", .23
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "@echo @pinned @modelonly @thinkingonly work", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	got := []domain.Generation{<-seen, <-seen, <-seen, <-seen}
	for _, want := range []domain.Generation{{Model: g.Model, Thinking: "high", Temperature: .23}, {Model: g.Model, Thinking: "none", Temperature: .23}, {Model: "fixed", Thinking: "none", Temperature: .23}} {
		if !slices.Contains(got, want) {
			t.Fatal("unexpected effective generation", got, want)
		}
	}
	runs, _ := s.Store.ChatRuns(c.ID)
	byBot := map[string]domain.Generation{}
	for _, run := range runs {
		bot, _ := s.Bot(run.BotID)
		byBot[bot.Handle] = run.Generation
	}
	for handle, want := range map[string]domain.Generation{
		"echo": g, "modelonly": g,
		"thinkingonly": {EndpointID: g.EndpointID, Model: g.Model, Thinking: "none", Temperature: .23},
		"pinned":       {EndpointID: g.EndpointID, Model: "fixed", Thinking: "none", Temperature: .23},
	} {
		if byBot[handle] != want {
			t.Fatal(handle, byBot[handle], want)
		}
	}
	saved, _ := s.Store.Chat(c.ID)
	if saved.Generation != g {
		t.Fatal("bot override changed chat defaults", saved.Generation)
	}
	noModel := chat(t, s, domain.Generation{Temperature: .6})
	d, err = s.send(noModel.ID, SendRequest{Content: "@pinned work"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if (<-seen).Model != "fixed" {
		t.Fatal("override was not used")
	}
	if _, err = s.send(noModel.ID, SendRequest{Content: "@echo @pinned fail"}); err == nil {
		t.Fatal("accepted an unconfigured inherited model")
	}
	if err = s.Store.Delete("endpoints", g.EndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.send(noModel.ID, SendRequest{Content: "@pinned fail"}); err == nil {
		t.Fatal("missing override connection fell back")
	}
}

func TestBotGenerationEditKeepsAcceptedRuns(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	b, _ := s.Bot("echo")
	b.ModelOverride = &domain.BotModelOverride{EndpointID: g.EndpointID, Model: "before"}
	b, err := s.updateBot(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := sendWork(t, s, g, "first")
	active := nextInvocation(t, started, "echo", "first")
	queued := sendWork(t, s, g, "queued")
	b.ModelOverride.Model = "after"
	effort := "low"
	b.ThinkingOverride = &effort
	saved, err := s.updateBot(b, nil)
	if err != nil || saved.Revision != b.Revision {
		t.Fatal("preference edit revoked scope", saved, err)
	}
	select {
	case <-first.done:
		t.Fatal("active run stopped")
	default:
	}
	active.finish <- "first complete"
	waitDispatch(t, first)
	next := nextInvocation(t, started, "echo", "queued")
	next.finish <- "queued complete"
	waitDispatch(t, queued)
	for _, d := range []*dispatch{first, queued} {
		runs, _ := s.Store.ChatRuns(d.chatID)
		if len(runs) != 1 || runs[0].Generation.Model != "before" || runs[0].Generation.Thinking != "" {
			t.Fatal(runs)
		}
	}
	third := sendWork(t, s, g, "third")
	last := nextInvocation(t, started, "echo", "third")
	last.finish <- "done"
	waitDispatch(t, third)
	runs, _ := s.Store.ChatRuns(third.chatID)
	if runs[0].Generation.Model != "after" || runs[0].Generation.Thinking != "low" {
		t.Fatal(runs)
	}
}

func TestBotGenerationScheduledGroupAndDelegation(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "ok", nil) })
	b, err := s.saveBot(domain.Bot{Handle: "worker", ModelOverride: &domain.BotModelOverride{EndpointID: g.EndpointID}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.scheduled(t.Context(), calendar.Occurrence{BotID: b.ID, Prompt: "scheduled", Generation: domain.Generation{Temperature: .4}}); err != nil {
		t.Fatal(err)
	}
	makeOneGroup(t, s, domain.BotGroupMember{BotID: b.ID})
	c := chat(t, s, domain.Generation{Temperature: .4})
	d, err := s.send(c.ID, SendRequest{Content: "@pool grouped"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	echo, _ := s.Bot("echo")
	scope := delegationScope(t, s, echo, g)
	var parentRun domain.Run
	_ = s.Store.Get("runs", scope.RunID, &parentRun)
	parentRun.Generation.Model = "caller-pinned"
	_ = s.Store.Put("runs", parentRun.ID, parentRun)
	receipt, err := s.startDelegatedChat(t.Context(), scope, "worker", "delegated", "")
	if err != nil {
		t.Fatal(err)
	}
	child, _ := s.Store.Chat(receipt.ChatID)
	if child.Generation != g {
		t.Fatal("delegation leaked caller override", child.Generation)
	}
}

func TestBotGenerationSwarmSnapshots(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	seen := make(chan string, 1)
	g := attachModel(t, b, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen <- body.Model
		stream(w, "remote done", nil)
	})
	bot, _ := b.Bot("echo")
	bot.ModelOverride = &domain.BotModelOverride{EndpointID: g.EndpointID, Model: "remote-pinned"}
	if _, err := b.updateBot(bot, nil); err != nil {
		t.Fatal(err)
	}
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@linux.echo run", GenerationByNode: map[string]domain.Generation{b.Swarm.Node().ID: {EndpointID: "missing", Model: "chat", Temperature: .23}}})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	if <-seen != "remote-pinned" {
		t.Fatal("remote override lost")
	}
	runs, _ := a.Store.ChatRuns(c.ID)
	if len(runs) != 1 || runs[0].Generation.Model != "remote-pinned" || runs[0].Generation.Temperature != .23 {
		t.Fatal(runs)
	}
	defaults := chatForSwarm(t, a)
	d, err = a.send(defaults.ID, SendRequest{Content: "@linux.echo defaults"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	<-seen
	runs, _ = a.Store.ChatRuns(defaults.ID)
	if len(runs) != 1 || runs[0].Generation.Temperature != .6 {
		t.Fatal("pinned model changed inherited temperature", runs)
	}
}

func TestBotGenerationInlineAndCLIRejection(t *testing.T) {
	s := testServer(t)
	bot, input, _ := inlineFixture(t, s)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string
			Thinking string `json:"reasoning_effort"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Model != "inline-fixed" || body.Thinking != "none" {
			t.Error(body)
		}
		stream(w, "ok", nil)
	})
	effort := "none"
	bot.ModelOverride = &domain.BotModelOverride{EndpointID: g.EndpointID, Model: "inline-fixed"}
	bot.ThinkingOverride = &effort
	bot, err := s.updateBot(bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	input.Generation = domain.Generation{EndpointID: "missing", Temperature: .6}
	inlineEvents(t, inlineHTTP(s, bot, input, cookie, context.Background()))
	cli, _ := externalFixture(t, s, "codex-cli")
	bot.ModelOverride.EndpointID = cli.EndpointID
	bot, err = s.updateBot(bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := inlineHTTP(s, bot, input, cookie, context.Background())
	if w.Code != 400 || !strings.Contains(w.Body.String(), "not inline editor AI") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestBotGenerationLegacyHomeCannotIgnoreOverrides(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "ok", nil) })
	bot, _ := s.Bot("echo")
	bot.ModelOverride = &domain.BotModelOverride{EndpointID: g.EndpointID}
	bot, err := s.updateBot(bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	self := s.Swarm.Node().ID
	req := workerRequest{Home: "legacy", Origin: self, ID: domain.ID(), Token: strings.Repeat("t", 64), Runs: []domain.Run{{BotID: bot.ID, Generation: g}}}
	if err := s.validateWorker(req); err == nil || !strings.Contains(err.Error(), "update the chat home") {
		t.Fatal(err)
	}
}

func TestBotGenerationPersistenceAndExternalEffort(t *testing.T) {
	for _, kind := range []string{"codex-cli", "claude-code-cli"} {
		t.Run(kind, func(t *testing.T) {
			s := testServer(t)
			g, log := externalFixture(t, s, kind)
			bot, _ := s.Bot("echo")
			effort := "high"
			bot.ModelOverride = &domain.BotModelOverride{EndpointID: g.EndpointID, Model: "native-fixed"}
			bot.ThinkingOverride = &effort
			bot, err := s.updateBot(bot, nil)
			if err != nil {
				t.Fatal(err)
			}
			c := chat(t, s, domain.Generation{Temperature: .6})
			externalSend(t, s, c, SendRequest{Content: "first"})
			externalSend(t, s, c, SendRequest{Content: "second"})
			bot.ModelOverride.Model = "native-new"
			effort = "low"
			bot.ThinkingOverride = &effort
			if _, err = s.updateBot(bot, nil); err != nil {
				t.Fatal(err)
			}
			externalSend(t, s, c, SendRequest{Content: "third"})
			calls := externalCalls(t, log)
			if len(calls) != 3 || !slices.Contains(calls[0].Args, "native-fixed") || !slices.Contains(calls[1].Args, "native-fixed") || !slices.Contains(calls[2].Args, "native-new") {
				t.Fatal(calls)
			}
			resume := "resume"
			if kind == "claude-code-cli" {
				resume = "--resume"
			}
			if !slices.Contains(calls[1].Args, resume) || slices.Contains(calls[2].Args, resume) {
				t.Fatal("native settings change resumed stale session", calls)
			}
			for i, level := range []string{"high", "high", "low"} {
				want := "model_reasoning_effort=\"" + level + "\""
				if kind == "claude-code-cli" {
					want = level
				}
				if !slices.Contains(calls[i].Args, want) {
					t.Fatal("thinking argument missing", calls[i])
				}
			}
			dir := s.Dir
			s.Close()
			guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := New(dir, nil, guest)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			saved, _ := reopened.Bot("echo")
			if saved.ModelOverride.Model != "native-new" || *saved.ThinkingOverride != "low" {
				t.Fatal(saved)
			}
		})
	}
}
