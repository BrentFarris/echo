package calendar

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/store"
)

func TestDeferredUpdateOccurrenceSurvivesRestartAndRunsOnce(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	o := Occurrence{ID: "maintenance-job", BotID: "echo", Status: "queued"}
	if err = db.Put("calendar:occurrences", o.ID, o); err != nil {
		t.Fatal(err)
	}
	c := &Calendar{Store: db, Deferred: func() bool { return true }}
	c.run(context.Background(), o)
	if err = db.Get("calendar:occurrences", o.ID, &o); err != nil || o.Status != "deferred" {
		t.Fatal(o, err)
	}
	var calls atomic.Int32
	replacement := &Calendar{Store: db, Dispatch: func(context.Context, Occurrence) error { calls.Add(1); return nil }}
	if err = replacement.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer replacement.Stop()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		_ = db.Get("calendar:occurrences", o.ID, &o)
		if o.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if o.Status != "completed" || calls.Load() != 1 {
		t.Fatal(o, calls.Load())
	}
	replacement.run(context.Background(), o)
	if calls.Load() != 1 {
		t.Fatal("deferred work repeated")
	}
}
