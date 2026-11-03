package development

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const UpdateCapability = "development-update/1"
const updateLease = 60 * time.Second

type updateStore interface {
	Get(string, string, any) error
	PutRecords(map[string]map[string]any) error
}

// Update is a versioned host-local journal. Owner is always the authenticated
// originating instance, not an arbitrary identity supplied in a JSON body.
type Update struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	Owner           string    `json:"owner"`
	Target          string    `json:"target"`
	Status          string    `json:"status"`
	Stage           string    `json:"stage"`
	Error           string    `json:"error,omitempty"`
	Revision        string    `json:"revision"`
	InstanceID      string    `json:"instanceId"`
	CheckoutUpdated bool      `json:"checkoutUpdated"`
	LogPath         string    `json:"logPath"`
	LogTail         string    `json:"logTail"`
	Repository      string    `json:"repository"`
	Staged          string    `json:"staged,omitempty"`
	Branch          string    `json:"branch,omitempty"`
	Upstream        string    `json:"upstream,omitempty"`
	Lease           time.Time `json:"lease"`
	CancelRequested bool      `json:"cancelRequested,omitempty"`
}

type UpdateCheck struct {
	Target   string `json:"target"`
	Revision string `json:"revision"`
	Head     string `json:"head"`
	Ready    bool   `json:"ready"`
	Current  bool   `json:"current"`
	Error    string `json:"error,omitempty"`
}

func UpdateTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled" || status == "skipped"
}

func (m *Manager) Revision() string { return m.revision }

// ConfigureUpdates recovers journaled launch intent, never re-executes a crashed
// build, and runs before the server begins accepting requests or schedules.
func (m *Manager) ConfigureUpdates(store updateStore, drain func(bool), idle func() bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates, m.setDrain, m.idle = store, drain, idle
	var id string
	if err := store.Get("settings", "development-update", &id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var u Update
	if err := store.Get("development-updates", id, &u); err != nil {
		return err
	}
	if u.Version != 1 {
		return errors.New("unsupported development update journal version")
	}
	m.update = &u
	if !UpdateTerminal(u.Status) {
		if u.Status == "restarting" && u.InstanceID != m.instanceID && u.Target == m.revision {
			m.update.Status, m.update.Stage = "succeeded", "Replacement revision verified"
		} else {
			m.update.Status, m.update.Error = "failed", "Update interrupted; retry explicitly. Running revision: "+m.revision
		}
		m.update.Revision = m.revision
		return m.saveUpdateLocked()
	}
	return nil
}

func (m *Manager) saveUpdateLocked() error {
	return m.updates.PutRecords(map[string]map[string]any{
		"development-updates": {m.update.ID: m.update},
		"settings":            {"development-update": m.update.ID},
	})
}

func (m *Manager) UpdateStatus(owner, id string) (Update, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var u Update
	if m.update != nil && m.update.ID == id {
		u = *m.update
	} else if err := m.updates.Get("development-updates", id, &u); err != nil {
		return u, err
	}
	if u.Owner != owner {
		return Update{}, errors.New("update belongs to another coordinator")
	}
	return u, nil
}

func (m *Manager) clean(ctx context.Context, dir string) error {
	status, err := m.git(ctx, dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("checkout has local edits or untracked files; commit or move them before updating")
	}
	return nil
}

var commitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
var updateIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

// Target pins the selected host's upstream once, including when it is current.
func (m *Manager) UpdateTarget(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.busy() {
		return "", ErrBusy
	}
	if err := m.validate(ctx, m.config.RepositoryPath); err != nil {
		return "", err
	}
	c, err := m.compare(ctx, m.config.RepositoryPath, io.Discard)
	if err == nil {
		m.comparison = &c
	}
	return c.RemoteCommit, err
}

func (m *Manager) checkTarget(ctx context.Context, dir, target string, output io.Writer) (Comparison, error) {
	if !commitPattern.MatchString(target) {
		return Comparison{}, errors.New("invalid target commit")
	}
	if err := m.validate(ctx, dir); err != nil {
		return Comparison{}, err
	}
	if err := m.clean(ctx, dir); err != nil {
		return Comparison{}, err
	}
	c, err := m.compare(ctx, dir, output)
	if err != nil {
		return c, err
	}
	if _, err = m.git(ctx, dir, "merge-base", "--is-ancestor", target, c.RemoteCommit); err != nil {
		return c, errors.New("target commit is not available in this machine's configured upstream history")
	}
	if _, err = m.git(ctx, dir, "merge-base", "--is-ancestor", c.LocalCommit, target); err != nil {
		return c, errors.New("checkout is ahead of or diverged from the target; automatic reset or branch switching is not allowed")
	}
	c.RemoteCommit = target
	return c, nil
}

func (m *Manager) CheckUpdate(ctx context.Context, target string) UpdateCheck {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := UpdateCheck{Target: target, Revision: m.revision}
	if m.closed || m.busy() {
		result.Error = ErrBusy.Error()
		return result
	}
	c, err := m.checkTarget(ctx, m.config.RepositoryPath, target, io.Discard)
	result.Head = c.LocalCommit
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Ready, result.Current = true, m.revision == target
	return result
}

func (m *Manager) PrepareUpdate(owner, id, target string) (Update, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !commitPattern.MatchString(target) || !updateIDPattern.MatchString(id) {
		return Update{}, errors.New("invalid update identity or target")
	}
	var prior Update
	if err := m.updates.Get("development-updates", id, &prior); err == nil {
		if prior.Owner != owner || prior.Target != "" && prior.Target != target {
			return Update{}, errors.New("update identity collision")
		}
		return prior, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Update{}, err
	}
	if m.closed || m.busy() {
		return Update{}, ErrBusy
	}
	dir := filepath.Join(m.dir, "development", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Update{}, err
	}
	u := Update{Version: 1, ID: id, Owner: owner, Target: target, Status: "preparing", Stage: "Validating target", Revision: m.revision, InstanceID: m.instanceID, Repository: m.config.RepositoryPath, LogPath: filepath.Join(dir, "operation.log"), Lease: time.Now().Add(updateLease)}
	m.update = &u
	if err := m.saveUpdateLocked(); err != nil {
		m.update = nil
		return Update{}, err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.updateCancel = cancel
	m.wg.Add(1)
	go func() { defer m.wg.Done(); defer cancel(); m.runUpdate(ctx, u) }()
	return u, nil
}

func (m *Manager) ControlUpdate(owner, id, action string) (Update, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !updateIDPattern.MatchString(id) {
		return Update{}, errors.New("invalid update identity")
	}
	if m.update == nil || m.update.ID != id {
		var prior Update
		err := m.updates.Get("development-updates", id, &prior)
		if err == nil && prior.Owner == owner && UpdateTerminal(prior.Status) {
			return prior, nil
		}
		if errors.Is(err, sql.ErrNoRows) && action == "cancel" {
			// Fence a delayed prepare whose acceptance response was lost.
			prior = Update{Version: 1, ID: id, Owner: owner, Status: "cancelled", Stage: "Cancelled before preparation"}
			err = m.updates.PutRecords(map[string]map[string]any{"development-updates": {id: prior}})
			return prior, err
		}
		return Update{}, errors.New("update not owned by this coordinator")
	}
	if m.update.Owner != owner {
		return Update{}, errors.New("update not owned by this coordinator")
	}
	u := m.update
	if UpdateTerminal(u.Status) || u.Status == "restarting" {
		return *u, nil
	}
	switch action {
	case "renew":
		u.Lease = time.Now().Add(updateLease)
	case "activate":
		if u.Status != "ready" && u.Status != "draining" {
			return Update{}, errors.New("update is not prepared")
		}
		u.Status, u.Stage, u.Lease = "draining", "Waiting for active work to finish", time.Now().Add(updateLease)
	case "cancel":
		u.CancelRequested = true
	default:
		return Update{}, errors.New("unknown update control")
	}
	if err := m.saveUpdateLocked(); err != nil {
		return Update{}, err
	}
	if action == "cancel" && m.updateCancel != nil {
		m.updateCancel()
	}
	return *u, nil
}

func (m *Manager) updateStage(stage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.update.Stage = stage
	return m.saveUpdateLocked()
}

type updateWriter struct {
	m    *Manager
	file *os.File
}

func (w updateWriter) Write(p []byte) (int, error) {
	n, err := w.file.Write(p)
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.m.update.LogTail += string(p[:n])
	if len(w.m.update.LogTail) > tailLimit {
		w.m.update.LogTail = w.m.update.LogTail[len(w.m.update.LogTail)-tailLimit:]
	}
	if saveErr := w.m.saveUpdateLocked(); err == nil {
		err = saveErr
	}
	return n, err
}

func (m *Manager) runUpdate(ctx context.Context, initial Update) {
	var failure error
	launched, draining := false, false
	defer func() {
		if draining {
			m.setDrain(false)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if launched {
			return
		}
		if failure != nil {
			m.update.Status, m.update.Error = "failed", failure.Error()
			if m.update.CancelRequested {
				m.update.Status, m.update.Error = "cancelled", "Update cancelled; any completed checkout fast-forward remains in place"
			}
		} else {
			m.update.Status, m.update.Stage = "succeeded", "Already running the target revision"
		}
		if m.update.Staged != "" {
			_ = os.Remove(m.update.Staged)
		}
		_ = m.saveUpdateLocked()
	}()
	log, err := os.OpenFile(initial.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		failure = err
		return
	}
	defer log.Close()
	output := updateWriter{m, log}
	buildCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	c, err := m.checkTarget(buildCtx, initial.Repository, initial.Target, output)
	if err == nil && c.LocalCommit != initial.Target {
		err = m.fastForward(buildCtx, initial.Repository, c, output)
		if err == nil {
			m.mu.Lock()
			m.update.CheckoutUpdated = true
			err = m.saveUpdateLocked()
			m.mu.Unlock()
		}
	}
	if err == nil {
		err = m.clean(buildCtx, initial.Repository)
	}
	if err != nil {
		cancel()
		failure = err
		return
	}
	m.mu.Lock()
	m.update.Branch, m.update.Upstream = c.Branch, c.Upstream
	m.mu.Unlock()
	if initial.Revision == initial.Target {
		cancel()
		return
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	staged := filepath.Join(initial.Repository, ".cache", "updates", initial.ID, "echo"+suffix)
	if err = os.MkdirAll(filepath.Dir(staged), 0700); err == nil {
		m.mu.Lock()
		m.update.Staged = staged
		err = m.saveUpdateLocked()
		m.mu.Unlock()
	}
	if err == nil {
		err = m.updateStage("Building the pinned source revision")
	}
	if err == nil {
		if runtime.GOOS == "windows" {
			var shell string
			shell, err = findPowerShell()
			if err == nil {
				err = m.run(buildCtx, initial.Repository, output, shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(initial.Repository, "build.ps1"), "-OutputPath", staged)
			}
		} else {
			err = m.run(buildCtx, initial.Repository, output, "/bin/sh", filepath.Join(initial.Repository, "build.sh"), "--output", staged)
		}
	}
	if err == nil {
		err = m.verifyUpdateSource(buildCtx, initial.Repository, initial.Target, c.Branch, c.Upstream)
	}
	if err == nil {
		info, e := os.Stat(staged)
		if e != nil || !info.Mode().IsRegular() {
			err = errors.New("build did not produce a staged executable")
		}
	}
	cancel()
	if err != nil {
		failure = err
		return
	}
	m.mu.Lock()
	m.update.Status, m.update.Stage = "ready", "Build prepared"
	err = m.saveUpdateLocked()
	m.mu.Unlock()
	if err != nil {
		failure = err
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			failure = ctx.Err()
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		u := *m.update
		m.mu.Unlock()
		if time.Now().After(u.Lease) {
			failure = errors.New("coordinator lease expired; retry explicitly")
			return
		}
		if u.Status != "draining" {
			continue
		}
		if !draining {
			m.setDrain(true)
			draining = true
		}
		if !m.idle() {
			continue
		}
		verifyCtx, verifyCancel := context.WithTimeout(ctx, 10*time.Second)
		err = m.verifyUpdateSource(verifyCtx, initial.Repository, initial.Target, c.Branch, c.Upstream)
		verifyCancel()
		if err != nil {
			failure = err
			return
		}
		m.mu.Lock()
		if m.update.CancelRequested || ctx.Err() != nil || time.Now().After(m.update.Lease) {
			m.mu.Unlock()
			failure = errors.New("activation cancelled or expired")
			return
		}
		m.update.Status, m.update.Stage = "restarting", "Launch intent saved; replacing Echo"
		err = m.saveUpdateLocked()
		if err == nil {
			spec := launchSpec{ProcessID: os.Getpid(), StagedPath: staged, BinaryPath: filepath.Join(initial.Repository, "echo"+suffix), Arguments: m.arguments, WorkingDir: m.workingDir, LogPath: filepath.Join(filepath.Dir(initial.LogPath), "relaunch.log"), WaitSeconds: 60, ReplaceBinary: true}
			err = m.launch(spec, filepath.Dir(initial.LogPath))
		}
		m.mu.Unlock()
		if err != nil {
			failure = fmt.Errorf("launcher preparation failed: %w", err)
			return
		}
		launched = true
		// Keep admission closed through process shutdown, not just launcher creation.
		draining = false
		m.stop()
		return
	}
}

func (m *Manager) verifyUpdateSource(ctx context.Context, dir, target, branch, upstream string) error {
	if err := m.clean(ctx, dir); err != nil {
		return err
	}
	head, err := m.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	b, up, err := m.tracking(ctx, dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != target || b != branch || up.Name != upstream {
		return errors.New("source or tracking branch changed during update; prepared build was not activated")
	}
	return nil
}
