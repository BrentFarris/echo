// Package fossil implements Fossil SCM as a built-in source control provider.
package fossil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/brent/echo/internal/sandbox"
	"github.com/brent/echo/internal/sourcecontrol"
	"github.com/brent/echo/internal/workspacefs"
	"github.com/brent/echo/internal/workspaces"
)

const ID = "fossil"

var capabilities = []sourcecontrol.Capability{
	sourcecontrol.CapabilityStatus, sourcecontrol.CapabilityDiff, sourcecontrol.CapabilityHistory,
	sourcecontrol.CapabilityTrack, sourcecontrol.CapabilityCommitAll, sourcecontrol.CapabilityCommitSelected,
	sourcecontrol.CapabilityUpdate, sourcecontrol.CapabilitySync, sourcecontrol.CapabilityPull,
	sourcecontrol.CapabilityPush, sourcecontrol.CapabilityBranches, sourcecontrol.CapabilityMerge,
	sourcecontrol.CapabilityStashes,
}

type repositoryScope struct {
	sourcecontrol.Scope
	rootPath      string
	rootRefPrefix string
}

type rootInfo struct {
	workspacefs.Root
	canonical string
}

type repositoryState struct {
	workspaceID string
	root        string
	repository  string
	label       string
	parent      bool
	rootRef     *workspacefs.FileRef
	scopes      []repositoryScope
	available   bool
	diagnostic  string
	revision    atomic.Uint64
	mutationMu  sync.Mutex
}

func (r *repositoryState) public() sourcecontrol.Repository {
	scopes := make([]sourcecontrol.Scope, len(r.scopes))
	for index := range r.scopes {
		scopes[index] = r.scopes[index].Scope
	}
	var rootRef *workspacefs.FileRef
	if r.rootRef != nil {
		copy := *r.rootRef
		rootRef = &copy
	}
	return sourcecontrol.Repository{
		ID: sourcecontrol.RepositoryID(r.workspaceID, ID, r.root), ProviderID: ID, ProviderLabel: "Fossil",
		Label: r.label, RootRef: rootRef, Parent: r.parent, Scopes: scopes, Revision: r.revision.Load(),
		Available: r.available, Diagnostic: r.diagnostic, Capabilities: append([]sourcecontrol.Capability(nil), capabilities...),
	}
}

type Provider struct {
	workspaces *workspaces.Manager
	fs         *workspacefs.Service
	sandbox    *sandbox.Manager
	mu         sync.RWMutex
	repos      map[string]map[string]*repositoryState
}

func New(workspaces *workspaces.Manager, fs *workspacefs.Service, sandboxManager *sandbox.Manager) *Provider {
	return &Provider{workspaces: workspaces, fs: fs, sandbox: sandboxManager, repos: make(map[string]map[string]*repositoryState)}
}

func (p *Provider) Descriptor(ctx context.Context, workspaceID string) sourcecontrol.ProviderDescriptor {
	descriptor := sourcecontrol.ProviderDescriptor{ID: ID, Label: "Fossil", Capabilities: append([]sourcecontrol.Capability(nil), capabilities...)}
	if strings.TrimSpace(workspaceID) == "" {
		return descriptor
	}
	roots, err := p.fs.Roots(workspaceID)
	if err != nil || len(roots) == 0 {
		descriptor.Diagnostic = "Workspace folders are unavailable"
		return descriptor
	}
	output, err := p.run(ctx, workspaceID, roots[0].HostPath, false, "version")
	if err != nil {
		descriptor.Diagnostic = providerDiagnostic(err)
		return descriptor
	}
	descriptor.Available = true
	descriptor.Version = strings.TrimSpace(strings.TrimPrefix(strings.SplitN(string(output), "\n", 2)[0], "This is fossil version"))
	return descriptor
}

func (p *Provider) Repositories(ctx context.Context, workspaceID string) ([]sourcecontrol.Repository, error) {
	states, err := p.discover(ctx, strings.TrimSpace(workspaceID))
	if err != nil {
		return nil, err
	}
	repositories := make([]sourcecontrol.Repository, 0, len(states))
	for _, state := range states {
		repositories = append(repositories, state.public())
	}
	sort.SliceStable(repositories, func(i, j int) bool {
		left, right := strings.ToLower(repositories[i].Label), strings.ToLower(repositories[j].Label)
		if left == right {
			return repositories[i].ID < repositories[j].ID
		}
		return left < right
	})
	return repositories, nil
}

func (p *Provider) discover(ctx context.Context, workspaceID string) ([]*repositoryState, error) {
	workspace, ok, err := p.workspaces.Get(workspaceID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &sourcecontrol.Error{Code: "workspace_not_found", Message: "workspace not found", Cause: sourcecontrol.ErrNotFound}
	}
	roots, err := p.fs.Roots(workspaceID)
	if err != nil {
		return nil, err
	}
	rootInfos := make([]rootInfo, 0, len(roots))
	for _, root := range roots {
		canonical, canonicalErr := canonicalExisting(root.HostPath)
		if canonicalErr == nil {
			rootInfos = append(rootInfos, rootInfo{Root: root, canonical: canonical})
		}
	}
	candidates := make(map[string]bool)
	for _, root := range rootInfos {
		if hasCheckoutMarker(root.canonical) {
			candidates[root.canonical] = true
		}
		children, readErr := os.ReadDir(root.canonical)
		if readErr == nil {
			for _, child := range children {
				if !child.IsDir() || child.Type()&os.ModeSymlink != 0 {
					continue
				}
				name := strings.ToLower(child.Name())
				if name == ".git" || name == ".echo" || name == "node_modules" {
					continue
				}
				candidate := filepath.Join(root.canonical, child.Name())
				if hasCheckoutMarker(candidate) {
					candidates[candidate] = true
				}
			}
		}
		if workspace.SearchParentRepositories || workspace.SearchParentGitRepositories {
			if ancestor := nearestCheckoutRoot(root.canonical); ancestor != "" {
				candidates[ancestor] = true
			}
		}
	}

	type discovered struct {
		root, repository, diagnostic string
		available                    bool
	}
	discoveredRoots := make(map[string]discovered)
	for candidate := range candidates {
		info, infoErr := p.checkoutInfo(ctx, workspaceID, candidate)
		if infoErr != nil {
			discoveredRoots[pathIdentity(candidate)] = discovered{root: candidate, available: false, diagnostic: providerDiagnostic(infoErr)}
			continue
		}
		root := info.LocalRoot
		if root == "" {
			root = candidate
		}
		// Native validation must identify the candidate itself as the checkout
		// root. This prevents a legacy .fos-looking file inside an ancestor
		// checkout from accidentally enabling parent-repository discovery.
		if pathIdentity(root) != pathIdentity(candidate) {
			continue
		}
		discoveredRoots[pathIdentity(root)] = discovered{root: root, repository: info.Repository, available: true}
	}

	p.mu.Lock()
	existing := p.repos[workspaceID]
	if existing == nil {
		existing = make(map[string]*repositoryState)
	}
	next := make(map[string]*repositoryState, len(discoveredRoots))
	states := make([]*repositoryState, 0, len(discoveredRoots))
	for _, item := range discoveredRoots {
		scopes, rootRef, parent := scopesForRepository(item.root, rootInfos)
		if len(scopes) == 0 {
			continue
		}
		id := sourcecontrol.RepositoryID(workspaceID, ID, item.root)
		state := existing[id]
		if state == nil {
			state = &repositoryState{workspaceID: workspaceID, root: item.root}
			state.revision.Store(1)
		}
		state.repository = item.repository
		state.label = filepath.Base(item.root)
		state.parent = parent
		state.rootRef = rootRef
		state.scopes = scopes
		state.available = item.available
		state.diagnostic = item.diagnostic
		next[id] = state
		states = append(states, state)
	}
	p.repos[workspaceID] = next
	p.mu.Unlock()
	metadataRefs := make([]workspacefs.FileRef, 0, len(states)*2)
	for _, state := range states {
		for _, marker := range []string{".fslckout", "_FOSSIL_", ".fos"} {
			if _, err := os.Lstat(filepath.Join(state.root, marker)); err == nil {
				if ref, ok := state.refForPath(marker); ok && ref != nil {
					metadataRefs = append(metadataRefs, *ref)
				}
			}
		}
		if state.repository != "" {
			for _, root := range rootInfos {
				if relative, ok := relativeWithin(root.canonical, state.repository); ok && relative != "" {
					metadataRefs = append(metadataRefs, workspacefs.FileRef{RootID: root.ID, Path: relative})
					break
				}
			}
		}
	}
	p.fs.SetSourceControlMetadata(workspaceID, ID, metadataRefs)
	return states, nil
}

func (p *Provider) repository(ctx context.Context, workspaceID, repositoryID string) (*repositoryState, error) {
	p.mu.RLock()
	state := p.repos[workspaceID][repositoryID]
	p.mu.RUnlock()
	if state == nil {
		if _, err := p.discover(ctx, workspaceID); err != nil {
			return nil, err
		}
		p.mu.RLock()
		state = p.repos[workspaceID][repositoryID]
		p.mu.RUnlock()
	}
	if state == nil {
		return nil, &sourcecontrol.Error{Code: "repository_not_found", Message: "Fossil checkout not found", Cause: sourcecontrol.ErrNotFound}
	}
	if !state.available {
		return nil, &sourcecontrol.Error{Code: "fossil_unavailable", Message: state.diagnostic}
	}
	return state, nil
}

func (p *Provider) Subscribe(context.Context, string) error { return nil }
func (p *Provider) Unsubscribe(string)                      {}
func (p *Provider) Close()                                  {}

func (p *Provider) InvalidateWorkspace(workspaceID string) {
	p.mu.RLock()
	states := make([]*repositoryState, 0, len(p.repos[workspaceID]))
	for _, state := range p.repos[workspaceID] {
		states = append(states, state)
	}
	p.mu.RUnlock()
	for _, state := range states {
		state.revision.Add(1)
	}
}

func (p *Provider) ResetWorkspace(ctx context.Context, workspaceID string) error {
	p.RemoveWorkspace(workspaceID)
	_, err := p.discover(ctx, workspaceID)
	return err
}

func (p *Provider) RemoveWorkspace(workspaceID string) {
	p.mu.Lock()
	delete(p.repos, workspaceID)
	p.mu.Unlock()
	p.fs.RemoveSourceControlMetadata(workspaceID, ID)
}

func (p *Provider) StopWorkspaceProcesses(string) {}

func hasCheckoutMarker(directory string) bool {
	for _, name := range []string{".fslckout", "_FOSSIL_", ".fos"} {
		info, err := os.Lstat(filepath.Join(directory, name))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func nearestCheckoutRoot(directory string) string {
	current := filepath.Clean(directory)
	for {
		if hasCheckoutMarker(current) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
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
				scopes = append(scopes, repositoryScope{Scope: sourcecontrol.Scope{RootID: root.ID, RootLabel: root.Label, RepoPrefix: prefix}, rootPath: root.canonical})
			}
			if prefix == "" {
				parent = false
				if len(root.canonical) > rootRefSpecificity {
					rootRefSpecificity = len(root.canonical)
					rootRef = &workspacefs.FileRef{RootID: root.ID}
				}
			}
			continue
		}
		if prefix, ok := relativeWithin(root.canonical, repositoryRoot); ok {
			parent = false
			key := root.ID + "\x00"
			if !seen[key] {
				seen[key] = true
				scopes = append(scopes, repositoryScope{Scope: sourcecontrol.Scope{RootID: root.ID, RootLabel: root.Label}, rootPath: root.canonical, rootRefPrefix: prefix})
			}
			if len(root.canonical) > rootRefSpecificity {
				rootRefSpecificity = len(root.canonical)
				rootRef = &workspacefs.FileRef{RootID: root.ID, Path: filepath.ToSlash(prefix)}
			}
		}
	}
	sort.SliceStable(scopes, func(i, j int) bool { return len(scopes[i].rootPath) > len(scopes[j].rootPath) })
	return scopes, rootRef, parent
}

func (r *repositoryState) pathAllowed(value string) bool {
	clean, err := cleanPath(value)
	if err != nil || r.isProtectedPath(clean) {
		return false
	}
	for _, scope := range r.scopes {
		prefix := strings.Trim(filepath.ToSlash(scope.RepoPrefix), "/")
		if prefix == "" || clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

func (r *repositoryState) isProtectedPath(value string) bool {
	if protectedPath(value) {
		return true
	}
	if r.repository == "" {
		return false
	}
	relative, ok := relativeWithin(r.root, r.repository)
	if !ok || relative == "" {
		return false
	}
	clean, err := cleanPath(value)
	return err == nil && pathIdentity(clean) == pathIdentity(relative)
}

func (r *repositoryState) validatePaths(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, &sourcecontrol.Error{Code: "paths_required", Message: "at least one file is required"}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		clean, err := cleanPath(value)
		if err != nil || !r.pathAllowed(clean) {
			return nil, &sourcecontrol.Error{Code: "path_outside_workspace", Message: "source control path is outside this workspace", Cause: sourcecontrol.ErrInvalidPath}
		}
		key := pathIdentity(clean)
		if !seen[key] {
			seen[key] = true
			result = append(result, clean)
		}
	}
	return result, nil
}

func (r *repositoryState) refForPath(value string) (*workspacefs.FileRef, bool) {
	clean, err := cleanPath(value)
	if err != nil {
		return nil, false
	}
	absolute := filepath.Join(r.root, filepath.FromSlash(clean))
	for _, scope := range r.scopes {
		relative, err := filepath.Rel(scope.rootPath, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		return &workspacefs.FileRef{RootID: scope.RootID, Path: filepath.ToSlash(relative)}, true
	}
	return nil, false
}

func relativeWithin(base, candidate string) (string, bool) {
	relative, err := filepath.Rel(base, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	if relative == "." {
		return "", true
	}
	return filepath.ToSlash(relative), true
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

func pathIdentity(value string) string {
	value = filepath.Clean(value)
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func cleanPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) {
		return "", sourcecontrol.ErrInvalidPath
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", sourcecontrol.ErrInvalidPath
	}
	return clean, nil
}

func protectedPath(value string) bool {
	clean := strings.ToLower(strings.Trim(filepath.ToSlash(value), "/"))
	for _, part := range strings.Split(clean, "/") {
		if part == ".git" || part == ".fslckout" || part == "_fossil_" || part == ".fos" || part == ".echo" {
			return true
		}
	}
	return false
}

func providerDiagnostic(err error) string {
	var sourceError *sourcecontrol.Error
	if errors.As(err, &sourceError) {
		return sourceError.Message
	}
	if err == nil {
		return "Fossil is unavailable"
	}
	return err.Error()
}
