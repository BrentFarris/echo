package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/development"
)

func rolloutGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func rolloutRepositories(t *testing.T, hosts ...*Server) (string, string) {
	t.Helper()
	remote := t.TempDir()
	rolloutGit(t, remote, "init", "-b", "main")
	for _, file := range []string{"main.go", "build.ps1", "build.sh", "web/package.json", "web/package-lock.json", "internal/plugins/guest/main.go", "internal/store/store.go", "go.mod"} {
		path := filepath.Join(remote, file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		content := "fixture"
		if file == "go.mod" {
			content = "module github.com/brent/echo\n"
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() {
		rolloutGit(t, remote, "add", ".")
		rolloutGit(t, remote, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	}
	commit()
	for _, s := range hosts {
		repo := filepath.Join(t.TempDir(), "checkout")
		rolloutGit(t, "", "clone", remote, repo)
		if err := s.Development.Save(context.Background(), development.Config{RepositoryPath: repo}, func(c development.Config) error { return s.Store.Put("settings", "development", c) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(remote, "next.txt"), []byte("next"), 0600); err != nil {
		t.Fatal(err)
	}
	commit()
	return remote, rolloutGit(t, remote, "rev-parse", "HEAD")
}

func TestRolloutThreeMachinesPinnedTargetLostResponseAndOrder(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	c, ch := swarmFixture(t, "mac", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	connectSwarm(t, a, ch, remotePassword)
	_, target := rolloutRepositories(t, a, b, c)
	r, err := a.rollouts.check(context.Background(), b.Swarm.Node().ID, "swarm")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Members) != 3 || r.Target != target || r.Members[2].NodeID != a.Swarm.Node().ID {
		t.Fatal(r)
	}
	var mu sync.Mutex
	accepted := map[string]development.Update{}
	builds := map[string]int{}
	order := []string{}
	lost := false
	a.rollouts.requestUpdate = func(_ context.Context, p rolloutMember, pinned, action string) (development.Update, error) {
		mu.Lock()
		defer mu.Unlock()
		if pinned != target {
			t.Error("target drift")
		}
		u, exists := accepted[p.OperationID]
		if action == "prepare" && !exists {
			u = development.Update{ID: p.OperationID, Target: pinned, Status: "ready", Revision: "old"}
			accepted[p.OperationID] = u
			builds[p.NodeID]++
			if !lost {
				lost = true
				return development.Update{}, errors.New("lost acceptance response")
			}
		}
		if action == "activate" {
			order = append(order, p.NodeID)
			u.Status = "restarting"
		}
		if action == "status" && u.Status == "restarting" {
			u.Status = "succeeded"
			u.Revision = pinned
		}
		accepted[p.OperationID] = u
		return u, nil
	}
	if _, err = a.rollouts.control(r.ID, "start"); err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool { return a.rollouts.snapshot(r).State == "succeeded" })
	mu.Lock()
	defer mu.Unlock()
	for _, p := range r.Members {
		if builds[p.NodeID] != 1 {
			t.Fatal("duplicate execution", builds)
		}
	}
	if !slices.Equal(order, []string{r.Members[0].NodeID, r.Members[1].NodeID, a.Swarm.Node().ID}) {
		t.Fatal("wrong activation order", order)
	}
}

func TestRolloutEntryAuthorityPreflightAndFrozenMembership(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	c, ch := swarmFixture(t, "mac", remotePassword+"different")
	connectSwarm(t, a, bh, remotePassword)
	connectSwarm(t, b, ch, remotePassword+"different")
	_, _ = rolloutRepositories(t, a, b, c)
	var response any
	if err := a.Swarm.JSON(context.Background(), b.Swarm.Node().ID, "POST", "/api/v1/development/rollouts/check", map[string]string{"scope": "swarm"}, &response); err == nil {
		t.Fatal("peer used another entry's grants for orchestration")
	}
	r, err := a.rollouts.check(context.Background(), a.Swarm.Node().ID, "swarm")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Members) != 2 {
		t.Fatal("included unauthorized destination", r.Members)
	}
	bRepo := b.Development.Snapshot(context.Background()).RepositoryPath
	if err = os.WriteFile(filepath.Join(bRepo, "owner-edit.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	dirty, err := a.rollouts.check(context.Background(), a.Swarm.Node().ID, "swarm")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range dirty.Members {
		if p.NodeID == b.Swarm.Node().ID && (p.Status != "skipped" || !strings.Contains(p.Error, "local edits")) {
			t.Fatal(p)
		}
	}
	machine, err := a.rollouts.check(context.Background(), a.Swarm.Node().ID, "machine")
	if err != nil || len(machine.Members) != 1 {
		t.Fatal(machine, err)
	}
	connectSwarm(t, a, ch, remotePassword+"different")
	if len(a.rollouts.snapshot(r).Members) != 2 {
		t.Fatal("rollout membership changed after check")
	}
}

func TestRolloutPartialRetryKeepsTargetAndDoesNotRepeatSuccess(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	_, target := rolloutRepositories(t, a, b)
	r, err := a.rollouts.check(context.Background(), a.Swarm.Node().ID, "swarm")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := map[string]int{}
	fail := true
	a.rollouts.requestUpdate = func(_ context.Context, p rolloutMember, commit, action string) (development.Update, error) {
		mu.Lock()
		defer mu.Unlock()
		if commit != target {
			t.Error("retry target changed")
		}
		if action == "prepare" {
			calls[p.NodeID]++
		}
		u := development.Update{Target: commit, Status: "succeeded", Revision: commit}
		if p.NodeID == b.Swarm.Node().ID && fail {
			u.Status, u.Error = "failed", "build failed"
		}
		return u, nil
	}
	_, _ = a.rollouts.control(r.ID, "start")
	eventuallySwarm(t, func() bool { return a.rollouts.snapshot(r).State == "partial" })
	mu.Lock()
	fail = false
	mu.Unlock()
	_, err = a.rollouts.control(r.ID, "retry")
	if err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool { return a.rollouts.snapshot(r).State == "succeeded" })
	mu.Lock()
	defer mu.Unlock()
	if calls[a.Swarm.Node().ID] != 1 || calls[b.Swarm.Node().ID] != 2 {
		t.Fatal(calls)
	}
}

func TestUpdateMaintenanceRetriesDistributedReservations(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "linux", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	g := attachModel(t, a, func(w http.ResponseWriter, r *http.Request) { stream(w, "local complete", nil) })
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) { stream(w, "remote complete", nil) })
	b.mu.Lock()
	b.maintenance = true
	b.mu.Unlock()
	d, err := a.send(chat(t, a, g).ID, SendRequest{Content: "@echo @linux.echo work", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	select {
	case <-d.done:
		t.Fatal("maintenance failed the dispatch")
	default:
	}
	b.mu.Lock()
	b.maintenance = false
	b.mu.Unlock()
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
}

func TestRolloutRecoveryRetainsMembershipAndCompletedResults(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	r := &rollout{Version: 1, ID: "recover", SourceNodeID: a.Swarm.Node().ID, Scope: "swarm", Target: strings.Repeat("a", 40), State: "running", CreatedAt: time.Now(), Members: []rolloutMember{{NodeID: a.Swarm.Node().ID, Status: "succeeded"}, {NodeID: strings.Repeat("b", 32), Status: "failed", Error: "offline"}}}
	if err := a.Store.Put("development-rollouts", r.ID, r); err != nil {
		t.Fatal(err)
	}
	a = restartSwarmFixture(t, a)
	eventuallySwarm(t, func() bool {
		a.rollouts.mu.Lock()
		defer a.rollouts.mu.Unlock()
		return a.rollouts.jobs[r.ID].State == "partial"
	})
	a.rollouts.mu.Lock()
	saved := *a.rollouts.jobs[r.ID]
	a.rollouts.mu.Unlock()
	if saved.Target != r.Target || len(saved.Members) != 2 || saved.Members[0].Status != "succeeded" {
		t.Fatal(saved)
	}
}
