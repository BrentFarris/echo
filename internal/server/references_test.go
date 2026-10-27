package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
)

func TestReferencesMergeRankAndEnforceRecipients(t *testing.T) {
	s := testServer(t)
	root, otherRoot := t.TempDir(), t.TempDir()
	for _, path := range []string{filepath.Join(root, "src", "tools"), filepath.Join(otherRoot, "tools-extra")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	bots := []domain.Bot{
		{ID: "kaiju", Handle: "kaiju", Name: "Kaiju", Roots: []string{root}},
		{ID: "backend", Handle: "backend", Name: "Backend", Roots: []string{otherRoot, filepath.Join(root, "src")}},
	}
	for _, bot := range bots {
		if err := s.Store.Put("bots", bot.ID, bot); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	var entries []broker.Entry
	for {
		response := scmAPI(t, s, "POST", "/references", map[string]any{"content": "ask @kaiju to work with @backend #tools", "query": "tools"})
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &entries); err != nil {
			t.Fatal(err)
		}
		if response.Header().Get("X-Echo-Reference-Incomplete") != "false" {
			t.Fatal("fixture search incomplete")
		}
		if response.Header().Get("X-Echo-Reference-Indexing") == "false" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("index did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(entries) != 2 || entries[0].Name != "tools" {
		t.Fatalf("wrong ranking or duplicates: %v", entries)
	}
	if !slices.Equal(entries[0].AllowedBots, []string{"kaiju", "backend"}) || !slices.Equal(entries[1].AllowedBots, []string{"backend"}) {
		t.Fatalf("wrong bot access labels: %v", entries)
	}
	// Cached results must not survive scope revocation or reveal unrelated roots.
	bots[0].Roots = []string{t.TempDir()}
	if err := s.Store.Put("bots", bots[0].ID, bots[0]); err != nil {
		t.Fatal(err)
	}
	response := scmAPI(t, s, "POST", "/references", map[string]any{"content": "@kaiju #tools", "query": "tools"})
	if response.Code != 200 || response.Body.String() != "[]\n" {
		t.Fatalf("stale scope exposed: %s", response.Body.String())
	}
	response = scmAPI(t, s, "POST", "/references", map[string]any{"content": "@missing #tools", "query": "tools"})
	if response.Code == 200 {
		t.Fatal("unknown bot silently fell back to Echo")
	}
}

func TestFileMutationsInvalidateReferences(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	bot := domain.Bot{ID: "kaiju", Roots: []string{root}}
	for {
		_, status, err := s.References.Search(context.Background(), bot, root, "")
		if err != nil {
			t.Fatal(err)
		}
		if !status.Indexing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := s.mutateFile(context.Background(), bot, "file.mkdir", map[string]any{"path": "src/new-folder"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, status, err := s.References.Search(context.Background(), bot, root, "new-folder")
		if err != nil {
			t.Fatal(err)
		}
		if !status.Indexing {
			if len(entries) != 1 {
				t.Fatalf("mutation was not indexed: %v", entries)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("index did not refresh")
		}
		time.Sleep(time.Millisecond)
	}
}
