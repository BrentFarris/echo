// Package development implements owner-requested source updates and process lifecycle operations.
package development

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/buildinfo"
	"github.com/brent/echo/internal/domain"
)

const tailLimit = 16 * 1024

var ErrBusy = errors.New("a development operation is already in progress")

type Config struct {
	RepositoryPath   string `json:"repositoryPath"`
	AutoCheckUpdates bool   `json:"autoCheckUpdates"`
}

type Operation struct {
	ID              string `json:"id"`
	Action          string `json:"action"`
	Status          string `json:"status"`
	Stage           string `json:"stage"`
	Error           string `json:"error,omitempty"`
	LogPath         string `json:"logPath"`
	LogTail         string `json:"logTail"`
	CheckoutUpdated bool   `json:"checkoutUpdated"`
}

type Snapshot struct {
	Config
	UpdateAvailable bool        `json:"updateAvailable"`
	SuggestedPath   string      `json:"suggestedPath"`
	RepositoryValid bool        `json:"repositoryValid"`
	RepositoryError string      `json:"repositoryError,omitempty"`
	Branch          string      `json:"branch"`
	Upstream        string      `json:"upstream"`
	UpdateError     string      `json:"updateError,omitempty"`
	Comparison      *Comparison `json:"comparison,omitempty"`
	Operation       *Operation  `json:"operation,omitempty"`
	InstanceID      string      `json:"instanceId"`
	Revision        string      `json:"revision"`
}

type runner func(context.Context, string, io.Writer, string, ...string) error

type Manager struct {
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	config          Config
	op              *Operation
	comparison      *Comparison
	instanceID, dir string
	stop            func()
	run             runner
	launch          func(launchSpec, string) error
	executable      string
	arguments       []string
	workingDir      string
	autoWake        chan struct{}
	autoStarted     bool
	closed          bool
	updates         updateStore
	update          *Update
	updateCancel    context.CancelFunc
	setDrain        func(bool)
	idle            func() bool
	revision        string
}

func New(parent context.Context, dir string, config Config, stop func()) *Manager {
	ctx, cancel := context.WithCancel(parent)
	executable, _ := os.Executable()
	workingDir, _ := os.Getwd()
	return &Manager{ctx: ctx, cancel: cancel, dir: dir, config: config, stop: stop, autoWake: make(chan struct{}, 1),
		instanceID: domain.ID(), revision: buildinfo.Current(), run: runCommand, launch: prepareAndLaunch,
		executable: executable, workingDir: workingDir, arguments: append([]string(nil), os.Args[1:]...)}
}

func (m *Manager) InstanceID() string { return m.instanceID }

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) busy() bool {
	return m.update != nil && !UpdateTerminal(m.update.Status) || m.op != nil && (m.op.Status == "running" || m.op.Status == "restarting" || m.op.Status == "stopping")
}

// Save serializes repository changes with operations, including their validation.
func (m *Manager) Save(ctx context.Context, config Config, persist func(Config) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("Echo is shutting down")
	}
	if m.busy() {
		return ErrBusy
	}
	config.RepositoryPath = strings.TrimSpace(config.RepositoryPath)
	if config.RepositoryPath != "" {
		if err := m.validate(ctx, config.RepositoryPath); err != nil {
			return err
		}
		config.RepositoryPath = filepath.Clean(config.RepositoryPath)
	}
	if err := persist(config); err != nil {
		return err
	}
	previous := m.config
	m.config = config
	if previous.RepositoryPath != config.RepositoryPath {
		m.comparison = nil
	}
	if previous != config {
		select {
		case m.autoWake <- struct{}{}:
		default:
		}
	}
	return nil
}

func (m *Manager) Snapshot(ctx context.Context) Snapshot {
	m.mu.Lock()
	s := Snapshot{Config: m.config, InstanceID: m.instanceID, Revision: m.revision}
	if m.op != nil {
		op := *m.op
		s.Operation = &op
	}
	if m.comparison != nil {
		comparison := *m.comparison
		s.Comparison = &comparison
	}
	m.mu.Unlock()
	if s.RepositoryPath == "" {
		for _, candidate := range []string{m.workingDir, filepath.Dir(m.executable)} {
			if candidate != "" && m.validate(ctx, candidate) == nil {
				s.SuggestedPath = candidate
				break
			}
		}
		return s
	}
	if err := m.validate(ctx, s.RepositoryPath); err != nil {
		s.RepositoryError = err.Error()
		return s
	}
	s.RepositoryValid = true
	branch, upstream, err := m.tracking(ctx, s.RepositoryPath)
	s.Branch, s.Upstream = branch, upstream.Name
	if err != nil {
		s.UpdateError = err.Error()
	}
	// A branch change outside Echo must not leave an actionable stale comparison.
	if s.Comparison != nil && (s.Comparison.Branch != branch || s.Comparison.Upstream != upstream.Name) {
		s.Comparison = nil
	}
	s.UpdateAvailable = s.RepositoryValid && s.UpdateError == "" && comparisonAvailable(s.Comparison, s.Revision)
	return s
}

func (m *Manager) Operation(id string) (Operation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.op == nil || m.op.ID != id {
		return Operation{}, false
	}
	return *m.op, true
}

func (m *Manager) Start(action string) (Operation, error) {
	switch action {
	case "check-updates", "update-relaunch", "build-relaunch", "relaunch", "terminate":
	default:
		return Operation{}, errors.New("unknown development action")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked(action)
}

func (m *Manager) startLocked(action string) (Operation, error) {
	if m.closed {
		return Operation{}, errors.New("Echo is shutting down")
	}
	if m.busy() {
		return Operation{}, ErrBusy
	}
	if action != "relaunch" && action != "terminate" && m.config.RepositoryPath == "" {
		return Operation{}, errors.New("save the Echo Git repository path first")
	}
	id := domain.ID()
	logDir := filepath.Join(m.dir, "development", id)
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return Operation{}, err
	}
	logPath := filepath.Join(logDir, "operation.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Operation{}, err
	}
	m.op = &Operation{ID: id, Action: action, Status: "running", Stage: "Preparing", LogPath: logPath}
	if action == "update-relaunch" {
		m.comparison = nil
	}
	result := *m.op
	config := m.config
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Minute)
		defer cancel()
		output := &operationWriter{manager: m, file: log}
		err := m.execute(ctx, config, action, output)
		if err != nil {
			fmt.Fprintln(output, err)
		}
		closeErr := log.Close()
		if err == nil {
			err = closeErr
		}
		m.mu.Lock()
		if err != nil {
			m.op.Status, m.op.Error = "failed", err.Error()
			if m.op.CheckoutUpdated {
				m.op.Error = "The checkout was updated, but Echo is still running the previous build. " + err.Error()
			}
		} else if action == "check-updates" {
			m.op.Status, m.op.Stage = "succeeded", "Update check complete"
		} else if action == "terminate" {
			m.op.Status, m.op.Stage = "stopping", "Echo is shutting down"
		} else {
			m.op.Status, m.op.Stage = "restarting", "Waiting for Echo to restart"
		}
		m.mu.Unlock()
		if err == nil && action != "check-updates" {
			// Give the initiating browser time to receive the acceptance and status responses.
			select {
			case <-m.ctx.Done():
			case <-time.After(750 * time.Millisecond):
				m.stop()
			}
		}
	}()
	return result, nil
}

func (m *Manager) stage(output io.Writer, stage string) {
	m.mu.Lock()
	m.op.Stage = stage
	m.mu.Unlock()
	fmt.Fprintf(output, "\n%s %s\n", time.Now().Format(time.RFC3339), stage)
}

func (m *Manager) execute(ctx context.Context, config Config, action string, output io.Writer) error {
	if action == "terminate" {
		return nil
	}
	if action == "check-updates" || action == "update-relaunch" || action == "build-relaunch" {
		m.stage(output, "Validating repository")
		if err := m.validate(ctx, config.RepositoryPath); err != nil {
			return err
		}
	}
	if action == "check-updates" || action == "update-relaunch" {
		m.stage(output, "Fetching and comparing upstream")
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		comparison, err := m.compare(checkCtx, config.RepositoryPath, output)
		cancel()
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.comparison = &comparison
		m.mu.Unlock()
		if action == "check-updates" {
			return nil
		}
		if comparison.State != "behind" {
			return fmt.Errorf("cannot update: branch is %s; only a fast-forward from behind is supported", comparison.State)
		}
		m.stage(output, "Fast-forwarding upstream")
		if err := m.fastForward(ctx, config.RepositoryPath, comparison, output); err != nil {
			return err
		}
		m.mu.Lock()
		m.op.CheckoutUpdated = true
		m.comparison = nil
		m.mu.Unlock()
	}
	binary, staged := m.executable, m.executable
	launcherOwnsStaged := false
	defer func() {
		if staged != binary && !launcherOwnsStaged {
			_ = os.Remove(staged)
		}
	}()
	if action == "build-relaunch" || action == "update-relaunch" {
		suffix := ""
		if runtime.GOOS == "windows" {
			suffix = ".exe"
		}
		binary = filepath.Join(config.RepositoryPath, "echo"+suffix)
		staged = filepath.Join(config.RepositoryPath, "echo.rebuild-"+domain.ID()+suffix)
		m.stage(output, "Building plugin runtime, frontend, and Echo")
		var buildErr error
		if runtime.GOOS == "windows" {
			powerShell, err := findPowerShell()
			if err != nil {
				return err
			}
			buildErr = m.run(ctx, config.RepositoryPath, output, powerShell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(config.RepositoryPath, "build.ps1"), "-OutputPath", staged)
		} else {
			buildErr = m.run(ctx, config.RepositoryPath, output, "/bin/sh", filepath.Join(config.RepositoryPath, "build.sh"), "--output", staged)
		}
		if buildErr != nil {
			return fmt.Errorf("build failed: %w", buildErr)
		}
		if info, err := os.Stat(staged); err != nil || !info.Mode().IsRegular() {
			return errors.New("build did not produce the staged Echo executable")
		}
	}
	if binary == "" {
		return errors.New("cannot resolve the running Echo executable")
	}
	m.stage(output, "Preparing detached launcher")
	m.mu.Lock()
	logPath := m.op.LogPath
	m.mu.Unlock()
	spec := launchSpec{ProcessID: os.Getpid(), StagedPath: staged, BinaryPath: binary, Arguments: m.arguments,
		WorkingDir: m.workingDir, LogPath: filepath.Join(filepath.Dir(logPath), "relaunch.log"), WaitSeconds: 60, ReplaceBinary: staged != binary}
	if err := m.launch(spec, filepath.Dir(logPath)); err != nil {
		return fmt.Errorf("launcher preparation failed: %w", err)
	}
	launcherOwnsStaged = true
	return nil
}

type operationWriter struct {
	manager *Manager
	file    *os.File
}

func (w *operationWriter) Write(p []byte) (int, error) {
	w.manager.mu.Lock()
	defer w.manager.mu.Unlock()
	n, err := w.file.Write(p)
	tail := w.manager.op.LogTail + string(p[:n])
	if len(tail) > tailLimit {
		tail = tail[len(tail)-tailLimit:]
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}
	w.manager.op.LogTail = tail
	return n, err
}

func runCommand(ctx context.Context, dir string, output io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	command.Stdout, command.Stderr = output, output
	configureCommand(command)
	command.WaitDelay = 5 * time.Second
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return nil
}
