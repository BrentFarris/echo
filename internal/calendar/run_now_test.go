package calendar

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

func manualCalendar(t *testing.T) (*Calendar, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &Calendar{Store: db, Dispatch: func(context.Context, Occurrence) error { return nil }}
	t.Cleanup(func() { c.Stop(); db.Close() })
	return c, dir
}

func saveManualSchedule(t *testing.T, c *Calendar, rule string, paused bool, at time.Time) Schedule {
	t.Helper()
	s, err := c.Save(Schedule{BotID: "bot", Prompt: "saved task", Recurrence: rule, Timezone: "America/Chicago", Paused: paused, Generation: domain.Generation{EndpointID: "saved", Model: "model", Temperature: .3}}, at)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunNowTimingAndSavedSettings(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	at := time.Date(2026, 3, 7, 14, 0, 0, 0, loc)
	now := at.Add(3 * time.Minute)
	for _, rule := range []string{at.Add(time.Hour).Format(time.RFC3339), "every 5 minutes", "every 2 hours", "every 1 days", "daily 17:00", "weekly WED 17:00"} {
		for _, paused := range []bool{false, true} {
			t.Run(rule+"/paused="+map[bool]string{false: "false", true: "true"}[paused], func(t *testing.T) {
				c, _ := manualCalendar(t)
				s := saveManualSchedule(t, c, rule, paused, at)
				var dispatched Occurrence
				c.Dispatch = func(_ context.Context, o Occurrence) error { dispatched = o; return nil }
				updated, o, err := c.RunNow(t.Context(), s.ID, now)
				if err != nil {
					t.Fatal(err)
				}
				c.wg.Wait()
				want := s.Next
				if oneShot(rule) {
					want = time.Time{}
				} else if rule[:5] == "every" {
					want, err = Next(rule, s.Timezone, now)
				}
				if err != nil || !updated.Next.Equal(want) || updated.Paused != paused || updated.Suspended != paused || updated.OneShotConsumed != oneShot(rule) {
					t.Fatalf("schedule: %+v; want next %v", updated, want)
				}
				if rule == "every 1 days" && updated.Next.Sub(now) != 23*time.Hour {
					t.Fatal("lost DST calendar-day semantics")
				}
				if !o.Manual || o.Status != "queued" || !o.IntendedAt.Equal(now) || o.ScheduleID != s.ID || o.ID == occurrence(s).ID || dispatched.Generation != s.Generation || dispatched.Prompt != s.Prompt || dispatched.BotID != s.BotID {
					t.Fatalf("occurrence: %+v; dispatched: %+v", o, dispatched)
				}
				if _, err := c.Tick(now.Add(time.Second), false); err != nil {
					t.Fatal(err)
				}
				var stored Schedule
				if err := c.Store.Get("calendar:schedules", s.ID, &stored); err != nil || !stored.Next.Equal(want) {
					t.Fatal("tick changed accepted timing", stored, err)
				}
			})
		}
	}
}

func TestRunNowOneShotStaysConsumedAndCanBeRerun(t *testing.T) {
	c, _ := manualCalendar(t)
	at := time.Now().UTC()
	s := saveManualSchedule(t, c, at.Add(time.Hour).Format(time.RFC3339), true, at)
	c.Dispatch = func(context.Context, Occurrence) error { return errors.New("model failed") }
	s, first, err := c.RunNow(t.Context(), s.ID, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	var failed Occurrence
	if err := c.Store.Get("calendar:occurrences", first.ID, &failed); err != nil || failed.Status != "failed" {
		t.Fatal(failed, err)
	}
	for _, enabled := range []bool{false, true} {
		c.Enabled = func(string) bool { return enabled }
		s.Paused, s.Prompt, s.OneShotConsumed = false, "edited task", false
		s, err = c.Save(s, at.Add(2*time.Minute))
		if err != nil || !s.OneShotConsumed || !s.Next.IsZero() {
			t.Fatal("edit rearmed event", s, err)
		}
		if err := c.ReconcileActivation(at.Add(3 * time.Minute)); err != nil {
			t.Fatal(err)
		}
		if due, err := c.Tick(at.Add(4*time.Minute), false); err != nil || len(due) != 0 {
			t.Fatal(due, err)
		}
	}
	c.Dispatch = func(context.Context, Occurrence) error { return nil }
	_, second, err := c.RunNow(t.Context(), s.ID, at.Add(5*time.Minute))
	if err != nil || second.ID == first.ID {
		t.Fatal(second, err)
	}
	c.wg.Wait()
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if due, err := c.Tick(at.Add(2*time.Hour), false); err != nil || len(due) != 0 {
		t.Fatal("consumed event fired", due, err)
	}
	// Successful execution can also be deliberately rerun.
	_, third, err := c.RunNow(t.Context(), s.ID, at.Add(6*time.Minute))
	if err != nil || third.ID == second.ID {
		t.Fatal(third, err)
	}
	s.Recurrence = at.Add(3 * time.Hour).Format(time.RFC3339)
	s, err = c.Save(s, at.Add(7*time.Minute))
	if err != nil || s.OneShotConsumed || s.Next.IsZero() {
		t.Fatal("explicit reschedule did not rearm", s, err)
	}
}

func TestRunNowMissedWork(t *testing.T) {
	for _, single := range []bool{true, false} {
		t.Run(map[bool]string{true: "one-shot", false: "recurring"}[single], func(t *testing.T) {
			c, _ := manualCalendar(t)
			at := time.Now().UTC()
			rule := "every 1 hours"
			if single {
				rule = at.Add(time.Hour).Format(time.RFC3339)
			}
			s := saveManualSchedule(t, c, rule, false, at)
			if _, err := c.Tick(at.Add(2*time.Hour), true); err != nil {
				t.Fatal(err)
			}
			before, _ := c.Occurrences()
			updated, o, err := c.RunNow(t.Context(), s.ID, at.Add(3*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			after, _ := c.Occurrences()
			if single {
				if len(after) != 1 || o.ID != before[0].ID || !updated.Next.IsZero() {
					t.Fatal(after, updated)
				}
			} else {
				if len(after) != len(before)+1 {
					t.Fatal(after)
				}
				for _, old := range before {
					var current Occurrence
					if err := c.Store.Get("calendar:occurrences", old.ID, &current); err != nil || !reflect.DeepEqual(old, current) {
						t.Fatal("changed missed work", old, current, err)
					}
				}
			}
		})
	}
}

func TestRunNowPreservesHistoricalRecurringMissedAfterRescheduling(t *testing.T) {
	c, _ := manualCalendar(t)
	at := time.Now().UTC()
	s := saveManualSchedule(t, c, "every 1 hours", false, at)
	if _, err := c.Tick(at.Add(2*time.Hour), true); err != nil {
		t.Fatal(err)
	}
	before, _ := c.Occurrences()
	s.Recurrence = at.Add(4 * time.Hour).Format(time.RFC3339)
	s, err := c.Save(s, at.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.RunNow(t.Context(), s.ID, at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	for _, old := range before {
		var current Occurrence
		if err := c.Store.Get("calendar:occurrences", old.ID, &current); err != nil || !reflect.DeepEqual(old, current) {
			t.Fatal("lost recurring missed work", current, err)
		}
	}
}

func TestRunNowIntervalFailureKeepsAcceptedTiming(t *testing.T) {
	c, _ := manualCalendar(t)
	at := time.Now().UTC()
	s := saveManualSchedule(t, c, "every 5 minutes", false, at)
	c.Dispatch = func(context.Context, Occurrence) error { return errors.New("model failed") }
	accepted := at.Add(2 * time.Minute)
	_, o, err := c.RunNow(t.Context(), s.ID, accepted)
	if err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	var stored Schedule
	if err := c.Store.Get("calendar:schedules", s.ID, &stored); err != nil || !stored.Next.Equal(accepted.Add(5*time.Minute)) {
		t.Fatal(stored, err)
	}
	var failed Occurrence
	if err := c.Store.Get("calendar:occurrences", o.ID, &failed); err != nil || failed.Status != "failed" {
		t.Fatal(failed, err)
	}
	due, err := c.Tick(stored.Next, false)
	if err != nil || len(due) != 1 || due[0].Manual || due[0].ID == o.ID {
		t.Fatal(due, err)
	}
}

func TestRunNowRejectionsDoNotChangeSchedule(t *testing.T) {
	for _, status := range []string{"queued", "running", "deferred", "disabled", "missing", "canceled"} {
		t.Run(status, func(t *testing.T) {
			c, _ := manualCalendar(t)
			at := time.Now().UTC()
			s := saveManualSchedule(t, c, "every 5 minutes", false, at)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			id, want := s.ID, ErrActive
			switch status {
			case "disabled":
				c.Enabled = func(string) bool { return false }
				want = ErrDisabled
			case "missing":
				id, want = "absent", ErrScheduleNotFound
			case "canceled":
				cancel()
				want = context.Canceled
			default:
				o := occurrence(s)
				o.Status = status
				if err := c.Store.Put("calendar:occurrences", o.ID, o); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := c.Occurrences()
			if _, _, err := c.RunNow(ctx, id, at.Add(time.Minute)); !errors.Is(err, want) {
				t.Fatal(err)
			}
			var stored Schedule
			c.Store.Get("calendar:schedules", s.ID, &stored)
			after, _ := c.Occurrences()
			if !reflect.DeepEqual(s, stored) || !reflect.DeepEqual(before, after) {
				t.Fatal("rejection mutated state")
			}
		})
	}
}

func TestRunNowConcurrentRequestsAndTick(t *testing.T) {
	for _, raceTick := range []bool{false, true} {
		t.Run(map[bool]string{false: "requests", true: "tick"}[raceTick], func(t *testing.T) {
			c, _ := manualCalendar(t)
			at := time.Now().UTC()
			s := saveManualSchedule(t, c, at.Add(time.Hour).Format(time.RFC3339), false, at)
			release := make(chan struct{})
			defer close(release)
			var calls, accepted atomic.Int32
			c.Dispatch = func(context.Context, Occurrence) error { calls.Add(1); <-release; return nil }
			var requests sync.WaitGroup
			for i := 0; i < 12; i++ {
				requests.Add(1)
				go func() {
					defer requests.Done()
					_, _, err := c.RunNow(t.Context(), s.ID, s.Next)
					if err == nil {
						accepted.Add(1)
					} else if !errors.Is(err, ErrActive) {
						t.Error(err)
					}
				}()
			}
			var due []Occurrence
			if raceTick {
				var err error
				due, err = c.Tick(s.Next, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			requests.Wait()
			all, _ := c.Occurrences()
			if len(all) != 1 || int(accepted.Load())+len(due) != 1 {
				t.Fatal("duplicate acceptance", all, accepted.Load(), due)
			}
		})
	}
}

func TestRunNowTransactionFailureRollsBack(t *testing.T) {
	c, dir := manualCalendar(t)
	at := time.Now().UTC()
	s := saveManualSchedule(t, c, "every 5 minutes", false, at)
	db, err := sql.Open("sqlite", filepath.Join(dir, "echo2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_manual BEFORE INSERT ON records WHEN NEW.namespace='calendar:occurrences' BEGIN SELECT RAISE(ABORT, 'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	c.Dispatch = func(context.Context, Occurrence) error { t.Error("dispatched uncommitted work"); return nil }
	if _, _, err := c.RunNow(t.Context(), s.ID, at.Add(time.Minute)); err == nil {
		t.Fatal("expected write failure")
	}
	var stored Schedule
	if err := c.Store.Get("calendar:schedules", s.ID, &stored); err != nil || !reflect.DeepEqual(s, stored) {
		t.Fatal("partial schedule change", stored, err)
	}
	all, err := c.Occurrences()
	if err != nil || len(all) != 0 {
		t.Fatal("orphan occurrence", all, err)
	}
}

func TestRunNowDeferredSurvivesRestart(t *testing.T) {
	c, _ := manualCalendar(t)
	at := time.Now().UTC()
	s := saveManualSchedule(t, c, at.Add(time.Hour).Format(time.RFC3339), false, at)
	c.Deferred = func() bool { return true }
	_, o, err := c.RunNow(t.Context(), s.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	var stored Occurrence
	if err := c.Store.Get("calendar:occurrences", o.ID, &stored); err != nil || stored.Status != "deferred" {
		t.Fatal(stored, err)
	}
	var calls atomic.Int32
	restarted := &Calendar{Store: c.Store, Dispatch: func(context.Context, Occurrence) error { calls.Add(1); return nil }}
	if err := restarted.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer restarted.Stop()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		c.Store.Get("calendar:occurrences", o.ID, &stored)
		if stored.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stored.Status != "completed" || calls.Load() != 1 {
		t.Fatal(stored, calls.Load())
	}
	if due, err := restarted.Tick(at.Add(2*time.Hour), false); err != nil || len(due) != 0 {
		t.Fatal(due, err)
	}
}
