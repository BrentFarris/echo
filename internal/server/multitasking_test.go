package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/store"
)

type modelInvocation struct {
	bot, prompt string
	finish      chan string
}

func controlledModel(t *testing.T, s *Server) (domain.Generation, chan modelInvocation) {
	t.Helper()
	started := make(chan modelInvocation, 20)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []model.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		bot := strings.Split(strings.Split(input.Messages[0].Content, "You are @")[1], ".")[0]
		call := modelInvocation{bot: bot, prompt: input.Messages[len(input.Messages)-1].Content, finish: make(chan string, 1)}
		started <- call
		select {
		case result := <-call.finish:
			if result == "fail" {
				http.Error(w, "model failed", http.StatusBadRequest)
			} else {
				stream(w, "Done", nil)
			}
		case <-r.Context().Done():
		}
	})
	// Cancel handlers before httptest's server cleanup, including on test failure.
	t.Cleanup(s.cancel)
	return g, started
}

func nextInvocation(t *testing.T, started <-chan modelInvocation, bot, prompt string) modelInvocation {
	t.Helper()
	select {
	case call := <-started:
		if call.bot != bot || !strings.Contains(call.prompt, prompt) {
			t.Fatalf("unexpected invocation: %+v; want %s / %s", call, bot, prompt)
		}
		return call
	case <-time.After(5 * time.Second):
		t.Fatalf("model did not start for %s / %s", bot, prompt)
		return modelInvocation{}
	}
}

func noInvocation(t *testing.T, started <-chan modelInvocation) {
	t.Helper()
	select {
	case call := <-started:
		t.Fatalf("queued bot started early: %+v", call)
	case <-time.After(80 * time.Millisecond):
	}
}

func sendWork(t *testing.T, s *Server, g domain.Generation, prompt string) *dispatch {
	t.Helper()
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: prompt, RequestID: "request", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func dispatchRuns(t *testing.T, s *Server, d *dispatch) []domain.Run {
	t.Helper()
	raw, err := s.Store.List("runs")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.Decode[domain.Run](raw)
	if err != nil {
		t.Fatal(err)
	}
	out := []domain.Run{}
	for _, run := range runs {
		if run.DispatchID == d.id {
			out = append(out, run)
		}
	}
	return out
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestMultitaskingDefaultsAndToolSettings(t *testing.T) {
	s := testServer(t)
	legacy := json.RawMessage(`{"id":"legacy","handle":"legacy","name":"Legacy","revision":1}`)
	if err := s.Store.Put("bots", "legacy", legacy); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Bot("legacy")
	if b.EnableMultitasking {
		t.Fatal("legacy bot enabled multitasking")
	}
	echo, _ := s.Bot("echo")
	if echo.EnableMultitasking {
		t.Fatal("Echo enabled multitasking")
	}
	toolSave := func(args map[string]any) domain.Bot {
		t.Helper()
		raw, _ := json.Marshal(args)
		value, err := s.tool(context.Background(), nil, domain.ExecutionScope{Bot: echo}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_save", Arguments: string(raw)}})
		if err != nil {
			t.Fatal(err)
		}
		return value.(domain.Bot)
	}
	b = toolSave(map[string]any{"handle": "serial"})
	if b.EnableMultitasking {
		t.Fatal("new bot default is not off")
	}
	b = toolSave(map[string]any{"handle": "parallel", "enableMultitasking": true})
	if !b.EnableMultitasking {
		t.Fatal("chat creation ignored true")
	}
	b = toolSave(map[string]any{"id": b.ID, "handle": b.Handle, "name": "Updated"})
	if !b.EnableMultitasking {
		t.Fatal("omitted setting was reset")
	}
	revision := b.Revision
	b = toolSave(map[string]any{"id": b.ID, "handle": b.Handle, "enableMultitasking": false})
	if b.EnableMultitasking || b.Revision != revision {
		t.Fatal("toggle changed scope", b)
	}
	b.EnableMultitasking = true
	w := scmAPI(t, s, "POST", "/bots", b)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil || !b.EnableMultitasking {
		t.Fatal("API round trip", b, err)
	}
	w = scmAPI(t, s, "GET", "/state", nil)
	if !strings.Contains(w.Body.String(), `"enableMultitasking":true`) {
		t.Fatal("state omits setting")
	}
	var reloaded domain.Bot
	if err := s.Store.Get("bots", b.ID, &reloaded); err != nil || !reloaded.EnableMultitasking {
		t.Fatal("setting not persisted", err)
	}
	for _, tool := range s.coreTools(echo) {
		if tool.Name == "bot_save" && tool.Parameters["properties"].(map[string]any)["enableMultitasking"] == nil {
			t.Fatal("missing tool schema property")
		}
	}
}

func TestMultitaskingFIFOAndIdempotency(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	d1 := sendWork(t, s, g, "one")
	first := nextInvocation(t, started, "echo", "one")
	d2 := sendWork(t, s, g, "two")
	d3 := sendWork(t, s, g, "three")
	for _, d := range []*dispatch{d2, d3} {
		runs := dispatchRuns(t, s, d)
		if len(runs) != 1 || runs[0].Status != "queued" {
			t.Fatal("not queued", runs)
		}
		s.mu.Lock()
		timerStarted := d.timer != nil
		s.mu.Unlock()
		if timerStarted {
			t.Fatal("queued dispatch started its execution timeout")
		}
	}
	duplicate, err := s.send(d2.chatID, SendRequest{Content: "two", RequestID: "request", Generation: g})
	if err != nil || duplicate.id != d2.id {
		t.Fatal("queued retry duplicated", err)
	}
	if _, err = s.send(d2.chatID, SendRequest{Content: "another", Generation: g}); err == nil {
		t.Fatal("second request in busy chat accepted")
	}
	noInvocation(t, started)
	first.finish <- "done"
	waitDispatch(t, d1)
	second := nextInvocation(t, started, "echo", "two")
	noInvocation(t, started)
	second.finish <- "done"
	waitDispatch(t, d2)
	third := nextInvocation(t, started, "echo", "three")
	third.finish <- "done"
	waitDispatch(t, d3)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.botQueues) != 0 || len(s.botReservations) != 0 {
		t.Fatal("scheduler leaked reservations")
	}
}

func TestMultitaskingLiveTogglesKeepWork(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	d1 := sendWork(t, s, g, "one")
	first := nextInvocation(t, started, "echo", "one")
	d2 := sendWork(t, s, g, "two")
	bot, _ := s.Bot("echo")
	revision := bot.Revision
	bot.EnableMultitasking = true
	bot.Plugins = map[string]bool{"shell": true, "calendar": true}
	w := scmAPI(t, s, "POST", "/bots", bot)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	second := nextInvocation(t, started, "echo", "two")
	bot, _ = s.Bot("echo")
	if bot.Revision != revision {
		t.Fatal("equivalent plugin defaults changed scope")
	}
	// A bot may change only its own scheduler preference without cancelling itself.
	raw := `{"id":"echo","handle":"echo","enableMultitasking":false}`
	_, err := s.tool(d1.ctx, d1, domain.ExecutionScope{Bot: bot, DispatchID: d1.id}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_save", Arguments: raw}})
	if err != nil {
		t.Fatal(err)
	}
	d3 := sendWork(t, s, g, "three")
	noInvocation(t, started)
	first.finish <- "done"
	waitDispatch(t, d1)
	noInvocation(t, started)
	second.finish <- "done"
	waitDispatch(t, d2)
	third := nextInvocation(t, started, "echo", "three")
	third.finish <- "done"
	waitDispatch(t, d3)
	for _, d := range []*dispatch{d1, d2, d3} {
		if d.err != nil {
			t.Fatal("toggle interrupted work", d.err)
		}
	}
}

func TestMultitaskingGroupStartsAvailableBotsAndReservesHandoffs(t *testing.T) {
	s := testServer(t)
	a, _ := s.saveBot(domain.Bot{Handle: "a"})
	b, _ := s.saveBot(domain.Bot{Handle: "b"})
	g, started := controlledModel(t, s)
	blocker := sendWork(t, s, g, "@a blocker")
	busy := nextInvocation(t, started, "a", "blocker")
	group := sendWork(t, s, g, "@a @b group")
	free := nextInvocation(t, started, "b", "group")
	free.finish <- "done"
	eventually(t, func() bool {
		for _, run := range dispatchRuns(t, s, group) {
			if run.BotID == b.ID && run.Status == "completed" {
				return true
			}
		}
		return false
	})
	later := sendWork(t, s, g, "@b @a later")
	noInvocation(t, started)
	busy.finish <- "done"
	waitDispatch(t, blocker)
	working := nextInvocation(t, started, "a", "group")
	request, err := group.group.Send(a.ID, b.ID, "follow-up", "handoff")
	if err != nil {
		t.Fatal(err)
	}
	resumed := nextInvocation(t, started, "b", "follow-up")
	resumed.finish <- "done"
	if _, err = group.group.Wait(context.Background(), a.ID, request.ID); err != nil {
		t.Fatal(err)
	}
	noInvocation(t, started)
	working.finish <- "done"
	waitDispatch(t, group)
	seen := map[string]bool{}
	for range 2 {
		select {
		case call := <-started:
			seen[call.bot] = true
			call.finish <- "done"
		case <-time.After(5 * time.Second):
			t.Fatal("overlapping group stalled")
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Fatal("later group missing bots", seen)
	}
	waitDispatch(t, later)
}

func TestMultitaskingCancellationAndRevocation(t *testing.T) {
	for _, action := range []string{"stop queued", "delete queued chat", "delete bot", "change scope", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			s := testServer(t)
			b, _ := s.saveBot(domain.Bot{Handle: "worker"})
			g, started := controlledModel(t, s)
			active := sendWork(t, s, g, "@worker active")
			call := nextInvocation(t, started, "worker", "active")
			queued := sendWork(t, s, g, "@worker queued")
			switch action {
			case "stop queued":
				s.stopChat(queued.chatID)()
			case "delete queued chat":
				if w := scmAPI(t, s, "DELETE", "/chats/"+queued.chatID, nil); w.Code != http.StatusOK {
					t.Fatal(w.Body.String())
				}
			case "delete bot":
				if w := scmAPI(t, s, "DELETE", "/bots/"+b.ID, nil); w.Code != http.StatusOK {
					t.Fatal(w.Body.String())
				}
			case "change scope":
				b.Prompt = "Changed"
				if _, err := s.updateBot(b, nil); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				s.cancel()
			}
			waitDispatch(t, queued)
			noInvocation(t, started)
			if action != "delete queued chat" {
				for _, run := range dispatchRuns(t, s, queued) {
					if run.Status != "cancelled" {
						t.Fatal("queued run not cancelled", run)
					}
				}
			}
			call.finish <- "done"
			waitDispatch(t, active)
			if action != "delete bot" && action != "shutdown" {
				next := sendWork(t, s, g, "@worker next")
				call = nextInvocation(t, started, "worker", "next")
				call.finish <- "done"
				waitDispatch(t, next)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.botReservations) != 0 || len(s.botQueues) != 0 {
				t.Fatal("scheduler state leaked")
			}
		})
	}
}

func TestMultitaskingScheduledWorkWaitsAndFailureReleases(t *testing.T) {
	s := testServer(t)
	g, started := controlledModel(t, s)
	active := sendWork(t, s, g, "manual")
	first := nextInvocation(t, started, "echo", "manual")
	done := make(chan error, 1)
	go func() {
		done <- s.scheduled(context.Background(), calendar.Occurrence{BotID: "echo", Prompt: "scheduled", Generation: g})
	}()
	eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.botQueues["echo"]) == 1 })
	noInvocation(t, started)
	first.finish <- "fail"
	waitDispatch(t, active)
	if active.err == nil {
		t.Fatal("expected model failure")
	}
	next := nextInvocation(t, started, "echo", "scheduled")
	next.finish <- "done"
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled request stalled")
	}
}

func TestMultitaskingQueuedHandoffWaitsForAdmission(t *testing.T) {
	s := testServer(t)
	a, _ := s.saveBot(domain.Bot{Handle: "a"})
	b, _ := s.saveBot(domain.Bot{Handle: "b"})
	g, started := controlledModel(t, s)
	blocker := sendWork(t, s, g, "@b blocker")
	busy := nextInvocation(t, started, "b", "blocker")
	group := sendWork(t, s, g, "@a @b group")
	working := nextInvocation(t, started, "a", "group")
	request, err := group.group.Send(a.ID, b.ID, "question", "queued-handoff")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.tool(group.ctx, group, domain.ExecutionScope{Bot: a}, domain.ToolCall{Function: domain.FunctionCall{Name: "bot_wait", Arguments: fmt.Sprintf(`{"requestId":%q}`, request.ID)}})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatal("wait returned before admission", err)
	case <-time.After(80 * time.Millisecond):
	}
	busy.finish <- "done"
	waitDispatch(t, blocker)
	reply := nextInvocation(t, started, "b", "question")
	reply.finish <- "done"
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handoff wait stalled")
	}
	working.finish <- "done"
	waitDispatch(t, group)
}

func TestMultitaskingRecoveryDoesNotResumeQueuedRuns(t *testing.T) {
	s := testServer(t)
	for _, status := range []string{"queued", "running"} {
		run := domain.Run{ID: domain.ID(), BotID: "echo", Status: status}
		if err := s.Store.Put("runs", run.ID, run); err != nil {
			t.Fatal(err)
		}
		if err := s.Store.Recover(); err != nil {
			t.Fatal(err)
		}
		var recovered domain.Run
		if err := s.Store.Get("runs", run.ID, &recovered); err != nil || recovered.Status != "interrupted" {
			t.Fatal("unfinished run was not interrupted", recovered, err)
		}
	}
	if len(s.dispatches) != 0 || len(s.botQueues) != 0 {
		t.Fatal("recovery resumed unfinished work")
	}
}
