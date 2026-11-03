package development

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/store"
)

func updateFixture(t *testing.T) (*Manager, *store.Store, string, string, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	remote, repo := repositoryTest(t)
	writeTest(t, remote+"/.gitignore", ".cache/\n")
	commitTest(t, remote, "ignore build outputs")
	gitTest(t, repo, "pull", "--ff-only")
	writeTest(t, remote+"/version.txt", "target")
	commitTest(t, remote, "target")
	target := gitTest(t, remote, "rev-parse", "HEAD")
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := New(context.Background(), t.TempDir(), Config{RepositoryPath: repo}, func() {})
	t.Cleanup(m.Close)
	idle, drain, builds := &atomic.Bool{}, &atomic.Bool{}, &atomic.Int32{}
	if err = m.ConfigureUpdates(db, drain.Store, idle.Load); err != nil {
		t.Fatal(err)
	}
	m.run = func(ctx context.Context, dir string, w io.Writer, name string, args ...string) error {
		if name == "git" {
			return runCommand(ctx, dir, w, name, args...)
		}
		builds.Add(1)
		return os.WriteFile(args[len(args)-1], []byte("staged executable"), 0700)
	}
	return m, db, remote, target, drain, builds
}

func awaitUpdate(t *testing.T, m *Manager, id, status string) Update {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		u, err := m.UpdateStatus("owner", id)
		if err == nil && u.Status == status {
			return u
		}
		if err == nil && UpdateTerminal(u.Status) {
			t.Fatalf("wanted %s, got %+v", status, u)
		}
		time.Sleep(20 * time.Millisecond)
	}
	u, _ := m.UpdateStatus("owner", id)
	t.Fatalf("timed out waiting for %s: %+v", status, u)
	return u
}

func TestUpdatePinnedRevisionDrainAndRestartJournal(t *testing.T) {
	m, db, remote, target, drain, builds := updateFixture(t)
	// The upstream advances after the owner chose the commit. Every host must
	// still prepare that immutable commit, not today's upstream tip.
	writeTest(t, remote+"/later.txt", "later")
	commitTest(t, remote, "later")
	if _, err := m.PrepareUpdate("owner", "pin", target); err != nil {
		t.Fatal(err)
	}
	u := awaitUpdate(t, m, "pin", "ready")
	if gitTest(t, m.config.RepositoryPath, "rev-parse", "HEAD") != target || !u.CheckoutUpdated {
		t.Fatal(u)
	}
	if _, err := m.PrepareUpdate("owner", "pin", target); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 1 {
		t.Fatal("repeated accepted build")
	}
	if _, err := m.ControlUpdate("other", "pin", "activate"); err == nil {
		t.Fatal("accepted foreign coordinator")
	}
	if _, err := m.Start("relaunch"); !errors.Is(err, ErrBusy) {
		t.Fatal("overlapping development operation", err)
	}
	launched := make(chan launchSpec, 1)
	m.launch = func(spec launchSpec, _ string) error { launched <- spec; return nil }
	if _, err := m.ControlUpdate("owner", "pin", "activate"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !drain.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !drain.Load() {
		t.Fatal("admission was not drained")
	}
	select {
	case <-launched:
		t.Fatal("restarted active work")
	default:
	}
	// No test goroutine changes manager callbacks while execution is active.
	m.mu.Lock()
	m.update.Lease = time.Now().Add(time.Minute)
	m.mu.Unlock()
	m.Close()
	u, _ = m.UpdateStatus("owner", "pin")
	if u.Status != "failed" || drain.Load() {
		t.Fatal("crash did not release drain", u)
	}
	// Simulate the durable intent left immediately before a successful launch.
	u.Status = "restarting"
	u.Revision = "old"
	u.InstanceID = "old-process"
	if err := db.Put("development-updates", "pin", u); err != nil {
		t.Fatal(err)
	}
	replacement := New(context.Background(), t.TempDir(), Config{}, func() {})
	defer replacement.Close()
	replacement.revision = target
	if err := replacement.ConfigureUpdates(db, func(bool) {}, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	verified, err := replacement.UpdateStatus("owner", "pin")
	if err != nil || verified.Status != "succeeded" || verified.Revision != target {
		t.Fatal(verified, err)
	}
}

func TestUpdateCancellationLeaseAndSourceChanges(t *testing.T) {
	for _, mode := range []string{"cancel", "lease", "source"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _, target, drain, _ := updateFixture(t)
			if _, err := m.PrepareUpdate("owner", "update", target); err != nil {
				t.Fatal(err)
			}
			awaitUpdate(t, m, "update", "ready")
			m.launch = func(launchSpec, string) error {
				t.Error("unexpected launcher")
				return errors.New("unexpected launcher")
			}
			switch mode {
			case "cancel":
				_, _ = m.ControlUpdate("owner", "update", "activate")
				_, _ = m.ControlUpdate("owner", "update", "cancel")
				awaitUpdate(t, m, "update", "cancelled")
			case "lease":
				m.mu.Lock()
				m.update.Lease = time.Now().Add(-time.Second)
				m.mu.Unlock()
				awaitUpdate(t, m, "update", "failed")
			case "source":
				writeTest(t, filepath.Join(m.config.RepositoryPath, "version.txt"), "owner edit")
				m.idle = func() bool { return true }
				_, _ = m.ControlUpdate("owner", "update", "activate")
				u := awaitUpdate(t, m, "update", "failed")
				if !strings.Contains(u.Error, "local edits") {
					t.Fatal(u)
				}
			}
			if drain.Load() {
				t.Fatal("drain stuck after failure/cancellation")
			}
		})
	}
}

func TestUpdatePreflightPreservesDirtyAndDivergedCheckout(t *testing.T) {
	m, _, _, target, _, _ := updateFixture(t)
	ctx := context.Background()
	writeTest(t, filepath.Join(m.config.RepositoryPath, "untracked.txt"), "keep")
	check := m.CheckUpdate(ctx, target)
	if check.Ready || !strings.Contains(check.Error, "local edits") {
		t.Fatal(check)
	}
	commitTest(t, m.config.RepositoryPath, "local branch change")
	check = m.CheckUpdate(ctx, target)
	if check.Ready || !strings.Contains(check.Error, "diverged") {
		t.Fatal(check)
	}
	content, err := os.ReadFile(filepath.Join(m.config.RepositoryPath, "untracked.txt"))
	if err != nil || string(content) != "keep" {
		t.Fatal("edits changed")
	}
}

func TestUpdateLaunchFailureAndWrongReplacementRevision(t *testing.T) {
	m, db, _, target, drain, _ := updateFixture(t)
	m.idle = func() bool { return true }
	m.launch = func(launchSpec, string) error { return errors.New("launcher unavailable") }
	_, err := m.PrepareUpdate("owner", "failure", target)
	if err != nil {
		t.Fatal(err)
	}
	awaitUpdate(t, m, "failure", "ready")
	_, _ = m.ControlUpdate("owner", "failure", "activate")
	u := awaitUpdate(t, m, "failure", "failed")
	if !strings.Contains(u.Error, "launcher unavailable") || drain.Load() {
		t.Fatal(u)
	}
	u.Status = "restarting"
	if err = db.Put("development-updates", u.ID, u); err != nil {
		t.Fatal(err)
	}
	replacement := New(context.Background(), t.TempDir(), Config{}, func() {})
	defer replacement.Close()
	replacement.revision = "wrong"
	if err = replacement.ConfigureUpdates(db, func(bool) {}, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	u, _ = replacement.UpdateStatus("owner", u.ID)
	if u.Status != "failed" {
		t.Fatal("wrong binary counted as success", u)
	}
}

func TestUpdateCancellationFencesDelayedPrepare(t *testing.T) {
	m, _, _, target, _, builds := updateFixture(t)
	u, err := m.ControlUpdate("owner", "delayed", "cancel")
	if err != nil || u.Status != "cancelled" {
		t.Fatal(u, err)
	}
	u, err = m.PrepareUpdate("owner", "delayed", target)
	if err != nil || u.Status != "cancelled" {
		t.Fatal("late prepare escaped cancellation", u, err)
	}
	if builds.Load() != 0 {
		t.Fatal("cancelled preparation executed")
	}
	if _, err = m.PrepareUpdate("other", "delayed", target); err == nil {
		t.Fatal("cancellation ownership bypass")
	}
}
