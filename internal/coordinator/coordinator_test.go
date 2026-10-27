package coordinator

import (
	"context"
	"testing"
	"time"
)

func TestHandoffs(t *testing.T) {
	g := New([]string{"a", "b"})
	r, e := g.Send("a", "b", "endpoint", "request")
	if e != nil {
		t.Fatal(e)
	}
	g.Send("a", "b", "endpoint", "request")
	if len(g.Drain("b")) != 1 {
		t.Fatal("duplicate delivered")
	}
	if _, e = g.Send("a", "c", "escape", ""); e == nil {
		t.Fatal("outsider accepted")
	}
	g.Reply("b", r.ID, "/api/shield")
	answer, e := g.Wait(context.Background(), "a", r.ID)
	if e != nil || answer.Reply != "/api/shield" {
		t.Fatal(answer, e)
	}
}
func TestCycleAndCancellation(t *testing.T) {
	g := New([]string{"a", "b"})
	a, _ := g.Send("a", "b", "a", "")
	b, _ := g.Send("b", "a", "b", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error)
	go func() { _, e := g.Wait(ctx, "a", a.ID); done <- e }()
	for {
		g.mu.Lock()
		waiting := len(g.waiting) > 0
		g.mu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, e := g.Wait(ctx, "b", b.ID); e == nil {
		t.Fatal("cycle not detected")
	}
	g.Close()
	if e := <-done; e == nil {
		t.Fatal("cancel not propagated")
	}
}
