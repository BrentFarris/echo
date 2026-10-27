package calendar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
)

type Schedule struct {
	ID         string            `json:"id"`
	BotID      string            `json:"botId"`
	Prompt     string            `json:"prompt"`
	Recurrence string            `json:"recurrence"`
	Timezone   string            `json:"timezone"`
	Generation domain.Generation `json:"generation"`
	Paused     bool              `json:"paused"`
	Suspended  bool              `json:"suspended"`
	Next       time.Time         `json:"next"`
	CreatedAt  time.Time         `json:"createdAt"`
}
type Occurrence struct {
	ID         string            `json:"id"`
	ScheduleID string            `json:"scheduleId"`
	BotID      string            `json:"botId"`
	Prompt     string            `json:"prompt"`
	IntendedAt time.Time         `json:"intendedAt"`
	Generation domain.Generation `json:"generation"`
	Status     string            `json:"status"`
	Error      string            `json:"error,omitempty"`
	Timezone   string            `json:"timezone"`
	Recurrence string            `json:"recurrence"`
}
type Calendar struct {
	Store    *store.Store
	mu       sync.Mutex
	Dispatch func(context.Context, Occurrence) error
	Enabled  func(string) bool
	Changed  func()
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func Next(rule, timezone string, after time.Time) (time.Time, error) {
	loc, e := time.LoadLocation(timezone)
	if e != nil {
		return time.Time{}, e
	}
	rule = strings.TrimSpace(rule)
	parts := strings.Fields(strings.ToLower(rule))
	if len(parts) == 3 && parts[0] == "every" {
		n, e := strconv.Atoi(parts[1])
		if e != nil || n < 1 {
			return time.Time{}, errors.New("interval must be positive")
		}
		unit := time.Minute
		switch strings.TrimSuffix(parts[2], "s") {
		case "minute":
		case "hour":
			unit = time.Hour
		case "day":
			if n > 36500 {
				return time.Time{}, errors.New("interval exceeds supported range")
			}
			return after.In(loc).AddDate(0, 0, n).UTC(), nil
		default:
			return time.Time{}, errors.New("use every N minutes, hours, or days")
		}
		if n > 525600 {
			return time.Time{}, errors.New("interval exceeds supported range")
		}
		return after.Add(time.Duration(n) * unit), nil
	}
	if len(parts) > 0 && (parts[0] == "daily" || parts[0] == "weekly") {
		clock := ""
		weekday := -1
		if parts[0] == "daily" && len(parts) == 2 {
			clock = parts[1]
		} else if parts[0] == "weekly" && len(parts) == 3 {
			clock = parts[2]
			for i, v := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
				if parts[1] == v {
					weekday = i
				}
			}
			if weekday < 0 {
				return time.Time{}, errors.New("weekly requires SUN, MON, TUE, WED, THU, FRI, or SAT")
			}
		} else {
			return time.Time{}, errors.New("use daily HH:MM or weekly MON HH:MM")
		}
		c, e := time.Parse("15:04", clock)
		if e != nil {
			return time.Time{}, e
		}
		local := after.In(loc)
		for i := 0; i < 9; i++ {
			day := local.AddDate(0, 0, i)
			candidate := time.Date(day.Year(), day.Month(), day.Day(), c.Hour(), c.Minute(), 0, 0, loc)
			if candidate.Hour() != c.Hour() || candidate.Minute() != c.Minute() {
				continue
			}
			if weekday >= 0 && int(day.Weekday()) != weekday {
				continue
			}
			if candidate.After(after) {
				return candidate.UTC(), nil
			}
		}
		return time.Time{}, errors.New("cannot find next calendar occurrence")
	}
	at, e := time.Parse(time.RFC3339, rule)
	if e != nil {
		return time.Time{}, errors.New("use daily HH:MM, weekly MON HH:MM, every N minutes/hours/days, or an RFC3339 date")
	}
	if !at.After(after) {
		return time.Time{}, nil
	}
	return at.UTC(), nil
}
func (c *Calendar) List() ([]Schedule, error) {
	r, e := c.Store.List("calendar:schedules")
	if e != nil {
		return nil, e
	}
	return store.Decode[Schedule](r)
}
func (c *Calendar) Occurrences() ([]Occurrence, error) {
	r, e := c.Store.List("calendar:occurrences")
	if e != nil {
		return nil, e
	}
	return store.Decode[Occurrence](r)
}

func (c *Calendar) ReconcileActivation(now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ss, e := c.List()
	if e != nil {
		return e
	}
	for _, s := range ss {
		paused := s.Paused || c.Enabled != nil && !c.Enabled(s.BotID)
		if paused == s.Suspended {
			continue
		}
		s.Suspended = paused
		s.Next, e = Next(s.Recurrence, s.Timezone, now)
		if e != nil {
			return e
		}
		if e = c.Store.Put("calendar:schedules", s.ID, s); e != nil {
			return e
		}
	}
	return nil
}
func (c *Calendar) Save(s Schedule, now time.Time) (Schedule, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.BotID == "" || strings.TrimSpace(s.Prompt) == "" {
		return s, errors.New("bot and prompt are required")
	}
	if s.Timezone == "" || s.Timezone == "Local" {
		zone, e := LocalTimezone()
		if e != nil {
			return s, e
		}
		s.Timezone = zone
	}
	if s.ID == "" {
		s.ID = domain.ID()
		s.CreatedAt = now
	}
	next, e := Next(s.Recurrence, s.Timezone, now)
	if e != nil {
		return s, e
	}
	if next.IsZero() {
		return s, errors.New("one-time schedule must be in the future")
	}
	s.Next = next
	s.Suspended = false
	return s, c.Store.Put("calendar:schedules", s.ID, s)
}
func (c *Calendar) Delete(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.Store.Delete("calendar:schedules", id); e != nil {
		return e
	}
	occ, e := c.Occurrences()
	if e != nil {
		return e
	}
	for _, o := range occ {
		if o.ScheduleID == id && (o.Status == "missed" || o.Status == "queued") {
			o.Status = "skipped"
			if e = c.Store.Put("calendar:occurrences", o.ID, o); e != nil {
				return e
			}
		}
	}
	return nil
}
func (c *Calendar) DeleteBot(id string) error {
	ss, e := c.List()
	if e != nil {
		return e
	}
	for _, s := range ss {
		if s.BotID == id {
			if e = c.Delete(s.ID); e != nil {
				return e
			}
		}
	}
	return nil
}
func occurrence(s Schedule) Occurrence {
	hash := sha256.Sum256([]byte(s.ID + ":" + s.Next.UTC().Format(time.RFC3339Nano)))
	return Occurrence{ID: hex.EncodeToString(hash[:16]), ScheduleID: s.ID, BotID: s.BotID, Prompt: s.Prompt, IntendedAt: s.Next, Generation: s.Generation, Timezone: s.Timezone, Recurrence: s.Recurrence}
}
func (c *Calendar) Tick(now time.Time, offline bool) ([]Occurrence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ss, e := c.List()
	if e != nil {
		return nil, e
	}
	due := []Occurrence{}
	for _, s := range ss {
		originalNext, originalSuspended := s.Next, s.Suspended
		enabled := !s.Paused && (c.Enabled == nil || c.Enabled(s.BotID))
		if !enabled {
			if s.Suspended {
				continue
			}
			s.Suspended = true
			s.Next, e = Next(s.Recurrence, s.Timezone, now)
			if e != nil {
				return nil, e
			}
			if e = c.Store.Put("calendar:schedules", s.ID, s); e != nil {
				return nil, e
			}
			continue
		}
		if s.Suspended {
			s.Suspended = false
			s.Next, e = Next(s.Recurrence, s.Timezone, now)
			if e != nil {
				return nil, e
			}
		}
		for !s.Next.IsZero() && !s.Next.After(now) {
			o := occurrence(s)
			o.Status = "queued"
			if offline {
				o.Status = "missed"
			}
			created := false
			e = c.Store.Update("calendar:occurrences", o.ID, func(old json.RawMessage) (any, error) {
				if len(old) > 0 {
					return old, nil
				}
				created = true
				return o, nil
			})
			if e != nil {
				return nil, e
			}
			if created && !offline {
				due = append(due, o)
			}
			s.Next, e = Next(s.Recurrence, s.Timezone, s.Next)
			if e != nil {
				return nil, e
			}
		}
		if !s.Next.Equal(originalNext) || s.Suspended != originalSuspended {
			if e = c.Store.Put("calendar:schedules", s.ID, s); e != nil {
				return nil, e
			}
		}
	}
	return due, nil
}
func (c *Calendar) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	if _, e := c.Tick(domain.Now(), true); e != nil {
		return e
	}
	occ, e := c.Occurrences()
	if e != nil {
		return e
	}
	for _, o := range occ {
		if o.Status == "running" {
			o.Status = "interrupted"
			o.Error = "Echo stopped during execution; work was not replayed"
			c.Store.Put("calendar:occurrences", o.ID, o)
		} else if o.Status == "queued" {
			o.Status = "missed"
			c.Store.Put("calendar:occurrences", o.ID, o)
		}
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				due, e := c.Tick(now, false)
				if e != nil {
					continue
				}
				for _, o := range due {
					c.wg.Add(1)
					go func(o Occurrence) { defer c.wg.Done(); c.run(ctx, o) }(o)
				}
			}
		}
	}()
	return nil
}
func (c *Calendar) run(ctx context.Context, o Occurrence) {
	claimed := false
	e := c.Store.Update("calendar:occurrences", o.ID, func(raw json.RawMessage) (any, error) {
		var current Occurrence
		if json.Unmarshal(raw, &current) != nil {
			return nil, errors.New("missing occurrence")
		}
		if current.Status != "queued" {
			return current, nil
		}
		current.Status = "running"
		o = current
		claimed = true
		return current, nil
	})
	if e != nil || !claimed {
		return
	}
	if c.Enabled != nil && !c.Enabled(o.BotID) {
		o.Status = "skipped"
	} else if c.Dispatch == nil {
		o.Status = "failed"
		o.Error = "dispatcher unavailable"
	} else if e = c.Dispatch(ctx, o); e != nil {
		o.Status = "failed"
		o.Error = e.Error()
	} else {
		o.Status = "completed"
	}
	c.Store.Put("calendar:occurrences", o.ID, o)
	if c.Changed != nil {
		c.Changed()
	}
}
func (c *Calendar) Review(ctx context.Context, ids []string, concurrency int) error {
	if concurrency < 0 {
		return errors.New("concurrency must be zero or greater")
	}
	c.mu.Lock()
	all, e := c.Occurrences()
	if e != nil {
		c.mu.Unlock()
		return e
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	available := map[string]bool{}
	for _, o := range all {
		if o.Status == "missed" {
			available[o.ID] = true
		}
	}
	for id := range selected {
		if !available[id] {
			c.mu.Unlock()
			return errors.New("some selected jobs are no longer awaiting review")
		}
	}
	batch := []Occurrence{}
	updates := map[string]any{}
	for _, o := range all {
		if o.Status != "missed" {
			continue
		}
		if selected[o.ID] {
			o.Status = "queued"
			batch = append(batch, o)
			delete(selected, o.ID)
		} else {
			o.Status = "skipped"
		}
		updates[o.ID] = o
	}
	if e = c.Store.PutBatch("calendar:occurrences", updates); e != nil {
		c.mu.Unlock()
		return e
	}
	c.mu.Unlock()
	if len(selected) > 0 {
		return fmt.Errorf("some selected jobs are no longer awaiting review")
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if concurrency == 0 {
			concurrency = len(batch)
		}
		if concurrency < 1 {
			return
		}
		jobs := make(chan Occurrence)
		var workers sync.WaitGroup
		for i := 0; i < concurrency && i < len(batch); i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for o := range jobs {
					c.run(ctx, o)
				}
			}()
		}
		for _, o := range batch {
			select {
			case jobs <- o:
			case <-ctx.Done():
				close(jobs)
				workers.Wait()
				return
			}
		}
		close(jobs)
		workers.Wait()
		if c.Changed != nil {
			c.Changed()
		}
	}()
	return nil
}
func (c *Calendar) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}
