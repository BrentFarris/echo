package gitservice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	workspacefs "github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/scmexec"
	"github.com/brent/echo/internal/sourcecontrol"
)

const (
	localCommandTimeout   = 30 * time.Second
	networkCommandTimeout = 5 * time.Minute
	maximumCommandOutput  = 64 << 20
)

type repositoryScope struct {
	Scope
	rootPath      string
	rootRefPrefix string
}

type rootInfo struct {
	workspacefs.Root
	canonical string
}

type repositoryState struct {
	workspaceID  string
	root         string
	gitDir       string
	commonDir    string
	label        string
	parent       bool
	rootRef      *workspacefs.FileRef
	scopes       []repositoryScope
	revision     atomic.Uint64
	mutationMu   *sync.Mutex
	refreshMu    sync.Mutex
	refreshing   bool
	refreshAgain bool
	scheduleMu   sync.Mutex
	refreshTimer *time.Timer
}

func (r *repositoryState) public() Repository {
	scopes := make([]Scope, len(r.scopes))
	for index := range r.scopes {
		scopes[index] = r.scopes[index].Scope
	}
	var rootRef *workspacefs.FileRef
	if r.rootRef != nil {
		copy := *r.rootRef
		rootRef = &copy
	}
	return Repository{
		ID: repositoryID(r.workspaceID, r.root), Label: r.label, RootRef: rootRef,
		Parent: r.parent, Scopes: scopes, Revision: r.revision.Load(),
	}
}

type Service struct {
	workspaces   *scmexec.Workspaces
	fs           *workspacefs.Service
	mu           sync.RWMutex
	repos        map[string]map[string]*repositoryState
	locks        map[string]*sync.Mutex
	notify       func(Event)
	watches      map[string]*workspaceWatch
	statusSlots  chan struct{}
	runner       *scmexec.Runtime
	execStops    map[string]context.CancelFunc
	execContexts map[string]context.Context
}

func New(workspaces *scmexec.Workspaces, fs *workspacefs.Service) *Service {
	return &Service{
		workspaces: workspaces, fs: fs, locks: make(map[string]*sync.Mutex), repos: make(map[string]map[string]*repositoryState),
		watches: make(map[string]*workspaceWatch), statusSlots: make(chan struct{}, 4),
		execStops: make(map[string]context.CancelFunc), execContexts: make(map[string]context.Context),
	}
}

func (s *Service) SetNotifier(notify func(Event)) {
	s.mu.Lock()
	s.notify = notify
	s.mu.Unlock()
}

func (s *Service) SetRuntime(manager *scmexec.Runtime) {
	s.mu.Lock()
	s.runner = manager
	s.mu.Unlock()
}

func (s *Service) Version(ctx context.Context, workspaceID string) (string, error) {
	roots, err := s.fs.Roots(workspaceID)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", &Error{Code: "workspace_not_found", Message: "workspace folders are unavailable"}
	}
	ctx = s.executionContext(ctx, workspaceID)
	output, err := runGit(ctx, roots[0].HostPath, nil, true, "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(output)), "git version")), nil
}

func (s *Service) Available(workspaceID string) error {
	s.mu.RLock()
	runner := s.runner
	s.mu.RUnlock()
	if runner == nil || runner.Available == nil {
		return errors.New("Git execution scope unavailable")
	}
	return runner.Available(workspaceID, "git")
}

type gitExecutionContextKey struct{}

type gitExecutionContext struct {
	manager     *scmexec.Runtime
	workspaceID string
	stop        context.Context
}

func (s *Service) executionContext(ctx context.Context, workspaceID string) context.Context {
	s.mu.Lock()
	manager := s.runner
	if s.execContexts == nil {
		s.execContexts = make(map[string]context.Context)
		s.execStops = make(map[string]context.CancelFunc)
	}
	stop := s.execContexts[workspaceID]
	if stop == nil {
		var cancel context.CancelFunc
		stop, cancel = context.WithCancel(context.Background())
		s.execContexts[workspaceID], s.execStops[workspaceID] = stop, cancel
	}
	s.mu.Unlock()

	return context.WithValue(ctx, gitExecutionContextKey{}, gitExecutionContext{manager: manager, workspaceID: workspaceID, stop: stop})
}

// StopWorkspaceProcesses invalidates the current sandbox Git execution epoch.
// New operations receive a fresh context after the routing transition.
func (s *Service) StopWorkspaceProcesses(workspaceID string) {
	s.mu.Lock()
	if s.execContexts == nil {
		s.execContexts = make(map[string]context.Context)
		s.execStops = make(map[string]context.CancelFunc)
	}
	if cancel := s.execStops[workspaceID]; cancel != nil {
		cancel()
	}
	stop, cancel := context.WithCancel(context.Background())
	s.execContexts[workspaceID], s.execStops[workspaceID] = stop, cancel
	s.mu.Unlock()
}

func (s *Service) emit(event Event) {
	s.mu.RLock()
	notify := s.notify
	s.mu.RUnlock()
	if notify != nil {
		notify(event)
	}
}

func (s *Service) Repositories(ctx context.Context, workspaceID string) ([]Repository, error) {
	ctx = s.executionContext(ctx, workspaceID)
	states, err := s.discover(ctx, strings.TrimSpace(workspaceID))
	repositories := make([]Repository, 0, len(states))
	for _, state := range states {
		repositories = append(repositories, state.public())
	}
	s.syncWorkspaceWatch(workspaceID, states)
	sort.Slice(repositories, func(i, j int) bool {
		left, right := strings.ToLower(repositories[i].Label), strings.ToLower(repositories[j].Label)
		if left == right {
			return repositories[i].ID < repositories[j].ID
		}
		return left < right
	})
	return repositories, err
}

func (s *Service) discover(ctx context.Context, workspaceID string) ([]*repositoryState, error) {
	ctx = s.executionContext(ctx, workspaceID)
	workspace, ok, err := s.workspaces.Get(workspaceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &Error{Code: "workspace_not_found", Message: "workspace not found", Cause: ErrNotFound}
	}
	roots, err := s.fs.Roots(workspaceID)
	if err != nil {
		return nil, err
	}
	rootInfos := make([]rootInfo, 0, len(roots))
	for _, root := range roots {
		canonical, canonicalErr := canonicalExisting(root.HostPath)
		if canonicalErr != nil {
			continue
		}
		rootInfos = append(rootInfos, rootInfo{Root: root, canonical: canonical})
	}

	var discoveryErr error
	candidates := make(map[string]bool)
	for _, root := range rootInfos {
		if hasGitMarker(root.canonical) {
			candidates[root.canonical] = true
		}
		found, scanErr := scmexec.Discover(ctx, root.canonical, hasGitMarker, func(path string) bool { _, err := s.runner.ValidatePath(workspaceID, path); return err == nil })
		if scanErr != nil {
			discoveryErr = scanErr
		}
		for _, candidate := range found {
			candidates[candidate] = true
		}
		if workspace.SearchParentRepositories || workspace.SearchParentGitRepositories {
			candidates[root.canonical] = true
		}
	}

	discoveredRoots := make(map[string]string)
	for candidate := range candidates {
		root, discoverErr := discoverRepositoryRoot(ctx, candidate)
		if discoverErr != nil {
			discoveryErr = discoverErr
			continue
		}
		key := pathIdentity(root)
		discoveredRoots[key] = root
	}

	s.mu.Lock()
	existing := s.repos[workspaceID]
	if existing == nil {
		existing = make(map[string]*repositoryState)
	}
	next := make(map[string]*repositoryState, len(discoveredRoots))
	states := make([]*repositoryState, 0, len(discoveredRoots))
	for _, repositoryRoot := range discoveredRoots {
		id := repositoryID(workspaceID, repositoryRoot)
		scopes, rootRef, parent := scopesForRepository(repositoryRoot, rootInfos)
		if len(scopes) == 0 {
			continue
		}
		state := existing[id]
		if state == nil {
			gitDir, _ := discoverGitDir(ctx, repositoryRoot)
			commonDir, _ := discoverCommonGitDir(ctx, repositoryRoot)
			lock := s.locks[pathIdentity(commonDir)]
			if lock == nil {
				lock = &sync.Mutex{}
				s.locks[pathIdentity(commonDir)] = lock
			}
			state = &repositoryState{mutationMu: lock,
				workspaceID: workspaceID, root: repositoryRoot, gitDir: gitDir, commonDir: commonDir,
				label: filepath.Base(repositoryRoot), parent: parent, rootRef: rootRef, scopes: scopes,
			}
			state.revision.Store(1)
		} else {
			state.label = filepath.Base(repositoryRoot)
			state.parent = parent
			state.rootRef = rootRef
			state.scopes = scopes
		}
		next[id] = state
		states = append(states, state)
	}
	for id, state := range existing {
		if next[id] != nil {
			continue
		}
		state.scheduleMu.Lock()
		if state.refreshTimer != nil {
			state.refreshTimer.Stop()
			state.refreshTimer = nil
		}
		state.scheduleMu.Unlock()
	}
	s.repos[workspaceID] = next
	s.mu.Unlock()
	metadataRefs := make([]workspacefs.FileRef, 0, len(states)*3)
	for _, state := range states {
		if ref, ok := state.refForPath(".git"); ok && ref != nil {
			metadataRefs = append(metadataRefs, *ref)
		}
		for _, metadataPath := range []string{state.gitDir, state.commonDir} {
			if strings.TrimSpace(metadataPath) == "" {
				continue
			}
			for _, root := range rootInfos {
				if relative, ok := relativeWithin(root.canonical, metadataPath); ok && relative != "" {
					metadataRefs = append(metadataRefs, workspacefs.FileRef{RootID: root.ID, Path: relative})
					break
				}
			}
		}
	}
	s.fs.SetSourceControlMetadata(workspaceID, "git", metadataRefs)
	return states, discoveryErr
}

func scopesForRepository(repositoryRoot string, roots []rootInfo) ([]repositoryScope, *workspacefs.FileRef, bool) {
	scopes := make([]repositoryScope, 0, len(roots))
	parent := true
	var rootRef *workspacefs.FileRef
	rootRefSpecificity := -1
	seen := make(map[string]bool)
	for _, root := range roots {
		if prefix, ok := relativeWithin(repositoryRoot, root.canonical); ok {
			key := root.ID + "\x00" + prefix
			if !seen[key] {
				seen[key] = true
				scopes = append(scopes, repositoryScope{
					Scope:    Scope{RootID: root.ID, RootLabel: root.Label, RepoPrefix: prefix},
					rootPath: root.canonical,
				})
			}
			if prefix == "" {
				parent = false
				if len(root.canonical) > rootRefSpecificity {
					rootRefSpecificity = len(root.canonical)
					rootRef = &workspacefs.FileRef{RootID: root.ID, Path: ""}
				}
			}
			continue
		}
		if prefix, ok := relativeWithin(root.canonical, repositoryRoot); ok {
			parent = false
			key := root.ID + "\x00"
			if !seen[key] {
				seen[key] = true
				scopes = append(scopes, repositoryScope{
					Scope:    Scope{RootID: root.ID, RootLabel: root.Label},
					rootPath: root.canonical, rootRefPrefix: prefix,
				})
			}
			if len(root.canonical) > rootRefSpecificity {
				rootRefSpecificity = len(root.canonical)
				rootRef = &workspacefs.FileRef{RootID: root.ID, Path: prefix}
			}
		}
	}
	sort.SliceStable(scopes, func(i, j int) bool {
		return len(scopes[i].rootPath) > len(scopes[j].rootPath)
	})
	return scopes, rootRef, parent
}

func (s *Service) repository(ctx context.Context, workspaceID, repositoryID string) (*repositoryState, error) {
	ctx = s.executionContext(ctx, workspaceID)
	s.mu.RLock()
	state := s.repos[workspaceID][repositoryID]
	s.mu.RUnlock()
	if state != nil {
		roots, err := s.fs.Roots(workspaceID)
		if err != nil {
			return nil, err
		}
		for _, scope := range state.scopes {
			valid := false
			for _, root := range roots {
				if root.ID == scope.RootID && root.BlockedReason == "" {
					valid = true
				}
			}
			if !valid {
				return nil, &Error{Code: "stale_workspace_scope", Message: "Workspace folders changed; refresh Source Control before continuing"}
			}
		}
		return state, nil
	}
	if _, err := s.discover(ctx, workspaceID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	state = s.repos[workspaceID][repositoryID]
	s.mu.RUnlock()
	if state == nil {
		return nil, &Error{Code: "repository_not_found", Message: "Git repository not found", Cause: ErrNotFound}
	}
	return state, nil
}

func repositoryID(workspaceID, root string) string {
	return sourcecontrol.RepositoryID(workspaceID, "git", root)
}

func pathIdentity(value string) string {
	value = filepath.Clean(value)
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func canonicalExisting(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

func hasGitMarker(directory string) bool {
	_, err := os.Lstat(filepath.Join(directory, ".git"))
	return err == nil
}

func discoverRepositoryRoot(parent context.Context, directory string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	output, err := runGit(ctx, directory, nil, true, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return canonicalExisting(mappedGitPath(ctx, strings.TrimSpace(string(output))))
}

func discoverGitDir(parent context.Context, root string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	output, err := runGit(ctx, root, nil, true, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return canonicalExisting(mappedGitPath(ctx, strings.TrimSpace(string(output))))
}

func discoverCommonGitDir(parent context.Context, root string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, localCommandTimeout)
	defer cancel()
	output, err := runGit(ctx, root, nil, true, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return canonicalExisting(mappedGitPath(ctx, strings.TrimSpace(string(output))))
}

func mappedGitPath(ctx context.Context, value string) string {
	execution, ok := ctx.Value(gitExecutionContextKey{}).(gitExecutionContext)
	if !ok || execution.manager == nil {
		return value
	}
	hostPath, err := execution.manager.ValidatePath(execution.workspaceID, value)
	if err != nil {
		return value
	}
	return hostPath
}

func runGit(ctx context.Context, root string, input []byte, readOnly bool, args ...string) ([]byte, error) {
	execution, ok := ctx.Value(gitExecutionContextKey{}).(gitExecutionContext)
	if !ok || execution.manager == nil {
		return nil, errors.New("Git execution scope unavailable")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(execution.stop, cancel)
	defer stop()
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if readOnly {
		env = append(env, "GIT_OPTIONAL_LOCKS=0")
	}
	result, err := execution.manager.Execute(runCtx, execution.workspaceID, scmexec.Request{Command: append([]string{"git", "-c", "core.quotepath=false"}, args...), WorkingDirectory: root, Environment: env, Input: input, OutputLimit: maximumCommandOutput})
	if err != nil {
		return nil, err
	}
	if result.StdoutTruncated {
		return nil, errors.New("Git output exceeds the capture limit")
	}
	if result.ExitCode != 0 {
		return result.Stdout, &Error{Code: "git_command_failed", Message: sanitizeGitOutput(string(result.Stderr)+string(result.Stdout), root)}
	}
	return result.Stdout, nil
}

type cappedBuffer struct {
	buffer  bytes.Buffer
	limit   int64
	written int64
}

func (w *cappedBuffer) Write(data []byte) (int, error) {
	if w.written >= w.limit {
		return len(data), nil
	}
	remaining := w.limit - w.written
	write := data
	if int64(len(write)) > remaining {
		write = write[:remaining]
	}
	_, _ = w.buffer.Write(write)
	w.written += int64(len(write))
	return len(data), nil
}

func (w *cappedBuffer) Bytes() []byte  { return w.buffer.Bytes() }
func (w *cappedBuffer) String() string { return w.buffer.String() }

func cleanGitPath(value string) (string, error) {
	if strings.ContainsRune(value, 0) || filepath.IsAbs(value) {
		return "", &Error{Code: "invalid_git_path", Message: "Git path must be repository-relative", Cause: ErrInvalidPath}
	}
	if value == "" {
		return "", &Error{Code: "invalid_git_path", Message: "Git path is invalid", Cause: ErrInvalidPath}
	}
	value = strings.ReplaceAll(value, "\\", "/")
	value = filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if value == "." || value == "" || value == ".." || strings.HasPrefix(value, "../") {
		return "", &Error{Code: "invalid_git_path", Message: "Git path is invalid", Cause: ErrInvalidPath}
	}
	return value, nil
}

var credentialURLPattern = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
var gitLineEndingWarningPattern = regexp.MustCompile(`(?i)^warning: (?:in the working copy of '.+', )?(?:LF|CRLF) will be replaced by (?:LF|CRLF)(?: the next time Git touches it| in .+)\.?$`)

func isAuthenticationFailure(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"authentication failed", "could not read username", "could not read password",
		"terminal prompts disabled", "permission denied (publickey)", "publickey authentication failed",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sanitizeGitOutput(message, root string) string {
	message = strings.ReplaceAll(message, "\x00", "")
	message = credentialURLPattern.ReplaceAllString(message, "$1")
	for _, value := range []string{root, filepath.ToSlash(root)} {
		if value == "" {
			continue
		}
		if runtime.GOOS == "windows" {
			message = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(value)).ReplaceAllString(message, "<repository>")
		} else {
			message = strings.ReplaceAll(message, value, "<repository>")
		}
	}
	message = stripHarmlessGitWarnings(message)
	const safeOutputLimit = 8 << 10
	if len(message) > safeOutputLimit {
		message = message[:safeOutputLimit] + "…"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "Git command failed"
	}
	return message
}

// stripHarmlessGitWarnings removes Git's line-ending conversion notices from
// failed-command output. Those notices can number in the thousands and bury
// the actual error that caused the command to fail. Other warnings and hints
// remain visible.
func stripHarmlessGitWarnings(message string) string {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	filtered := make([]string, 0, len(lines))
	skipLegacyDetail := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if gitLineEndingWarningPattern.MatchString(trimmed) {
			skipLegacyDetail = !strings.Contains(strings.ToLower(trimmed), "the next time git touches it")
			continue
		}
		if skipLegacyDetail && strings.EqualFold(trimmed, "The file will have its original line endings in your working directory.") {
			skipLegacyDetail = false
			continue
		}
		skipLegacyDetail = false
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\n")
}

func relativeWithin(root, target string) (string, bool) {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	if relative == "." {
		return "", true
	}
	return filepath.ToSlash(relative), true
}

func (r *repositoryState) refForPath(gitPath string) (*workspacefs.FileRef, bool) {
	clean, err := cleanGitPath(gitPath)
	if err != nil {
		return nil, false
	}
	for _, scope := range r.scopes {
		if scope.RepoPrefix != "" && clean != scope.RepoPrefix && !strings.HasPrefix(clean, scope.RepoPrefix+"/") {
			continue
		}
		relative := clean
		if scope.RepoPrefix != "" {
			relative = strings.TrimPrefix(strings.TrimPrefix(clean, scope.RepoPrefix), "/")
		} else if scope.rootRefPrefix != "" {
			relative = strings.TrimSuffix(scope.rootRefPrefix, "/") + "/" + clean
		}
		return &workspacefs.FileRef{RootID: scope.RootID, Path: relative}, true
	}
	return nil, false
}

func (r *repositoryState) pathAllowed(gitPath string) bool {
	ref, ok := r.refForPath(gitPath)
	return ok && !workspacefs.IsProtectedWorkspaceMetadataPath(ref.Path)
}

func (r *repositoryState) protectedMetadataPath(gitPath string) bool {
	ref, ok := r.refForPath(gitPath)
	return ok && workspacefs.IsProtectedWorkspaceMetadataPath(ref.Path)
}

func (r *repositoryState) validatePaths(paths []string) ([]string, error) {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]bool)
	for _, item := range paths {
		path, err := cleanGitPath(item)
		if err != nil {
			return nil, err
		}
		ref, scoped := r.refForPath(path)
		if scoped && workspacefs.IsProtectedWorkspaceMetadataPath(ref.Path) {
			return nil, &Error{Code: "protected_workspace_metadata", Message: "workspace metadata is managed by Echo and cannot be changed through source control"}
		}
		if !scoped {
			return nil, &Error{Code: "path_outside_workspace", Message: "Git path is outside this workspace", Cause: ErrInvalidPath}
		}
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			cleaned = append(cleaned, path)
		}
	}
	if len(cleaned) == 0 {
		return nil, &Error{Code: "paths_required", Message: "select at least one changed file", Cause: ErrInvalidPath}
	}
	return cleaned, nil
}

func (r *repositoryState) scopePathspecs() []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, len(r.scopes)*3)
	for _, scope := range r.scopes {
		path := scope.RepoPrefix
		if path == "" {
			path = "."
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for _, scope := range r.scopes {
		prefix := strings.Trim(strings.TrimSpace(scope.RepoPrefix), "/")
		for _, metadata := range []string{".echo/workspace.json", ".echo/icon.*"} {
			path := metadata
			if prefix != "" {
				path = prefix + "/" + metadata
			}
			pathspec := ":(exclude)" + path
			if !seen[pathspec] {
				seen[pathspec] = true
				paths = append(paths, pathspec)
			}
		}
	}
	return paths
}

// compile-time guards for small helpers used by cappedBuffer.
var _ io.Writer = (*cappedBuffer)(nil)
