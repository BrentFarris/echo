package broker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func referenceFixture(t *testing.T, root, path string, directory bool) string {
	t.Helper()
	path = filepath.Join(root, filepath.FromSlash(path))
	parent := filepath.Dir(path)
	if directory {
		parent = path
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if !directory {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func indexedReferences(t *testing.T, index *ReferenceIndex, bot domain.Bot, root, query string) []Entry {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		entries, status, err := index.Search(context.Background(), bot, root, query)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Indexing {
			if status.Incomplete {
				t.Fatal("fixture index was incomplete")
			}
			return entries
		}
		if time.Now().After(deadline) {
			t.Fatal("index did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReferenceIndexFindsLateEntriesAndRanksBeforeLimiting(t *testing.T) {
	root := referenceFixture(t, t.TempDir(), "kaiju", true)
	for n := 0; n < 10020; n++ {
		referenceFixture(t, root, fmt.Sprintf("a-bulk/tools-%05d.txt", n), false)
	}
	tools := referenceFixture(t, root, "z/game/tools", true)
	editor := referenceFixture(t, root, "z/game/map_editor", true)
	referenceFixture(t, root, "z/game/map_editor.go", false)
	bot := domain.Bot{Roots: []string{root}}
	index := NewReferenceIndex(context.Background(), &Files{})
	defer index.Close()
	for _, query := range []string{"tools", "TOOLS", "z/game/tools", `z\game\tools`, "kaiju/z/game/tools", tools} {
		entries := indexedReferences(t, index, bot, root, query)
		if len(entries) == 0 || entries[0].Path != tools {
			t.Fatalf("query %q missed exact late folder: %v", query, entries)
		}
		if len(entries) > ReferenceLimit {
			t.Fatalf("unbounded displayed results: %d", len(entries))
		}
	}
	for _, query := range []string{"map_editor", "map edit", "mpedtr"} {
		entries := indexedReferences(t, index, bot, root, query)
		if len(entries) == 0 || entries[0].Path != editor {
			t.Fatalf("query %q missed %s: %v", query, editor, entries)
		}
	}
	entries := indexedReferences(t, index, bot, root, "kaiju")
	if len(entries) == 0 || entries[0].Path != root {
		t.Fatalf("workspace root not searchable: %v", entries)
	}
}

func TestReferenceIndexDirectPathsFreshnessAndScope(t *testing.T) {
	root := referenceFixture(t, t.TempDir(), "kaiju", true)
	private := referenceFixture(t, root, "private", true)
	referenceFixture(t, private, "secret.txt", false)
	outside := referenceFixture(t, t.TempDir(), "outside.txt", false)
	files := &Files{Private: private}
	bot := domain.Bot{Roots: []string{root}}
	index := NewReferenceIndex(context.Background(), files)
	defer index.Close()
	indexedReferences(t, index, bot, root, "")
	path := referenceFixture(t, root, "node_modules/a package/child file.ts", false)
	for _, query := range []string{path, `node_modules\a package\child file.ts`, "kaiju/node_modules/a package/child file.ts"} {
		entries, _, err := index.Search(context.Background(), bot, root, query)
		if err != nil || len(entries) == 0 || entries[0].Path != path {
			t.Fatalf("direct lookup %q: %v %v", query, entries, err)
		}
	}
	for _, query := range []string{outside, private, "secret"} {
		entries, _, err := index.Search(context.Background(), bot, root, query)
		if err != nil || len(entries) != 0 {
			t.Fatalf("scope leak for %q: %v %v", query, entries, err)
		}
	}
	if _, _, err := index.Search(context.Background(), domain.Bot{Roots: []string{filepath.Dir(outside)}}, root, ""); err == nil {
		t.Fatal("reused cached root outside the bot's grant")
	}
	added := referenceFixture(t, root, "src/fresh.go", false)
	index.Invalidate([]string{added})
	entries := indexedReferences(t, index, bot, root, "fresh")
	if len(entries) != 1 || entries[0].Path != added {
		t.Fatalf("new file not indexed: %v", entries)
	}
	if err := os.Remove(added); err != nil {
		t.Fatal(err)
	}
	entries, _, err := index.Search(context.Background(), bot, root, "fresh")
	if err != nil || len(entries) != 0 {
		t.Fatalf("stale removed file offered: %v %v", entries, err)
	}
	index.Close()
	if _, _, err := index.Search(context.Background(), bot, root, "fresh"); err == nil {
		t.Fatal("closed index restarted")
	}
}

func TestReferenceIndexRejectsJunctionAndSymlinkEscapes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	referenceFixture(t, outside, "secret.txt", false)
	link := filepath.Join(root, "escape")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("junction fixture: %v %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	index := NewReferenceIndex(context.Background(), &Files{})
	defer index.Close()
	bot := domain.Bot{Roots: []string{root}}
	for _, query := range []string{"escape", "secret", filepath.Join(link, "secret.txt")} {
		entries := indexedReferences(t, index, bot, root, query)
		if len(entries) != 0 {
			t.Fatalf("escaped workspace via %q: %v", query, entries)
		}
	}
}

func TestReferenceAccessRejectsPrivateAliasesForBoundAndUnboundBots(t *testing.T) {
	root := t.TempDir()
	private := referenceFixture(t, root, "private", true)
	secret := referenceFixture(t, private, "secret.txt", false)
	public := referenceFixture(t, root, "public.txt", false)
	alias := filepath.Join(root, "alias")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", alias, private).CombinedOutput(); err != nil {
			t.Fatalf("junction fixture: %v %s", err, output)
		}
	} else if err := os.Symlink(private, alias); err != nil {
		t.Fatal(err)
	}
	files := &Files{Private: private}
	for _, bot := range []domain.Bot{{Roots: []string{root}}, {}} {
		entries := files.FilterReferences(context.Background(), bot, []Entry{{Path: secret}, {Path: alias}, {Path: filepath.Join(alias, "secret.txt")}, {Path: public}})
		if len(entries) != 1 || entries[0].Path != public {
			t.Fatalf("private name disclosed: %v", entries)
		}
	}
}

func TestReferenceIndexCancellationAndPartialStatus(t *testing.T) {
	root := t.TempDir()
	index := NewReferenceIndex(context.Background(), &Files{})
	defer index.Close()
	bot := domain.Bot{Roots: []string{root}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := index.Search(ctx, bot, root, ""); err == nil {
		t.Fatal("cancelled search succeeded")
	}
	indexedReferences(t, index, bot, root, "")
	index.mu.Lock()
	index.roots[root].incomplete = true
	index.mu.Unlock()
	_, status, err := index.Search(context.Background(), bot, root, "")
	if err != nil || !status.Incomplete {
		t.Fatalf("partial search not reported: %v %v", status, err)
	}
}

func TestDirectReferencesStayShallowForUnboundBots(t *testing.T) {
	root := t.TempDir()
	path := referenceFixture(t, root, "folder with spaces/nested/file.go", false)
	files := &Files{}
	entries, err := files.DirectReferences(context.Background(), domain.Bot{}, root, filepath.Join(root, "folder"))
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Path, "folder with spaces") {
		t.Fatalf("shallow completion: %v %v", entries, err)
	}
	entries, err = files.DirectReferences(context.Background(), domain.Bot{}, root, path)
	if err != nil || len(entries) == 0 || entries[0].Path != path {
		t.Fatalf("absolute lookup: %v %v", entries, err)
	}
}
