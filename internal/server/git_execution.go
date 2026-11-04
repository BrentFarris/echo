package server

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

// Admission and revocation are shared by editor probes and bot invocations.
// A Save closes admission and joins the old group before publishing the new
// policy. In particular, a command waiting for the index lock cannot outlive
// the owner's grant, even if its caller cached an older plugin manifest.
type gitExecutionPolicy struct {
	mu       sync.Mutex
	changing bool
	epoch    context.Context
	stop     context.CancelFunc
	active   sync.WaitGroup
}

func (s *Server) beginGit(parent context.Context, bot domain.Bot, expected domain.PluginManifest) (context.Context, domain.PluginManifest, context.CancelFunc, error) {
	p := &s.gitPolicy
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := s.Plugins.Get("git")
	if err != nil {
		return nil, m, nil, err
	}
	if !m.Builtin || !m.Enabled || m.Pending || !bot.PluginEnabled("git") {
		return nil, m, nil, errors.New("Git plugin is disabled")
	}
	hostMode := boolean(m.Config, "runOutsideSandbox")
	if p.changing || hostMode != boolean(expected.Config, "runOutsideSandbox") {
		return nil, m, nil, errors.New("Git settings changed; retry the command")
	}
	if hostMode {
		// A delayed tool call can still carry a bot snapshot from before its
		// Git permission or configured folders changed.
		current, err := s.Bot(bot.ID)
		if err != nil {
			return nil, m, nil, err
		}
		if !current.PluginEnabled("git") || current.Revision != bot.Revision {
			return nil, m, nil, errors.New("bot Git access changed; retry the command")
		}
	}
	if p.epoch == nil {
		p.epoch, p.stop = context.WithCancel(s.ctx)
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(p.epoch, cancel)
	if p.epoch.Err() != nil {
		cancel()
	}
	p.active.Add(1)
	return ctx, m, func() { stop(); cancel(); p.active.Done() }, nil
}

// Both settings writers hold gitSettingsMu from reading the old manifest
// through Save so a concurrent chat update cannot restore an old host grant.
func (s *Server) saveConfiguredPlugin(m domain.PluginManifest) error {
	if m.ID != "git" {
		return s.Plugins.Save(m)
	}
	old, err := s.Plugins.Get("git")
	if err != nil {
		return err
	}
	if old.Enabled == m.Enabled && boolean(old.Config, "runOutsideSandbox") == boolean(m.Config, "runOutsideSandbox") {
		return s.Plugins.Save(m)
	}
	p := &s.gitPolicy
	p.mu.Lock()
	p.changing = true
	if p.stop != nil {
		p.stop()
	}
	p.mu.Unlock()
	p.active.Wait()
	ids := s.Git.StopAllWorkspaceProcesses()
	cleanupErr := s.Process.CloseNamedSessions("source-control:git")
	p.mu.Lock()
	err = s.Plugins.Save(m)
	p.epoch, p.stop = nil, nil
	p.changing = false
	p.mu.Unlock()
	for _, id := range ids {
		// Rebind open watches and discard cached repository metadata. Report
		// discovery diagnostics through the normal editor refresh.
		_ = s.Git.ResetWorkspace(s.ctx, id)
		s.SourceControl.Emit(sourcecontrol.Event{Type: "source_control_resync_required", WorkspaceID: id, ProviderID: "git"})
	}
	return errors.Join(err, cleanupErr)
}

func hostGitEnvironment(env map[string]string) {
	// Never ask for input on a terminal or launch an interactive askpass UI.
	// Credential helpers can still return credentials for the current account.
	env["GIT_ASKPASS"] = "echo-askpass-disabled"
	env["SSH_ASKPASS_REQUIRE"] = "never"
	env["GIT_PAGER"] = "cat"
}

func (s *Server) hostGitCommonDir(ctx context.Context, r broker.ProcessRequest) (string, error) {
	r.Args = []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}
	r.SeparateOutput, r.Input = true, ""
	r.Progress = nil
	result, err := s.Process.RunHost(ctx, r, nil)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		// Outside a repository, let the requested subcommand decide.
		return "", nil
	}
	path := strings.TrimSuffix(strings.TrimSuffix(result.Stdout, "\n"), "\r")
	if !filepath.IsAbs(path) || result.Truncated {
		return "", errors.New("Git returned an invalid common metadata directory")
	}
	return filepath.EvalSymlinks(filepath.FromSlash(path))
}

func (s *Server) executeHostGit(ctx context.Context, m domain.PluginManifest, request scmexec.Request) (scmexec.Result, error) {
	binary, err := scmExecutable("git", m)
	if err != nil {
		return scmexec.Result{}, err
	}
	_, env, err := scmArguments("git", "version", nil, m.Config, "")
	if err != nil {
		return scmexec.Result{}, err
	}
	for _, entry := range request.Environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	hostGitEnvironment(env)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := s.Process.RunHost(ctx, broker.ProcessRequest{
		Executable: binary, Args: request.Command[1:], Directory: request.WorkingDirectory,
		Environment: env, Input: string(request.Input), SeparateOutput: true, OutputLimit: request.OutputLimit,
	}, nil)
	return scmexec.Result{Stdout: []byte(result.Stdout), Stderr: []byte(result.Stderr), ExitCode: int(result.ExitCode), StdoutTruncated: result.Truncated}, err
}
