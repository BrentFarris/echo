package editorfs

import (
	"github.com/brent/echo/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestBotIsolationRevocationAndPrivateState(t *testing.T) {
	base := t.TempDir()
	private := filepath.Join(base, "private")
	os.Mkdir(private, 0700)
	os.WriteFile(filepath.Join(private, "secret.txt"), []byte("secret"), 0600)
	os.WriteFile(filepath.Join(base, "public.txt"), []byte("public"), 0600)
	other := t.TempDir()
	bots := map[string]domainBotRoots{"a": {base}, "b": {other}, "empty": {}}
	service := New(func(id string) (domain.Bot, error) {
		roots, ok := bots[id]
		if !ok {
			return domain.Bot{}, ErrNotFound
		}
		return domain.Bot{ID: id, Roots: []string(roots)}, nil
	}, private)
	defer service.Close()
	roots, _ := service.Roots("a")
	ref := FileRef{RootID: roots[0].ID, Path: "public.txt"}
	if _, err := service.Read("b", ref); err == nil {
		t.Fatal("cross-bot reference accepted")
	}
	empty, err := service.Roots("empty")
	if err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	for _, path := range []string{"../secret.txt", "private/secret.txt"} {
		if _, err := service.Read("a", FileRef{RootID: ref.RootID, Path: path}); err == nil {
			t.Fatal("read forbidden path", path)
		}
	}
	entries, err := service.List("a", FileRef{RootID: ref.RootID})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "private" {
			t.Fatal("private folder listed")
		}
	}
	result := waitForSearch(t, service, "a", "secret", false)
	if len(result.Items) > 0 {
		t.Fatal("private state indexed", result)
	}
	bots["a"] = nil
	if result := service.Search("a", "public", 20); len(result.Items) != 0 {
		t.Fatal("revoked root remained indexed", result)
	}
	if _, err := service.Save("a", SaveRequest{Ref: ref, Content: "bad", CreateOnly: true}); err == nil {
		t.Fatal("revoked root accepted")
	}
	delete(bots, "a")
	if _, err := service.Roots("a"); err == nil {
		t.Fatal("deleted bot accepted")
	}
}

func TestMediaLimitAndPrivateAncestorMutation(t *testing.T) {
	service, id, base, root := newTestService(t)
	file, err := os.Create(filepath.Join(base, "huge.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(MaxMediaBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, _, _, _, err = service.MediaMeta(id, FileRef{RootID: root.ID, Path: "huge.mp4"}); err == nil {
		t.Fatal("oversized video accepted")
	}
	private := filepath.Join(base, "outer", "private")
	os.MkdirAll(private, 0700)
	service.private = private
	os.WriteFile(filepath.Join(private, "secret"), []byte("private"), 0600)
	if _, err := service.Trash(id, FileRef{RootID: root.ID, Path: "outer"}); err == nil {
		t.Fatal("private-state ancestor was trashed")
	}
	if _, err := service.Rename(id, FileRef{RootID: root.ID, Path: "outer"}, "moved"); err == nil {
		t.Fatal("private-state ancestor was renamed")
	}
}

type domainBotRoots []string

func TestJunctionEscapeAndPrivateAlias(t *testing.T) {
	service, id, base, root := newTestService(t)
	for _, target := range []string{t.TempDir(), service.private} {
		os.MkdirAll(target, 0700)
		os.WriteFile(filepath.Join(target, "secret.txt"), []byte("secret"), 0600)
		link := filepath.Join(base, "alias")
		var err error
		if runtime.GOOS == "windows" {
			_, err = exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		} else {
			err = os.Symlink(target, link)
		}
		if err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := service.Read(id, FileRef{RootID: root.ID, Path: "alias/secret.txt"}); err == nil {
			t.Fatal("alias escaped root")
		}
		if _, _, err := service.Create(id, CreateRequest{Parent: FileRef{RootID: root.ID, Path: "alias"}, Name: "new.txt", Kind: "file"}); err == nil {
			t.Fatal("write escaped root")
		}
		os.Remove(link)
	}
}

func TestCreateOnlyAndTrashCollision(t *testing.T) {
	service, id, _, root := newTestService(t)
	ref := FileRef{RootID: root.ID, Path: "new.txt"}
	var wg sync.WaitGroup
	success := make(chan bool, 2)
	for range 2 {
		wg.Go(func() {
			_, err := service.Save(id, SaveRequest{Ref: ref, Content: "original", CreateOnly: true})
			success <- err == nil
		})
	}
	wg.Wait()
	close(success)
	count := 0
	for ok := range success {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatal("create-only winners", count)
	}
	item, err := service.Trash(id, ref)
	if err != nil {
		t.Fatal(err)
	}
	service.Save(id, SaveRequest{Ref: ref, Content: "replacement", CreateOnly: true})
	if _, err := service.Restore(id, item.ID); err == nil {
		t.Fatal("restore overwrote occupied path")
	}
	snapshot, err := service.Read(id, ref)
	if err != nil || snapshot.Content != "replacement" {
		t.Fatal(snapshot, err)
	}
	if err := service.PurgeTrash(id, item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherReferenceCleanup(t *testing.T) {
	service, id, _, _ := newTestService(t)
	watch := NewWatchManager(service, func(WatchEvent) {})
	defer watch.Close()
	if err := watch.Subscribe(id); err != nil {
		t.Fatal(err)
	}
	if err := watch.Subscribe(id); err != nil {
		t.Fatal(err)
	}
	watch.Unsubscribe(id)
	watch.mu.Lock()
	count := len(watch.workspaces)
	watch.mu.Unlock()
	if count != 1 {
		t.Fatal(count)
	}
	watch.Unsubscribe(id)
	watch.mu.Lock()
	count = len(watch.workspaces)
	watch.mu.Unlock()
	if count != 0 {
		t.Fatal("watcher leaked", count)
	}
}
