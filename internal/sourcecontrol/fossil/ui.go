package fossil

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/brent/echo/internal/sourcecontrol"
)

const (
	fossilUIStartupGrace  = 500 * time.Millisecond
	fossilUIStopTimeout   = 5 * time.Second
	maximumFossilUIOutput = 1 << 20
)

type fossilUIProcess interface {
	Wait() error
	Kill() error
}

type fossilUIStartSpec struct {
	Executable  string
	Args        []string
	Directory   string
	Environment []string
	Stdout      io.Writer
	Stderr      io.Writer
}

type fossilUIStarter func(fossilUIStartSpec) (fossilUIProcess, error)

type fossilUISession struct {
	workspaceID string
	process     fossilUIProcess
	stdout      *cappedBuffer
	stderr      *cappedBuffer
	done        chan struct{}
	waitErr     error
}

type localFossilUIProcess struct{ command *exec.Cmd }

func (p *localFossilUIProcess) Wait() error { return p.command.Wait() }

func (p *localFossilUIProcess) Kill() error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return os.ErrProcessDone
	}
	return p.command.Process.Kill()
}

func startLocalFossilUI(spec fossilUIStartSpec) (fossilUIProcess, error) {
	command := exec.Command(spec.Executable, spec.Args...)
	command.Dir = spec.Directory
	command.Env = append(os.Environ(), spec.Environment...)
	command.Stdout = spec.Stdout
	command.Stderr = spec.Stderr
	configureFossilUICommand(command)
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &localFossilUIProcess{command: command}, nil
}

func (p *Provider) openFossilUI(state *repositoryState) error {
	if p.sandbox != nil && p.sandbox.IsEnabled(state.workspaceID) {
		return &sourcecontrol.Error{
			Code:    "fossil_ui_unavailable_in_sandbox",
			Message: "Fossil UI requires the workspace sandbox to be disabled",
		}
	}

	key := pathIdentity(state.root)
	p.uiMu.Lock()
	if p.uiClosed {
		p.uiMu.Unlock()
		return &sourcecontrol.Error{Code: "fossil_ui_start_failed", Message: "Fossil UI cannot be opened while Echo is shutting down"}
	}
	epoch := p.uiWorkspaceEpoch[state.workspaceID]
	previous := p.uiProcesses[key]
	p.uiMu.Unlock()

	if previous != nil {
		if err := p.stopFossilUISession(previous); err != nil {
			return &sourcecontrol.Error{Code: "fossil_ui_restart_failed", Message: "The previous Fossil UI process could not be stopped", Cause: err}
		}
	}

	// Recheck after stopping an existing server. A sandbox transition that
	// races this action must never cause a host-side Fossil launch.
	if p.sandbox != nil && p.sandbox.IsEnabled(state.workspaceID) {
		return &sourcecontrol.Error{
			Code:    "fossil_ui_unavailable_in_sandbox",
			Message: "Fossil UI requires the workspace sandbox to be disabled",
		}
	}

	stdout := &cappedBuffer{limit: maximumFossilUIOutput}
	stderr := &cappedBuffer{limit: maximumFossilUIOutput}
	session := &fossilUISession{
		workspaceID: state.workspaceID,
		stdout:      stdout,
		stderr:      stderr,
		done:        make(chan struct{}),
	}
	spec := fossilUIStartSpec{
		Executable:  "fossil",
		Args:        []string{"ui", "--localhost"},
		Directory:   state.root,
		Environment: fossilCommandEnvironment(),
		Stdout:      stdout,
		Stderr:      stderr,
	}

	p.uiMu.Lock()
	if p.uiClosed || p.uiWorkspaceEpoch[state.workspaceID] != epoch {
		p.uiMu.Unlock()
		return &sourcecontrol.Error{Code: "fossil_ui_start_failed", Message: "Fossil UI launch was cancelled because the workspace execution environment changed"}
	}
	starter := p.uiStarter
	if starter == nil {
		starter = startLocalFossilUI
	}
	process, err := starter(spec)
	if err != nil {
		p.uiMu.Unlock()
		return fossilUIStartError(err, state.root, stdout, stderr)
	}
	session.process = process
	p.uiProcesses[key] = session
	p.uiMu.Unlock()

	go p.monitorFossilUISession(key, session)
	grace := p.uiStartupGrace
	if grace <= 0 {
		grace = fossilUIStartupGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-session.done:
		return fossilUIStartError(session.waitErr, state.root, stdout, stderr)
	case <-timer.C:
		return nil
	}
}

func (p *Provider) monitorFossilUISession(key string, session *fossilUISession) {
	session.waitErr = session.process.Wait()
	close(session.done)
	p.uiMu.Lock()
	if p.uiProcesses[key] == session {
		delete(p.uiProcesses, key)
	}
	p.uiMu.Unlock()
}

func fossilUIStartError(err error, root string, stdout, stderr *cappedBuffer) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return &sourcecontrol.Error{Code: "fossil_unavailable", Message: "Fossil is not installed or is not available on PATH", Cause: err}
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = strings.TrimSpace(stdout.String())
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	if message == "" {
		message = "Fossil UI exited before it was ready"
	}
	return &sourcecontrol.Error{Code: "fossil_ui_start_failed", Message: sanitizeOutput(message, root), Cause: err}
}

func (p *Provider) stopFossilUISession(session *fossilUISession) error {
	select {
	case <-session.done:
		return nil
	default:
	}
	if err := session.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		select {
		case <-session.done:
			return nil
		default:
			return err
		}
	}
	timeout := p.uiStopTimeout
	if timeout <= 0 {
		timeout = fossilUIStopTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-session.done:
		return nil
	case <-timer.C:
		return &sourcecontrol.Error{Code: "fossil_ui_stop_timeout", Message: "Fossil UI did not stop in time"}
	}
}

func (p *Provider) stopWorkspaceFossilUIProcesses(workspaceID string) {
	p.uiMu.Lock()
	p.uiWorkspaceEpoch[workspaceID]++
	sessions := make([]*fossilUISession, 0)
	for key, session := range p.uiProcesses {
		if session.workspaceID == workspaceID {
			delete(p.uiProcesses, key)
			sessions = append(sessions, session)
		}
	}
	p.uiMu.Unlock()
	for _, session := range sessions {
		_ = p.stopFossilUISession(session)
	}
}

func (p *Provider) closeFossilUIProcesses() {
	p.uiMu.Lock()
	if p.uiClosed {
		p.uiMu.Unlock()
		return
	}
	p.uiClosed = true
	sessions := make([]*fossilUISession, 0, len(p.uiProcesses))
	for key, session := range p.uiProcesses {
		delete(p.uiProcesses, key)
		sessions = append(sessions, session)
	}
	p.uiMu.Unlock()
	for _, session := range sessions {
		_ = p.stopFossilUISession(session)
	}
}
