package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func TestNotesRevisionPersistenceAndDeletion(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	note, err := s.MutateNote(domain.Note{Title: "Private", Markdown: "#plan"}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if note.Revision != 1 || note.CreatedAt == "" {
		t.Fatal(note)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Note(note.ID)
	if err != nil || loaded.Markdown != "#plan" {
		t.Fatal(loaded, err)
	}
	loaded.Markdown = "new"
	updated, err := s.MutateNote(loaded, false, false)
	if err != nil || updated.Revision != 2 || updated.CreatedAt != loaded.CreatedAt {
		t.Fatal(updated, err)
	}
	if _, err = s.MutateNote(loaded, false, false); !errors.Is(err, ErrNoteConflict) {
		t.Fatal("stale save", err)
	}
	if _, err = s.MutateNote(loaded, false, true); !errors.Is(err, ErrNoteConflict) {
		t.Fatal("stale deletion", err)
	}
	if _, err = s.MutateNote(updated, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MutateNote(updated, false, false); !errors.Is(err, ErrNoteMissing) {
		t.Fatal("late save resurrected note", err)
	}
}

func TestNotesConcurrentSaveAndDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, remove := range []bool{false, true} {
		note, err := s.MutateNote(domain.Note{Title: "Race"}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(deleteNote bool) {
				defer wg.Done()
				_, err := s.MutateNote(note, false, deleteNote)
				results <- err
			}(remove && i == 1)
		}
		wg.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, ErrNoteConflict) && !errors.Is(err, ErrNoteMissing) {
				t.Fatal(err)
			}
		}
		if successes != 1 {
			t.Fatalf("expected one committed operation, got %d", successes)
		}
	}
}
