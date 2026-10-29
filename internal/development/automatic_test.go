package development

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func nextAutomaticOperation(t *testing.T, m *Manager, previous string) Operation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		op := m.op
		if op != nil && op.ID != previous && op.Status != "running" {
			result := *op
			m.mu.Unlock()
			if result.Status != "succeeded" {
				t.Fatalf("check failed: %+v", result)
			}
			return result
		}
		m.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("automatic check did not complete")
	return Operation{}
}

func automaticTick(t *testing.T, ticks chan time.Time) {
	t.Helper()
	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("automatic scheduler stopped")
	}
}

func TestAutomaticChecksStartupHourlyAndPreference(t *testing.T) {
	remote, repo := repositoryTest(t)
	original := gitTest(t, repo, "rev-parse", "HEAD")
	m := managerTest(t, repo)
	m.config.AutoCheckUpdates = true
	m.revision = original
	ticks := make(chan time.Time)
	m.startAutomaticChecks(ticks)
	first := nextAutomaticOperation(t, m, "")
	if m.UpdateNotice().UpdateAvailable {
		t.Fatal("current executable marked outdated")
	}

	writeTest(t, filepath.Join(remote, "main.go"), "new upstream\n")
	commitTest(t, remote, "new upstream")
	automaticTick(t, ticks)
	second := nextAutomaticOperation(t, m, first.ID)
	if !m.UpdateNotice().UpdateAvailable {
		t.Fatal("hourly check missed update")
	}
	if gitTest(t, repo, "rev-parse", "HEAD") != original {
		t.Fatal("automatic check changed checkout")
	}

	var saved Config
	persist := func(c Config) error { saved = c; return nil }
	if err := m.Save(context.Background(), Config{RepositoryPath: repo}, persist); err != nil {
		t.Fatal(err)
	}
	// Two handshakes ensure the preceding disabled tick has been processed.
	automaticTick(t, ticks)
	automaticTick(t, ticks)
	m.mu.Lock()
	id := m.op.ID
	m.mu.Unlock()
	if saved.AutoCheckUpdates || id != second.ID {
		t.Fatal("disabled checks still ran")
	}

	if err := m.Save(context.Background(), Config{RepositoryPath: repo, AutoCheckUpdates: true}, persist); err != nil {
		t.Fatal(err)
	}
	nextAutomaticOperation(t, m, second.ID)
	if !saved.AutoCheckUpdates {
		t.Fatal("preference was not persisted")
	}
	m.Close() // Closing must stop the scheduler without another tick.
}

func TestAutomaticChecksDisabledByDefault(t *testing.T) {
	m := managerTest(t, "")
	ticks := make(chan time.Time)
	m.startAutomaticChecks(ticks)
	automaticTick(t, ticks)
	automaticTick(t, ticks)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.op != nil || m.config.AutoCheckUpdates {
		t.Fatal("checks ran without opting in")
	}
}

func TestAutomaticChecksSkipBusyOperations(t *testing.T) {
	_, repo := repositoryTest(t)
	m := managerTest(t, repo)
	m.config.AutoCheckUpdates = true
	// A manual operation holds the manager until it completes.
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	m.run = func(ctx context.Context, dir string, out io.Writer, name string, args ...string) error {
		if len(args) > 0 && args[0] == "fetch" {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return runCommand(ctx, dir, out, name, args...)
	}
	op, err := m.Start("check-updates")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ticks := make(chan time.Time)
	m.startAutomaticChecks(ticks)
	automaticTick(t, ticks)
	automaticTick(t, ticks)
	current, _ := m.Operation(op.ID)
	if current.Status != "running" {
		t.Fatal("automatic check replaced active operation")
	}
	close(release)
	if waitOperation(t, m, op.ID).Status != "succeeded" {
		t.Fatal("manual check failed")
	}
}

func TestUpdateNoticeTracksRunningBuildAndSurvivesFailedCheck(t *testing.T) {
	remote, repo := repositoryTest(t)
	m := managerTest(t, repo)
	m.revision = gitTest(t, repo, "rev-parse", "HEAD")
	writeTest(t, filepath.Join(remote, "main.go"), "new upstream\n")
	commitTest(t, remote, "new upstream")
	target, err := m.UpdateTarget(context.Background())
	if err != nil || !m.UpdateNotice().UpdateAvailable {
		t.Fatal("manual check missed update", err)
	}
	gitTest(t, repo, "merge", "--ff-only", target)
	if _, err = m.UpdateTarget(context.Background()); err != nil || !m.UpdateNotice().UpdateAvailable {
		t.Fatal("pulled checkout hid the old running build", err)
	}

	m.run = func(ctx context.Context, dir string, out io.Writer, name string, args ...string) error {
		if len(args) > 0 && args[0] == "fetch" {
			return errors.New("offline")
		}
		return runCommand(ctx, dir, out, name, args...)
	}
	op, err := m.Start("check-updates")
	if err != nil {
		t.Fatal(err)
	}
	if waitOperation(t, m, op.ID).Status != "failed" || !m.UpdateNotice().UpdateAvailable {
		t.Fatal("temporary failure cleared the known update")
	}
	m.run = runCommand
	m.revision = target + "-modified"
	if _, err = m.UpdateTarget(context.Background()); err != nil || m.UpdateNotice().UpdateAvailable {
		t.Fatal("current running revision retained a stale notice", err)
	}
}

func TestUpdateNoticeUsesRunningRevisionWhenCheckoutIsBehind(t *testing.T) {
	revision := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	comparison := &Comparison{State: "behind", RemoteCommit: revision, Behind: 1}
	if comparisonAvailable(comparison, revision) {
		t.Fatal("running target revision marked outdated")
	}
	if !comparisonAvailable(comparison, "unknown") {
		t.Fatal("unknown build missed upstream commits")
	}
}
