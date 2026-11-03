package calendar

import (
	"context"
	"github.com/brent/echo/internal/store"
	"testing"
	"time"
)

func TestDSTAndMissedReview(t *testing.T) {
	at := time.Date(2026, 3, 7, 22, 0, 0, 0, time.UTC)
	next, e := Next("daily 15:00", "America/Chicago", at)
	if e != nil || next.Hour() != 20 {
		t.Fatal(next, e)
	}
	s, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c := &Calendar{Store: s, Dispatch: func(context.Context, Occurrence) error { return nil }}
	job, e := c.Save(Schedule{BotID: "kaiju", Prompt: "tests", Recurrence: "every 1 hours", Timezone: "UTC"}, at)
	if e != nil {
		t.Fatal(e)
	}
	c.Tick(at.Add(3*time.Hour), true)
	c.Tick(at.Add(3*time.Hour), true)
	occ, _ := c.Occurrences()
	if len(occ) != 3 {
		t.Fatal("missed jobs duplicated", len(occ))
	}
	if e = c.Review(context.Background(), []string{occ[0].ID}, 1); e != nil {
		t.Fatal(e)
	}
	c.wg.Wait()
	occ, _ = c.Occurrences()
	completed, skipped := 0, 0
	for _, o := range occ {
		if o.Status == "completed" {
			completed++
		}
		if o.Status == "skipped" {
			skipped++
		}
	}
	if completed != 1 || skipped != 2 {
		t.Fatal(occ)
	}
	c.Delete(job.ID)
}
func TestPausedDoesNotAccumulate(t *testing.T) {
	s, _ := store.Open(t.TempDir())
	defer s.Close()
	c := &Calendar{Store: s, Enabled: func(string) bool { return false }}
	at := time.Now()
	c.Save(Schedule{BotID: "a", Prompt: "task", Recurrence: "every 1 minutes", Timezone: "UTC"}, at)
	c.Tick(at.Add(time.Hour), true)
	jobs, _ := c.Occurrences()
	if len(jobs) != 0 {
		t.Fatal("disabled schedule accumulated missed work")
	}
}
