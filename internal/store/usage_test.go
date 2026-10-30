package store

import (
	"sync"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestUsageDurabilityDeduplicationAndReset(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	initial, err := s.Usage()
	if err != nil || initial.ModelCalls != 0 || initial.TrackingStartedAt.IsZero() {
		t.Fatal(initial, err)
	}
	run := domain.Run{ID: "run", Statistics: &domain.TurnStatistics{StartedAt: domain.Now()}}
	if err = s.Put("runs", run.ID, run); err != nil {
		t.Fatal(err)
	}
	u := &domain.TokenUsage{InputTokens: 100, OutputTokens: 20}
	run.Statistics.Add(u)
	for range 2 {
		if err = s.RecordModelUsage("one", u, run.ID, run.Statistics); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordModelUsage("two", &domain.TokenUsage{OutputTokens: 5, InputMissing: true}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordModelUsage("three", nil, "", nil); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Usage()
	if before.InputTokens != 100 || before.OutputTokens != 25 || before.ModelCalls != 3 || before.InputReports != 1 || before.OutputReports != 2 {
		t.Fatal(before)
	}
	reset, err := s.ResetUsage("reset-one")
	if err != nil || reset.ModelCalls != 0 || reset.InputTokens != 0 || reset.Revision <= before.Revision {
		t.Fatal(reset, err)
	}
	// A request started before reset but committed after it belongs to the new totals.
	if err = s.RecordModelUsage("four", u, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResetUsage("reset-one"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Usage()
	if after.ModelCalls != 1 || after.InputTokens != 100 {
		t.Fatal(after)
	}
	var saved domain.Run
	if err = s.Get("runs", run.ID, &saved); err != nil || saved.Statistics.InputTokens != 100 {
		t.Fatal(saved, err)
	}
	// Removing transcript/run data must not erase machine consumption.
	if err = s.Delete("runs", run.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened, _ := s.Usage()
	if reopened != after {
		t.Fatal(reopened, after)
	}
	if err = s.RecordModelUsage("one", u, "", nil); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := s.Usage()
	if duplicate != after {
		t.Fatal(duplicate)
	}
}

func TestUsageConcurrentRecordingAndReset(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.RecordModelUsage(domain.ID(), &domain.TokenUsage{InputTokens: 2, OutputTokens: 3}, "", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := s.ResetUsage("concurrent"); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	current, err := s.Usage()
	if err != nil || current.InputTokens != current.ModelCalls*2 || current.OutputTokens != current.ModelCalls*3 || current.ModelCalls > 40 {
		t.Fatal(current, err)
	}
	var ledger usageLedger
	if err = s.Get("usage", "totals", &ledger); err != nil {
		t.Fatal(err)
	}
	if ledger.ModelCalls != 40 || ledger.InputTokens != 80 || ledger.OutputTokens != 120 {
		t.Fatal(ledger)
	}
}
