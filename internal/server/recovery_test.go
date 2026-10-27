package server

import (
	"encoding/json"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDispatchRetryAndScopeRevocation(t *testing.T) {
	s := testServer(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	bot, _ := s.saveBot(domain.Bot{Handle: "kaiju", Roots: []string{t.TempDir()}})
	c := chat(t, s, g)
	request := SendRequest{Content: "@kaiju do work", Generation: g, RequestID: "same-request"}
	d, e := s.send(c.ID, request)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	duplicate, e := s.send(c.ID, request)
	if e != nil || duplicate.id != d.id {
		t.Fatal("retry dispatched twice", e)
	}
	finish := s.stopBot(bot.ID)
	if _, e = s.send(c.ID, SendRequest{Content: "@kaiju do other work", Generation: g}); e == nil {
		t.Fatal("new run entered a revoked scope")
	}
	bot.Roots = []string{t.TempDir()}
	if _, e = s.saveBot(bot); e != nil {
		t.Fatal(e)
	}
	finish()
	waitDispatch(t, d)
	ms, _ := s.messages(c.ID)
	users := 0
	for _, m := range ms {
		if m.Role == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatal("duplicate accepted message", users)
	}
	if e = s.Store.DeleteChat(c.ID); e != nil {
		t.Fatal(e)
	}
	if ms, _ = s.messages(c.ID); len(ms) != 0 {
		t.Fatal("deleted transcript returned")
	}
}

func TestInterruptedToolHistoryAndChatModelAvailability(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "done", nil) })
	c := chat(t, s, g)
	echo, _ := s.Bot("echo")
	call := domain.ToolCall{ID: "uncertain", Type: "function", Function: domain.FunctionCall{Name: "file_write", Arguments: `{"path":"file"}`}}
	s.Store.AddMessage(domain.Message{ID: domain.ID(), ChatID: c.ID, BotID: "echo", Role: "assistant", Status: "completed", Calls: []domain.ToolCall{call}})
	s.Store.AddMessage(domain.Message{ID: domain.ID(), ChatID: c.ID, BotID: "echo", Role: "tool", Status: "interrupted", CallID: call.ID, Content: "original tool arguments"})
	history, e := s.history(echo, c.ID)
	if e != nil || !strings.Contains(history[2].Content, "Effects are uncertain") {
		t.Fatal(history, e)
	}
	a, _ := s.saveBot(domain.Bot{Handle: "a"})
	b, _ := s.saveBot(domain.Bot{Handle: "b", Plugins: map[string]bool{"cloud-models": false}})
	cloud, _ := s.Plugins.Get("cloud-models")
	var ep domain.Endpoint
	s.Store.Get("endpoints", g.EndpointID, &ep)
	ep.Cloud = true
	s.Store.Put("endpoints", ep.ID, ep)
	before, _ := s.messages(c.ID)
	if _, e = s.send(c.ID, SendRequest{Content: "@a and @b work", Generation: g}); e == nil {
		t.Fatal("globally disabled cloud provider was dispatched")
	}
	after, _ := s.messages(c.ID)
	if len(before) != len(after) {
		t.Fatal("invalid dispatch was persisted")
	}
	cloud.Enabled = true
	if e = s.Plugins.Save(cloud); e != nil {
		t.Fatal(e)
	}
	d, e := s.send(c.ID, SendRequest{Content: "@a and @b work", Generation: g})
	if e != nil {
		t.Fatal("legacy per-bot cloud override blocked the chat's selected model", e)
	}
	waitDispatch(t, d)
	after, e = s.messages(c.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, bot := range []domain.Bot{a, b} {
		completed := false
		for _, message := range after {
			if message.BotID == bot.ID && message.Role == "assistant" && message.Status == "completed" && message.Content == "done" {
				completed = true
			}
		}
		if !completed {
			runs, _ := s.Store.List("runs")
			t.Fatalf("@%s did not complete with the chat's cloud model: %s", bot.Handle, runs)
		}
	}
	recipients, e := s.recipients(SendRequest{Content: "@a @a @b @A", Generation: g})
	if e != nil || len(recipients) != 2 || recipients[0].ID != a.ID || recipients[1].ID != b.ID {
		t.Fatal(recipients, e)
	}
}

func TestScheduledPromptCannotExpandRecipients(t *testing.T) {
	s := testServer(t)
	bot, _ := s.saveBot(domain.Bot{Handle: "kaiju"})
	s.saveBot(domain.Bot{Handle: "backend"})
	recipients, e := s.recipients(SendRequest{Content: "talk about @backend", scheduledBot: bot.ID})
	if e != nil || len(recipients) != 1 || recipients[0].ID != bot.ID {
		t.Fatal(recipients, e)
	}
	// Snapshot sequence includes every delta represented by its message payload.
	c := chat(t, s, domain.Generation{})
	m := domain.Message{ID: domain.ID(), ChatID: c.ID, Role: "assistant", Status: "running"}
	s.Store.AddMessage(m)
	buffer := &streamBuffer{s: s, m: m}
	buffer.append(model.Delta{Content: "partial"})
	ms, seq, e := s.snapshot(c.ID)
	if e != nil || seq < 1 || ms[0].Content != "partial" {
		t.Fatal(ms, seq, e)
	}
	data, _ := json.Marshal(ms)
	if !strings.Contains(string(data), "partial") {
		t.Fatal("snapshot lost active text")
	}
}
