package coordinator

import (
	"errors"
	"testing"
)

func TestHandoffCommitPrecedesDelivery(t *testing.T) {
	group := New([]string{"a", "b"})
	wakes := 0
	group.OnSend = func(Request) { wakes++ }
	failure := errors.New("disk unavailable")
	if _, err := group.SendWithCommit("a", "b", "work", "id", func(Request) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if group.Pending("b") || wakes != 0 {
		t.Fatal("uncommitted handoff delivered")
	}
	if _, err := group.Send("a", "b", "work", "id"); err != nil {
		t.Fatal(err)
	}
	if _, err := group.DrainWithCommit("b", func([]Request) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !group.Pending("b") {
		t.Fatal("uncommitted delivery removed inbox")
	}
	if err := group.ReplyWithCommit("b", "id", "answer", func(Request) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	requests := group.Drain("b")
	if len(requests) != 1 || requests[0].Done {
		t.Fatal("uncommitted reply became visible", requests)
	}
}
