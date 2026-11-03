package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func TestBotIsolationFoldersAndRevocation(t *testing.T) {
	s, bot, _ := newTestService(t)
	root := filepath.Join(t.TempDir(), "space ' quote 日本")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	bot.Roots = append(bot.Roots, root)
	if err := s.data.Put("bots", bot.ID, bot); err != nil {
		t.Fatal(err)
	}
	other := domain.Bot{ID: "other", Handle: "other", Roots: []string{t.TempDir()}}
	if err := s.data.Put("bots", other.ID, other); err != nil {
		t.Fatal(err)
	}
	backends := []*fakeBackend{}
	s.SetBackendFactory(func() (Backend, error) { b := newFakeBackend(); backends = append(backends, b); return b, nil })
	a, err := s.Start(bot.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Start(other.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("bots share a shell")
	}
	if err = s.Write(other.ID, a.ID, "bad"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("cross-bot write", err)
	}
	if err = s.ChangeFolder(bot.ID, a.ID, root); err != nil {
		t.Fatal(err)
	}
	command, _ := DirectoryCommand(a.Shell, root)
	if backends[0].written() != command+"\r" {
		t.Fatalf("directory input %q", backends[0].written())
	}
	if err = s.ChangeFolder(bot.ID, a.ID, other.Roots[0]); err == nil {
		t.Fatal("unconfigured shortcut accepted")
	}
	if err = os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeFolder(bot.ID, a.ID, root); err == nil {
		t.Fatal("missing folder accepted")
	}
	if _, err = s.SaveCommand(bot.ID, "", "private", "echo hello"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSavedCommands(other.ID)
	if err != nil || len(list) != 0 {
		t.Fatal("command lists shared", list, err)
	}
	if err = s.data.Delete("bots", bot.ID); err != nil {
		t.Fatal(err)
	}
	s.Reconcile()
	if backends[0].closeCount() != 1 {
		t.Fatal("deleted bot process remains")
	}
	if snapshot, err := s.Sync(other.ID, b.ID, 0); err != nil || snapshot.Status != "running" {
		t.Fatal("other bot affected", snapshot, err)
	}
}

func TestDirectoryCommandQuoting(t *testing.T) {
	for _, c := range []struct{ shell, path, want string }{
		{"PowerShell", `D:\space ' quoted\日本`, `Set-Location -LiteralPath 'D:\space '' quoted\日本'`},
		{"bash", `/tmp/space ' $(echo bad)`, `cd -- '/tmp/space '\'' $(echo bad)'`},
	} {
		got, err := DirectoryCommand(c.shell, c.path)
		if err != nil || got != c.want {
			t.Fatalf("%q: %q, %v; want %q", c.shell, got, err, c.want)
		}
	}
	for _, path := range []string{"bad\rpath", "bad\npath", "bad\x00path"} {
		if _, err := DirectoryCommand("sh", path); err == nil {
			t.Fatal("control accepted")
		}
	}
}

func TestStartingDirectoryAndConcurrentStart(t *testing.T) {
	s, bot, _ := newTestService(t)
	root := bot.Roots[0]
	bot.Roots = []string{filepath.Join(root, "missing"), root}
	if dir, err := availableWorkingDirectory(bot); err != nil || dir != root {
		t.Fatal(dir, err)
	}
	bot.Roots = bot.Roots[:1]
	if _, err := availableWorkingDirectory(bot); err == nil {
		t.Fatal("unavailable configured root silently replaced")
	}
	bot.Roots = nil
	home, _ := os.UserHomeDir()
	if dir, err := availableWorkingDirectory(bot); err != nil || dir != home {
		t.Fatal(dir, err)
	}
	count := 0
	s.SetBackendFactory(func() (Backend, error) { count++; return newFakeBackend(), nil })
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := s.Start(bot.ID, 80, 24); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if count != 1 {
		t.Fatal("duplicate PTYs", count)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(bot.ID, 80, 24); err == nil {
		t.Fatal("started after shutdown")
	}
}

func TestNativeTerminalRoundTrip(t *testing.T) {
	s, bot, _ := newTestService(t)
	snap, err := s.Start(bot.ID, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	command := "printf 'native-%s\\n' terminal; exit 7\r"
	if runtime.GOOS == "windows" {
		command = "Write-Output ('native-' + 'terminal'); exit 7\r"
	}
	if err = s.Write(bot.ID, snap.ID, command); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Sync(bot.ID, snap.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "exited" {
			var output strings.Builder
			for _, chunk := range got.Output {
				b, _ := base64.StdEncoding.DecodeString(chunk.Data)
				output.Write(b)
			}
			if got.ExitCode == nil || *got.ExitCode != 7 || !strings.Contains(output.String(), "native-terminal") {
				t.Fatalf("native result %+v output %q", got, output.String())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native shell did not exit")
}
