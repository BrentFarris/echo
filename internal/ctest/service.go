package ctest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brent/echo/internal/debugconfig"
	"github.com/brent/echo/internal/debugger"
	"github.com/brent/echo/internal/gotestconfig"
	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/terminal"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
	"github.com/google/uuid"
)

var (
	ErrNotCSourceFile = errors.New("file is not a C source file")
	ErrTargetNotFound = errors.New("C test target was not found")
	ErrEntryNotFound  = errors.New("C test entry function was not found")
	ErrRunNotFound    = errors.New("C test run was not found")
)

type LensRequest struct {
	Ref  workspacefs.FileRef `json:"ref"`
	Text string              `json:"text"`
}

type RunRequest struct {
	TargetID string `json:"targetId"`
}

type runRecord struct {
	SessionID string
	Request   RunRequest
}

type resolvedTarget struct {
	config             gotestconfig.CTarget
	entryPath          string
	entryRef           workspacefs.FileRef
	sourceRoots        []string
	sourceRootRefs     []workspacefs.FileRef
	objectRoots        []string
	executable         string
	cwd                string
	runtimeExecutable  string
	runtimeArgs        []string
	runtimeEnvironment map[string]string
	build              *resolvedCommand
	runtimeObjects     []string
	workspace          workspaces.Workspace
}

type resolvedCommand struct {
	command     string
	args        []string
	cwd         string
	environment map[string]string
	timeout     time.Duration
}

type Service struct {
	workspaces *workspaces.Manager
	fs         *workspacefs.Service
	terminal   *terminal.Service
	debugger   *debugger.Service
	sandbox    *sandbox.Manager

	mu             sync.Mutex
	latest         map[string]runRecord
	coverage       map[string]coverageRecord
	coverageNotify func(CoverageEvent)
}

func New(workspaceManager *workspaces.Manager, fs *workspacefs.Service, terminalService *terminal.Service, debuggerService *debugger.Service, sandboxManager *sandbox.Manager) *Service {
	return &Service{
		workspaces: workspaceManager, fs: fs, terminal: terminalService, debugger: debuggerService, sandbox: sandboxManager,
		latest: make(map[string]runRecord), coverage: make(map[string]coverageRecord),
	}
}

func (s *Service) Config(workspaceID string) (gotestconfig.CConfig, error) {
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return gotestconfig.CConfig{}, err
	}
	if !ok {
		return gotestconfig.CConfig{}, fmt.Errorf("%w: %q", workspaces.ErrWorkspaceNotFound, workspaceID)
	}
	return workspace.Testing.C.Normalized(), nil
}

func (s *Service) SetConfig(workspaceID string, config gotestconfig.CConfig) (gotestconfig.CConfig, error) {
	config = config.Normalized()
	if err := config.Validate(); err != nil {
		return gotestconfig.CConfig{}, err
	}
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return gotestconfig.CConfig{}, err
	}
	if !ok {
		return gotestconfig.CConfig{}, fmt.Errorf("%w: %q", workspaces.ErrWorkspaceNotFound, workspaceID)
	}
	for _, target := range config.Targets {
		if _, err := s.resolveTarget(workspaceID, target, false); err != nil {
			return gotestconfig.CConfig{}, fmt.Errorf("C test target %q: %w", target.ID, err)
		}
	}
	testing := workspace.Testing
	testing.C = config
	workspace, err = s.workspaces.SetTestingConfig(workspaceID, testing)
	if err != nil {
		return gotestconfig.CConfig{}, err
	}
	s.ClearCoverage(workspaceID)
	return workspace.Testing.C.Normalized(), nil
}

func (s *Service) Lenses(workspaceID string, request LensRequest) ([]Lens, error) {
	if len(request.Text) > MaxSourceBytes {
		return nil, fmt.Errorf("C source exceeds %d bytes", MaxSourceBytes)
	}
	hostPath, err := s.validateCRef(workspaceID, request.Ref)
	if err != nil {
		return nil, err
	}
	config, err := s.Config(workspaceID)
	if err != nil || !config.CodeLens {
		return nil, err
	}
	lenses := make([]Lens, 0, 4)
	for _, target := range config.Targets {
		resolved, resolveErr := s.resolveTarget(workspaceID, target, false)
		if resolveErr != nil || !samePath(resolved.entryPath, hostPath) {
			continue
		}
		functionRange, ok := discoverFunction(request.Text, target.Entry.Function)
		if !ok {
			continue
		}
		lenses = append(lenses,
			Lens{Range: functionRange, Title: "run C tests: " + target.Name, Action: "run", TargetID: target.ID},
			Lens{Range: functionRange, Title: "debug C tests: " + target.Name, Action: "debug", TargetID: target.ID},
		)
	}
	return lenses, nil
}

func (s *Service) Run(workspaceID string, request RunRequest) (terminal.Snapshot, error) {
	target, err := s.savedTarget(workspaceID, request.TargetID)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	config, err := s.Config(workspaceID)
	if err != nil {
		return terminal.Snapshot{}, err
	}
	coverageEnabled := config.Coverage
	generation := ""
	scratch := ""
	if coverageEnabled {
		generation = s.beginCoverage(workspaceID, target)
		scratch = filepath.Join(target.workspace.MainPath, workspaces.EchoDirName, "coverage", generation)
		if err := os.MkdirAll(scratch, 0o700); err != nil {
			s.abortCoverage(workspaceID, generation)
			return terminal.Snapshot{}, fmt.Errorf("prepare C coverage: %w", err)
		}
	}

	var fingerprint string
	prepare := func() error {
		if _, err := os.Stat(target.executable); err != nil {
			return fmt.Errorf("C test executable is unavailable: %w", err)
		}
		if !coverageEnabled {
			return nil
		}
		if target.config.Coverage.Provider == "gcov" {
			if err := clearGcovCounters(target.objectRoots); err != nil {
				return err
			}
		}
		var err error
		fingerprint, err = sourceFingerprint(target.sourceRoots)
		return err
	}

	steps := make([]terminal.TaskStep, 0, 2)
	if target.build != nil {
		build := *target.build
		steps = append(steps, terminal.TaskStep{
			Command: build.command, Args: build.args, WorkingDirectory: build.cwd, Environment: build.environment,
			DisplayCommand: displayCommand(build.command, build.args), Timeout: build.timeout, After: prepare,
		})
	} else if err := prepare(); err != nil {
		if scratch != "" {
			_ = os.RemoveAll(scratch)
			s.abortCoverage(workspaceID, generation)
		}
		return terminal.Snapshot{}, err
	}

	runEnvironment := cloneEnvironment(target.runtimeEnvironment)
	if coverageEnabled && target.config.Coverage.Provider == "llvm" {
		runtimeScratch, mapErr := s.runtimePath(workspaceID, scratch)
		if mapErr != nil {
			_ = os.RemoveAll(scratch)
			s.abortCoverage(workspaceID, generation)
			return terminal.Snapshot{}, mapErr
		}
		runEnvironment["LLVM_PROFILE_FILE"] = filepath.Join(runtimeScratch, "profile-%p.profraw")
		if target.workspace.Sandbox.Enabled {
			runEnvironment["LLVM_PROFILE_FILE"] = strings.ReplaceAll(runEnvironment["LLVM_PROFILE_FILE"], `\`, "/")
		}
	}
	timeout, _ := time.ParseDuration(target.config.Timeout)
	steps = append(steps, terminal.TaskStep{
		Command: target.runtimeExecutable, Args: target.runtimeArgs, WorkingDirectory: target.cwd,
		Environment: runEnvironment, DisplayCommand: displayCommand(target.runtimeExecutable, target.runtimeArgs), Timeout: timeout,
	})
	snapshot, err := s.terminal.StartTask(workspaceID, terminal.TaskRequest{
		Name: "Test Output", Steps: steps,
		OnExit: func(result terminal.TaskResult) {
			if coverageEnabled {
				s.finishCoverage(workspaceID, generation, scratch, fingerprint, target, result)
			}
		},
	})
	if err != nil {
		if scratch != "" {
			_ = os.RemoveAll(scratch)
			s.abortCoverage(workspaceID, generation)
		}
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
	target, err := s.savedTarget(workspaceID, request.TargetID)
	if err != nil {
		return debugger.Snapshot{}, err
	}
	profile, err := s.debugger.EnabledAdapterProfile(workspaceID, "lldb")
	if err != nil {
		return debugger.Snapshot{}, err
	}
	arguments := map[string]any{
		"program": target.config.Executable,
		"args":    stringsToAny(target.config.Args),
		"cwd":     target.config.Cwd,
		"env":     stringsMapToAny(target.config.Environment),
	}
	configuration := debugconfig.Configuration{
		ID: "c-codelens-" + target.config.ID, Name: "Debug C Tests: " + target.config.Name,
		AdapterProfileID: profile.ID, Request: "launch", Arguments: arguments,
	}
	if target.config.Build != nil {
		buildTimeout, _ := time.ParseDuration(target.config.Build.Timeout)
		configuration.PreLaunch = &debugconfig.LifecycleHook{
			Command: target.config.Build.Command, Args: target.config.Build.Args, Cwd: target.config.Build.Cwd,
			Environment: target.config.Build.Environment, TimeoutMS: int(buildTimeout / time.Millisecond),
		}
	}
	return s.debugger.StartTransient(ctx, workspaceID, configuration, debugger.StartRequest{
		CurrentFile: &debugconfig.SourceRef{RootID: target.entryRef.RootID, Path: target.entryRef.Path},
	})
}

func (s *Service) savedTarget(workspaceID, targetID string) (resolvedTarget, error) {
	config, err := s.Config(workspaceID)
	if err != nil {
		return resolvedTarget{}, err
	}
	targetID = strings.ToLower(strings.TrimSpace(targetID))
	for _, target := range config.Targets {
		if target.ID != targetID {
			continue
		}
		resolved, err := s.resolveTarget(workspaceID, target, true)
		if err != nil {
			return resolvedTarget{}, err
		}
		data, err := os.ReadFile(resolved.entryPath)
		if err != nil {
			return resolvedTarget{}, err
		}
		if len(data) > MaxSourceBytes {
			return resolvedTarget{}, fmt.Errorf("C source exceeds %d bytes", MaxSourceBytes)
		}
		if _, ok := discoverFunction(string(data), target.Entry.Function); !ok {
			return resolvedTarget{}, ErrEntryNotFound
		}
		return resolved, nil
	}
	return resolvedTarget{}, ErrTargetNotFound
}

func (s *Service) resolveTarget(workspaceID string, target gotestconfig.CTarget, requireDirectories bool) (resolvedTarget, error) {
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return resolvedTarget{}, err
	}
	if !ok {
		return resolvedTarget{}, fmt.Errorf("%w: %q", workspaces.ErrWorkspaceNotFound, workspaceID)
	}
	config := gotestconfig.CConfig{Targets: []gotestconfig.CTarget{target}}.Normalized()
	if err := config.Validate(); err != nil {
		return resolvedTarget{}, err
	}
	target = config.Targets[0]
	hostOptions := expansionOptions(workspace, false)
	runtimeOptions, err := s.expansionOptions(workspace)
	if err != nil {
		return resolvedTarget{}, err
	}
	expandHostPath := func(value string, mustExist, directory bool) (string, error) {
		expanded, err := debugconfig.ExpandString(value, hostOptions)
		if err != nil {
			return "", err
		}
		return confinedPath(workspace, expanded, mustExist, directory)
	}
	entryPath, err := expandHostPath(target.Entry.File, true, false)
	if err != nil || !strings.EqualFold(filepath.Ext(entryPath), ".c") {
		if err == nil {
			err = ErrNotCSourceFile
		}
		return resolvedTarget{}, err
	}
	entryRef, err := refForHostPath(s.fs, workspaceID, entryPath)
	if err != nil {
		return resolvedTarget{}, err
	}
	sourceRoots := make([]string, 0, len(target.SourceRoots))
	sourceRootRefs := make([]workspacefs.FileRef, 0, len(target.SourceRoots))
	for _, value := range target.SourceRoots {
		root, err := expandHostPath(value, requireDirectories, true)
		if err != nil {
			return resolvedTarget{}, err
		}
		ref, err := refForHostPath(s.fs, workspaceID, root)
		if err != nil {
			return resolvedTarget{}, err
		}
		sourceRoots, sourceRootRefs = append(sourceRoots, root), append(sourceRootRefs, ref)
	}
	objectRoots := make([]string, 0, len(target.Coverage.ObjectRoots))
	for _, value := range target.Coverage.ObjectRoots {
		root, err := expandHostPath(value, false, true)
		if err != nil {
			return resolvedTarget{}, err
		}
		objectRoots = append(objectRoots, root)
	}
	executable, err := expandHostPath(target.Executable, false, false)
	if err != nil {
		return resolvedTarget{}, err
	}
	cwd, err := expandHostPath(target.Cwd, true, true)
	if err != nil {
		return resolvedTarget{}, err
	}
	runtimeExecutable, err := debugconfig.ExpandString(target.Executable, runtimeOptions)
	if err != nil {
		return resolvedTarget{}, err
	}
	runtimeArgs, err := debugconfig.ExpandStrings(target.Args, runtimeOptions)
	if err != nil {
		return resolvedTarget{}, err
	}
	runtimeEnvironment, err := expandEnvironment(target.Environment, runtimeOptions)
	if err != nil {
		return resolvedTarget{}, err
	}
	result := resolvedTarget{
		config: target, entryPath: entryPath, entryRef: entryRef, sourceRoots: sourceRoots, sourceRootRefs: sourceRootRefs,
		objectRoots: objectRoots, executable: executable, cwd: cwd, runtimeExecutable: runtimeExecutable,
		runtimeArgs: runtimeArgs, runtimeEnvironment: runtimeEnvironment, workspace: workspace,
	}
	for _, value := range target.Coverage.Objects {
		host, err := expandHostPath(value, false, false)
		if err != nil {
			return resolvedTarget{}, err
		}
		_ = host
		runtimeValue, err := debugconfig.ExpandString(value, runtimeOptions)
		if err != nil {
			return resolvedTarget{}, err
		}
		result.runtimeObjects = append(result.runtimeObjects, runtimeValue)
	}
	if target.Build != nil {
		command, err := debugconfig.ExpandString(target.Build.Command, runtimeOptions)
		if err != nil {
			return resolvedTarget{}, err
		}
		args, err := debugconfig.ExpandStrings(target.Build.Args, runtimeOptions)
		if err != nil {
			return resolvedTarget{}, err
		}
		buildCwd, err := expandHostPath(target.Build.Cwd, true, true)
		if err != nil {
			return resolvedTarget{}, err
		}
		environment, err := expandEnvironment(target.Build.Environment, runtimeOptions)
		if err != nil {
			return resolvedTarget{}, err
		}
		timeout, _ := time.ParseDuration(target.Build.Timeout)
		result.build = &resolvedCommand{command: command, args: args, cwd: buildCwd, environment: environment, timeout: timeout}
	}
	return result, nil
}

func (s *Service) validateCRef(workspaceID string, ref workspacefs.FileRef) (string, error) {
	if !strings.EqualFold(filepath.Ext(filepath.FromSlash(ref.Path)), ".c") {
		return "", ErrNotCSourceFile
	}
	return s.fs.ResolveExistingHostPath(workspaceID, ref, false)
}

func (s *Service) expansionOptions(workspace workspaces.Workspace) (debugconfig.ExpandOptions, error) {
	if !workspace.Sandbox.Enabled {
		return expansionOptions(workspace, false), nil
	}
	if s.sandbox == nil {
		return debugconfig.ExpandOptions{}, fmt.Errorf("sandbox runtime is unavailable")
	}
	options := expansionOptions(workspace, true)
	main, err := s.sandbox.HostToGuest(workspace.ID, options.WorkspaceFolder)
	if err != nil {
		return debugconfig.ExpandOptions{}, fmt.Errorf("map sandbox workspace folder: %w", err)
	}
	options.WorkspaceFolder = main
	for name, folder := range options.WorkspaceFolders {
		mapped, err := s.sandbox.HostToGuest(workspace.ID, folder)
		if err != nil {
			return debugconfig.ExpandOptions{}, fmt.Errorf("map sandbox workspace folder %q: %w", name, err)
		}
		options.WorkspaceFolders[name] = mapped
	}
	return options, nil
}

func expansionOptions(workspace workspaces.Workspace, slash bool) debugconfig.ExpandOptions {
	main := workspace.MainPath
	folders := make(map[string]string, len(workspace.Folders))
	for _, folder := range workspace.Folders {
		folders[filepath.Base(folder)] = folder
	}
	return debugconfig.ExpandOptions{WorkspaceFolder: main, WorkspaceFolders: folders, SlashPaths: slash}
}

func confinedPath(workspace workspaces.Workspace, value string, mustExist, directory bool) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) {
		value = filepath.Join(workspace.MainPath, value)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if !containedInWorkspace(workspace, absolute) {
		return "", fmt.Errorf("C testing path is outside the workspace: %s", absolute)
	}
	if mustExist {
		info, err := os.Stat(absolute)
		if err != nil {
			return "", err
		}
		if directory != info.IsDir() {
			return "", fmt.Errorf("C testing path has the wrong kind: %s", absolute)
		}
	}
	return absolute, nil
}

func containedInWorkspace(workspace workspaces.Workspace, candidate string) bool {
	canonicalCandidate, err := canonicalizeForContainment(candidate)
	if err != nil {
		return false
	}
	for _, root := range workspace.Folders {
		canonicalRoot, rootErr := canonicalizeForContainment(root)
		if rootErr == nil && pathWithin(canonicalRoot, canonicalCandidate) {
			return true
		}
	}
	return false
}

func canonicalizeForContainment(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	probe := absolute
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			relative, relErr := filepath.Rel(probe, absolute)
			if relErr != nil {
				return "", relErr
			}
			if relative == "." {
				return filepath.Clean(resolved), nil
			}
			return filepath.Clean(filepath.Join(resolved, relative)), nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", resolveErr
		}
		probe = parent
	}
}

func refForHostPath(fs *workspacefs.Service, workspaceID, hostPath string) (workspacefs.FileRef, error) {
	roots, err := fs.Roots(workspaceID)
	if err != nil {
		return workspacefs.FileRef{}, err
	}
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i].HostPath) > len(roots[j].HostPath) })
	for _, root := range roots {
		if !pathWithin(root.HostPath, hostPath) {
			continue
		}
		relative, err := filepath.Rel(root.HostPath, hostPath)
		if err != nil {
			continue
		}
		if relative == "." {
			relative = ""
		} else if runtime.GOOS == "windows" {
			relative = actualRelativeCase(root.HostPath, relative)
		}
		return workspacefs.FileRef{RootID: root.ID, Path: filepath.ToSlash(relative)}, nil
	}
	return workspacefs.FileRef{}, fmt.Errorf("path is outside the workspace")
}

func actualRelativeCase(root, relative string) string {
	parts := strings.FieldsFunc(relative, func(character rune) bool { return character == '/' || character == '\\' })
	current := root
	for index, part := range parts {
		entries, err := os.ReadDir(current)
		if err == nil {
			for _, entry := range entries {
				if strings.EqualFold(entry.Name(), part) {
					parts[index] = entry.Name()
					break
				}
			}
		}
		current = filepath.Join(current, parts[index])
	}
	return filepath.Join(parts...)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return false
	}
	if runtime.GOOS == "windows" {
		root = strings.ToLower(filepath.Clean(root))
		candidate = strings.ToLower(filepath.Clean(candidate))
		return candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator))
	}
	return true
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func expandEnvironment(values map[string]string, options debugconfig.ExpandOptions) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for key, value := range values {
		expanded, err := debugconfig.ExpandString(value, options)
		if err != nil {
			return nil, err
		}
		result[key] = expanded
	}
	return result, nil
}

func cloneEnvironment(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	return result
}

func displayCommand(command string, args []string) string {
	values := append([]string{command}, args...)
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, " \t\r\n\"") {
			values[index] = strconv.Quote(value)
		}
	}
	return strings.Join(values, " ")
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func stringsMapToAny(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func (s *Service) runtimePath(workspaceID, hostPath string) (string, error) {
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return hostPath, err
	}
	if !ok {
		return "", fmt.Errorf("%w: %q", workspaces.ErrWorkspaceNotFound, workspaceID)
	}
	if !workspace.Sandbox.Enabled {
		return hostPath, nil
	}
	if s.sandbox == nil {
		return "", fmt.Errorf("sandbox runtime is unavailable")
	}
	return s.sandbox.HostToGuest(workspaceID, hostPath)
}

func (s *Service) RemoveWorkspace(workspaceID string) {
	s.mu.Lock()
	delete(s.latest, workspaceID)
	delete(s.coverage, workspaceID)
	s.mu.Unlock()
}

func (s *Service) targetByID(workspaceID, targetID string) (gotestconfig.CTarget, error) {
	config, err := s.Config(workspaceID)
	if err != nil {
		return gotestconfig.CTarget{}, err
	}
	for _, target := range config.Targets {
		if target.ID == targetID {
			return target, nil
		}
	}
	return gotestconfig.CTarget{}, ErrTargetNotFound
}

func newGeneration() string { return uuid.NewString() }
