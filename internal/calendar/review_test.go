package calendar

import (
	"context"
	"fmt"
	"github.com/brent/echo/internal/store"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewConcurrencyAndPersistedSelection(t *testing.T) {
	for _, limit := range []int{1, 0} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			dir := t.TempDir()
			db, e := store.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			c := &Calendar{Store: db}
			c.Save(Schedule{BotID: "bot", Prompt: "work", Recurrence: "every 1 hours", Timezone: "UTC"}, at)
			c.Tick(at.Add(4*time.Hour), true)
			db.Close()
			db, e = store.Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			c.Store = db
			jobs, _ := c.Occurrences()
			if len(jobs) != 4 {
				t.Fatal("review did not survive restart")
			}
			var active, maximum atomic.Int32
			c.Dispatch = func(context.Context, Occurrence) error {
				n := active.Add(1)
				for old := maximum.Load(); n > old; old = maximum.Load() {
					if maximum.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(40 * time.Millisecond)
				active.Add(-1)
				return nil
			}
			ids := []string{jobs[0].ID, jobs[1].ID, jobs[2].ID}
			if e = c.Review(context.Background(), ids, limit); e != nil {
				t.Fatal(e)
			}
			c.wg.Wait()
			if limit == 1 && maximum.Load() != 1 || limit == 0 && maximum.Load() != 3 {
				t.Fatal("wrong concurrency", maximum.Load())
			}
			result, _ := c.Occurrences()
			skipped, completed := 0, 0
			for _, o := range result {
				if o.Status == "skipped" {
					skipped++
				}
				if o.Status == "completed" {
					completed++
				}
			}
			if skipped != 1 || completed != 3 {
				t.Fatal(result)
			}
		})
	}
}

func TestDSTGapFoldAndDailyInterval(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	before := time.Date(2026, 3, 7, 15, 0, 0, 0, loc)
	next, e := Next("every 1 days", "America/Chicago", before)
	if e != nil || next.In(loc).Hour() != 15 || next.Sub(before) != 23*time.Hour {
		t.Fatal(next, e)
	}
	next, e = Next("daily 02:30", "America/Chicago", time.Date(2026, 3, 8, 0, 0, 0, 0, loc))
	if e != nil || next.In(loc).Day() != 9 {
		t.Fatal("DST gap", next, e)
	}
	first, e := Next("daily 01:30", "America/Chicago", time.Date(2026, 11, 1, 0, 0, 0, 0, loc))
	next, e = Next("daily 01:30", "America/Chicago", first)
	if e != nil || next.In(loc).Day() != 2 {
		t.Fatal("fold duplicated", next, e)
	}
	zone, e := LocalTimezone()
	if e != nil || zone == "" {
		t.Fatal(zone, e)
	}
	if _, e = time.LoadLocation(zone); e != nil {
		t.Fatal("server timezone is not portable", zone, e)
	}
}
