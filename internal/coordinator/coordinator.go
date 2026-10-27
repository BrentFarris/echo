// Package coordinator implements explicit handoffs without a second LLM.
package coordinator

import (
	"context"
	"errors"
	"github.com/brent/echo/internal/domain"
	"sync"
)

type Request struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Content string `json:"content"`
	Reply   string `json:"reply,omitempty"`
	Done    bool   `json:"done"`
}
type Group struct {
	mu       sync.Mutex
	members  map[string]bool
	requests map[string]*Request
	inbox    map[string][]string
	waiting  map[string]string
	notify   chan struct{}
	closed   bool
	OnSend   func(Request)
}

func New(ids []string) *Group {
	g := &Group{members: map[string]bool{}, requests: map[string]*Request{}, inbox: map[string][]string{}, waiting: map[string]string{}, notify: make(chan struct{})}
	for _, id := range ids {
		g.members[id] = true
	}
	return g
}
func (g *Group) signal() { close(g.notify); g.notify = make(chan struct{}) }
func (g *Group) Send(from, to, content, id string) (Request, error) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return Request{}, errors.New("dispatch stopped")
	}
	if !g.members[from] || !g.members[to] || from == to {
		g.mu.Unlock()
		return Request{}, errors.New("handoffs must target another bot in this dispatch")
	}
	if id != "" {
		if old := g.requests[id]; old != nil {
			if old.From != from || old.To != to || old.Content != content {
				g.mu.Unlock()
				return Request{}, errors.New("request id already used")
			}
			v := *old
			g.mu.Unlock()
			return v, nil
		}
	}
	if id == "" {
		id = domain.ID()
	}
	r := Request{ID: id, From: from, To: to, Content: content}
	g.requests[id] = &r
	g.inbox[to] = append(g.inbox[to], id)
	g.signal()
	callback := g.OnSend
	g.mu.Unlock()
	if callback != nil {
		callback(r)
	}
	return r, nil
}
func (g *Group) Drain(bot string) []Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []Request{}
	for _, id := range g.inbox[bot] {
		out = append(out, *g.requests[id])
	}
	delete(g.inbox, bot)
	return out
}
func (g *Group) Pending(bot string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.inbox[bot]) > 0
}
func (g *Group) Reply(bot, id, text string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.requests[id]
	if r == nil || r.To != bot {
		return errors.New("request is not addressed to this bot")
	}
	if r.Done {
		if r.Reply == text {
			return nil
		}
		return errors.New("request already answered")
	}
	r.Reply = text
	r.Done = true
	g.signal()
	return nil
}
func (g *Group) Wait(ctx context.Context, bot, id string) (Request, error) {
	g.mu.Lock()
	r := g.requests[id]
	if r == nil || r.From != bot {
		g.mu.Unlock()
		return Request{}, errors.New("request does not belong to this bot")
	}
	g.waiting[bot] = r.To
	seen := map[string]bool{bot: true}
	next := r.To
	for next != "" {
		if seen[next] {
			delete(g.waiting, bot)
			g.mu.Unlock()
			return Request{}, errors.New("circular wait detected; reply to the outstanding request before waiting")
		}
		seen[next] = true
		next = g.waiting[next]
	}
	defer func() { g.mu.Lock(); delete(g.waiting, bot); g.mu.Unlock() }()
	for {
		if g.closed {
			g.mu.Unlock()
			return Request{}, errors.New("dispatch stopped")
		}
		if r.Done {
			v := *r
			g.mu.Unlock()
			return v, nil
		}
		notify := g.notify
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return Request{}, ctx.Err()
		case <-notify:
		}
		g.mu.Lock()
	}
}

// WaitTarget validates ownership before the caller waits for scheduler admission.
func (g *Group) WaitTarget(bot, id string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.requests[id]
	if r == nil || r.From != bot {
		return "", errors.New("request does not belong to this bot")
	}
	return r.To, nil
}
func (g *Group) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		g.signal()
	}
}

func (g *Group) Fail(bot string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.requests {
		if r.To == bot && !r.Done {
			r.Done = true
			r.Reply = "Recipient failed: " + err.Error()
		}
	}
	g.signal()
}
