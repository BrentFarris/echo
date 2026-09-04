package gotest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

var (
	ErrNotGoTestFile  = errors.New("file is not a Go test file")
	ErrTargetNotFound = errors.New("Go test target was not found")
	ErrRunNotFound    = errors.New("Go test run was not found")
)

type RunRequest struct {
	Ref    workspacefs.FileRef `json:"ref"`
	Target Target              `json:"target"`
}

type LensRequest struct {
	Ref  workspacefs.FileRef `json:"ref"`
	Text string              `json:"text"`
}

type runRecord struct {
	SessionID string
	Request   RunRequest
}

type Service struct {
	workspaces *workspaces.Manager
	fs         *workspacefs.Service
	terminal   *terminal.Service
	debugger   *debugger.Service
	mu         sync.Mutex
	latest     map[string]runRecord
}

func New(workspaceManager *workspaces.Manager, fs *workspacefs.Service, terminalService *terminal.Service, debuggerService *debugger.Service) *Service {
	return &Service{workspaces: workspaceManager, fs: fs, terminal: terminalService, debugger: debuggerService, latest: map[string]runRecord{}}
}

func (s *Service) Config(workspaceID string) (gotestconfig.GoConfig, error) {
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return gotestconfig.GoConfig{}, err
	}
	if !ok {
		return gotestconfig.GoConfig{}, fmt.Errorf("%w: %q", workspaces.ErrWorkspaceNotFound, workspaceID)
	}
	return workspace.Testing.Go.Normalized(), nil
}

func (s *Service) SetConfig(workspaceID string, config gotestconfig.GoConfig) (gotestconfig.GoConfig, error) {
	config = config.Normalized()
	if err := config.Validate(); err != nil {
		return gotestconfig.GoConfig{}, err
	}
	workspace, err := s.workspaces.SetTestingConfig(workspaceID, gotestconfig.WorkspaceConfig{Go: config})
	if err != nil {
		return gotestconfig.GoConfig{}, err
	}
	return workspace.Testing.Go.Normalized(), nil
}

func (s *Service) Lenses(workspaceID string, request LensRequest) ([]Lens, error) {
	config, err := s.Config(workspaceID)
	if err != nil || !config.CodeLens {
		return nil, err
	}
	hostPath, err := s.validateRef(workspaceID, request.Ref)
	if err != nil {
		return nil, err
	}
	if len(request.Text) > MaxSourceBytes {
		return nil, fmt.Errorf("Go test source exceeds %d bytes", MaxSourceBytes)
	}
	lenses := DiscoverSource(hostPath, request.Text)
	if !hasTargetKind(lenses, TargetPackageBenchmarks) && packageBenchmarkExists(filepath.Dir(hostPath), hostPath) {
		lenses = addPackageBenchmarkLens(lenses)
	}
	return lenses, nil
}

func (s *Service) Run(workspaceID string, request RunRequest) (terminal.Snapshot, error) {
	hostPath, info, config, err := s.savedTarget(workspaceID, request)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	plan, err := buildCommand(request.Target, info, config)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	snapshot, err := s.terminal.StartTask(workspaceID, terminal.TaskRequest{
		Name: "Test Output", Command: "go", Args: plan.Args, WorkingDirectory: filepath.Dir(hostPath),
		Environment: config.Environment, DisplayCommand: plan.Display,
	})
	if err != nil {
		return terminal.Snapshot{}, err
	}
	s.mu.Lock()
	s.latest[workspaceID] = runRecord{SessionID: snapshot.ID, Request: request}
	s.mu.Unlock()
	return snapshot, nil
}

func (s *Service) Rerun(workspaceID, sessionID string) (terminal.Snapshot, error) {
	s.mu.Lock()
	record, ok := s.latest[workspaceID]
	s.mu.Unlock()
	if !ok || record.SessionID != sessionID {
		return terminal.Snapshot{}, ErrRunNotFound
	}
	return s.Run(workspaceID, record.Request)
}

func (s *Service) Debug(ctx context.Context, workspaceID string, request RunRequest) (debugger.Snapshot, error) {
	hostPath, info, config, err := s.savedTarget(workspaceID, request)
	if err != nil {
		return debugger.Snapshot{}, err
	}
	plan, err := buildCommand(request.Target, info, config)
	if err != nil {
		return debugger.Snapshot{}, err
	}
	profile, err := s.debugger.EnabledAdapterProfile(workspaceID, "go")
	if err != nil {
		return debugger.Snapshot{}, err
	}
	arguments := map[string]any{
		"mode": "test", "program": filepath.Dir(hostPath), "args": plan.DebugArguments,
		"env": config.Environment,
	}
	if plan.BuildFlags != "" {
		arguments["buildFlags"] = plan.BuildFlags
	}
	configuration := debugconfig.Configuration{
		ID: "go-codelens-test", Name: debugName(request.Target), AdapterProfileID: profile.ID,
		Request: "launch", Arguments: arguments,
	}
	return s.debugger.StartTransient(ctx, workspaceID, configuration, debugger.StartRequest{CurrentFile: &debugconfig.SourceRef{RootID: request.Ref.RootID, Path: request.Ref.Path}})
}

func (s *Service) savedTarget(workspaceID string, request RunRequest) (string, sourceInfo, gotestconfig.GoConfig, error) {
	hostPath, err := s.validateRef(workspaceID, request.Ref)
	if err != nil {
		return "", sourceInfo{}, gotestconfig.GoConfig{}, err
	}
	snapshot, err := s.fs.Read(workspaceID, request.Ref)
	if err != nil {
		return "", sourceInfo{}, gotestconfig.GoConfig{}, err
	}
	info, ok := parseSource(hostPath, snapshot.Content)
	if ok && request.Target.Kind == TargetPackageBenchmarks && len(info.Benchmarks) == 0 && packageBenchmarkExists(filepath.Dir(hostPath), hostPath) {
		info.Lenses = addPackageBenchmarkLens(info.Lenses)
	}
	if !ok || !targetPresent(info.Lenses, request.Target) {
		return "", sourceInfo{}, gotestconfig.GoConfig{}, ErrTargetNotFound
	}
	config, err := s.Config(workspaceID)
	if err != nil {
		return "", sourceInfo{}, gotestconfig.GoConfig{}, err
	}
	return hostPath, info, config, nil
}

func packageBenchmarkExists(directory, currentPath string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(entry.Name()), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if sameFilePath(path, currentPath) {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) > MaxSourceBytes {
			continue
		}
		info, parsed := parseSource(path, string(data))
		if parsed && len(info.Benchmarks) > 0 {
			return true
		}
	}
	return false
}

func sameFilePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func hasTargetKind(lenses []Lens, kind string) bool {
	for _, item := range lenses {
		if item.Target.Kind == kind {
			return true
		}
	}
	return false
}

func (s *Service) validateRef(workspaceID string, ref workspacefs.FileRef) (string, error) {
	if !strings.HasSuffix(strings.ToLower(filepath.ToSlash(ref.Path)), "_test.go") {
		return "", ErrNotGoTestFile
	}
	hostPath, err := s.fs.ResolveExistingHostPath(workspaceID, ref, false)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(hostPath), "_test.go") {
		return "", ErrNotGoTestFile
	}
	return hostPath, nil
}

func targetPresent(lenses []Lens, target Target) bool {
	for _, candidate := range lenses {
		if sameTarget(candidate.Target, target) {
			return true
		}
	}
	return false
}

func debugName(target Target) string {
	if len(target.Path) > 0 {
		return "Debug Test: " + strings.Join(target.Path, "/")
	}
	return "Debug Go Tests"
}
