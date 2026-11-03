package coordinator

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestQuestionsWithoutCountLimit(t *testing.T) {
	g := New([]string{"a", "b"})
	defer g.Close()
	for i := 0; i < 65; i++ {
		id := fmt.Sprintf("question-%d", i)
		r, err := g.Send("a", "b", "Please clarify", id)
		if err != nil {
			t.Fatalf("question %d: %v", i, err)
		}
		duplicate, err := g.Send("a", "b", "Please clarify", id)
		if err != nil || duplicate != r {
			t.Fatalf("duplicate question: %+v, %v", duplicate, err)
		}
		if inbox := g.Drain("b"); len(inbox) != 1 || inbox[0] != r {
			t.Fatalf("unexpected inbox: %+v", inbox)
		}
		if err := g.Reply("b", r.ID, "Clarified"); err != nil {
			t.Fatal(err)
		}
		answer, err := g.Wait(context.Background(), "a", r.ID)
		if err != nil || !answer.Done || answer.Reply != "Clarified" {
			t.Fatalf("answer: %+v, %v", answer, err)
		}
	}
}

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
