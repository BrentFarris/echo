package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	echotools "github.com/brent/echo/internal/tools"
)

type WorkspacePathResolver func(workspaceID string) (string, error)

type Options struct {
	RootDir       string
	CoreToolNames map[string]bool
	WorkspacePath WorkspacePathResolver
	WorkspaceIDs  func() []string
	SafeMode      bool
	HTTPClient    *http.Client
	Secrets       SecretStore
	Notify        func()
	RuntimeEvent  func(RuntimeEvent)
	Builtins      map[string]fs.FS
}

type Manager struct {
	mu            sync.RWMutex
	lifecycleMu   sync.Mutex
	root          string
	store         *registryStore
	coreToolNames map[string]bool
	workspacePath WorkspacePathResolver
	workspaceIDs  func() []string
	safeMode      bool
	httpClient    *http.Client
	secrets       SecretStore
	notify        func()
	builtins      map[string]fs.FS
	health        map[string]string
	blocked       map[string]int
	activeCalls   map[string]map[string]activePluginCall
	runtimes      *RuntimeManager
	toolMu        sync.Mutex
	toolRegistry  *echotools.Registry
	toolDisposers map[string][]func()
	uiMu          sync.Mutex
	uiSessions    map[string]UISession
	storageMu     sync.Mutex
	redactionMu   sync.RWMutex
	redactions    map[string][]string
}

type activePluginCall struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type ApprovalRequest struct {
	Scope       string `json:"scope"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	Enable      bool   `json:"enable"`
}

type ActionRequest struct {
	Action      string `json:"action"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type Catalog struct {
	SafeMode                 bool              `json:"safeMode"`
	CredentialStoreAvailable bool              `json:"credentialStoreAvailable"`
	Plugins                  []CatalogPlugin   `json:"plugins"`
	Stages                   []StageRecord     `json:"stages"`
	Missing                  []PluginRecipe    `json:"missing,omitempty"`
	Conflicts                []PluginRecipe    `json:"conflicts,omitempty"`
	Retained                 []CatalogRetained `json:"retained,omitempty"`
}

type CatalogRetained struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type CatalogPlugin struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Version          string             `json:"version"`
	Description      string             `json:"description,omitempty"`
	Digest           string             `json:"digest"`
	Revision         string             `json:"revision"`
	Source           Source             `json:"source"`
	GlobalEnabled    bool               `json:"globalEnabled"`
	WorkspaceEnabled bool               `json:"workspaceEnabled"`
	Effective        bool               `json:"effective"`
	Compatible       bool               `json:"compatible"`
	Health           string             `json:"health"`
	Permissions      []Permission       `json:"permissions,omitempty"`
	ApprovedTools    []string           `json:"approvedTools,omitempty"`
	Views            []CatalogView      `json:"views,omitempty"`
	Tools            []ToolContribution `json:"tools,omitempty"`
	Settings         []CatalogSetting   `json:"settings,omitempty"`
}

type CatalogView struct {
	PluginID    string     `json:"pluginId"`
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Icon        string     `json:"icon,omitempty"`
	DefaultSize Dimensions `json:"defaultSize,omitempty"`
	MinimumSize Dimensions `json:"minimumSize,omitempty"`
}

type CatalogSetting struct {
	SettingContribution
	Value        any    `json:"value,omitempty"`
	Configured   bool   `json:"configured,omitempty"`
	SecretSource string `json:"secretSource,omitempty"`
}

func NewManager(options Options) (*Manager, error) {
	root := strings.TrimSpace(options.RootDir)
	if root == "" {
		return nil, fmt.Errorf("plugin root directory is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin root: %w", err)
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	secretStore := options.Secrets
	if secretStore == nil {
		secretStore = NewDefaultSecretStore()
	}
	manager := &Manager{
		root: absolute, store: newRegistryStore(filepath.Join(absolute, "registry.json")),
		coreToolNames: cloneBoolMap(options.CoreToolNames), workspacePath: options.WorkspacePath, workspaceIDs: options.WorkspaceIDs,
		safeMode: options.SafeMode, httpClient: client, secrets: secretStore,
		notify: options.Notify, builtins: options.Builtins, health: map[string]string{}, blocked: map[string]int{},
		activeCalls:   map[string]map[string]activePluginCall{},
		toolDisposers: map[string][]func(){}, uiSessions: map[string]UISession{}, redactions: map[string][]string{},
	}
	manager.runtimes = NewRuntimeManager(RuntimeOptions{
		RootDir: filepath.Join(absolute, "data"), LogDir: filepath.Join(absolute, "logs"),
		Redact: manager.redactText,
		Events: func(event RuntimeEvent) {
			if event.Type == "runtime_unhealthy" {
				manager.setHealth(event.PluginID, event.Error)
				_ = manager.reconcileTools()
				manager.ClosePluginUISessions(event.PluginID)
				manager.changed()
			}
			if options.RuntimeEvent != nil {
				options.RuntimeEvent(event)
			}
		},
	})
	if err := manager.initialize(); err != nil {
		// A corrupt optional plugin registry must not keep Echo core from
		// starting. Keep the manager available in a degraded state so Settings
		// and safe mode can report and repair it.
		manager.health["__registry__"] = err.Error()
	}
	return manager, nil
}

func (m *Manager) initialize() error {
	if err := os.MkdirAll(filepath.Join(m.root, "staging"), 0o755); err != nil {
		return err
	}
	state, err := m.store.load()
	if err != nil {
		return err
	}
	for id, installed := range state.Plugins {
		validation, err := ValidatePackage(installed.PackagePath, m.coreToolNames)
		if !m.ownsInstalledPath(id, installed) {
			err = fmt.Errorf("installed package path is outside Echo's immutable package store")
		}
		if err != nil || validation.Manifest.ID != id || validation.Digest != installed.Digest {
			message := "installed package failed validation"
			if err != nil {
				message = err.Error()
			}
			m.health[id] = message
		}
	}
	return nil
}

func (m *Manager) RootDir() string { return m.root }

func (m *Manager) ValidateLocal(path string) (Validation, error) {
	return ValidatePackage(path, m.coreToolNames)
}

func (m *Manager) StageLocal(ctx context.Context, path string) (StageRecord, error) {
	if err := ctx.Err(); err != nil {
		return StageRecord{}, err
	}
	path, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return StageRecord{}, fmt.Errorf("resolve local plugin path: %w", err)
	}
	source := Source{Type: "local", Path: path}
	return m.stage(ctx, source, func(destination string) error { return CopyPackage(path, destination) })
}

func (m *Manager) StageBuiltin(ctx context.Context, id string) (StageRecord, error) {
	pluginFS := m.builtins[id]
	if pluginFS == nil {
		return StageRecord{}, fmt.Errorf("built-in plugin %q was not found", id)
	}
	source := Source{Type: "builtin", Builtin: id}
	return m.stage(ctx, source, func(destination string) error {
		return copyFS(pluginFS, ".", destination)
	})
}

func (m *Manager) StageGitHub(ctx context.Context, source Source) (StageRecord, error) {
	source = normalizeSource(source)
	source.Type = "github"
	repository, err := normalizeGitHubRepository(source.Repository)
	if err != nil {
		return StageRecord{}, err
	}
	source.Repository = repository
	if source.Ref == "" {
		if source.Commit != "" {
			source.Ref = source.Commit
		} else {
			source.Ref = "HEAD"
		}
	}
	commit, err := m.resolveGitHubCommit(ctx, source.Repository, source.Ref)
	if err != nil {
		return StageRecord{}, err
	}
	source.Commit = commit
	return m.stage(ctx, source, func(destination string) error {
		archiveURL := fmt.Sprintf("https://api.github.com/repos/%s/tarball/%s", source.Repository, url.PathEscape(commit))
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "Echo-Plugin-Installer/1")
		response, err := m.httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("download GitHub plugin: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			return fmt.Errorf("download GitHub plugin: HTTP %d", response.StatusCode)
		}
		return ExtractGitHubTar(response.Body, destination, source.Subdirectory)
	})
}

func (m *Manager) resolveGitHubCommit(ctx context.Context, repository, ref string) (string, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/commits/%s", repository, url.PathEscape(ref))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Echo-Plugin-Installer/1")
	response, err := m.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("resolve GitHub plugin ref: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return "", fmt.Errorf("resolve GitHub plugin ref: HTTP %d", response.StatusCode)
	}
	var payload struct {
		SHA string `json:"sha"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(&payload); err != nil || !validGitCommit(payload.SHA) {
		return "", fmt.Errorf("resolve GitHub plugin ref: invalid commit response")
	}
	return payload.SHA, nil
}

func (m *Manager) stage(ctx context.Context, source Source, populate func(string) error) (record StageRecord, err error) {
	id := randomID("stage-")
	directory := filepath.Join(m.root, "staging", id)
	packageDir := filepath.Join(directory, "package")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return StageRecord{}, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	if err := populate(packageDir); err != nil {
		return StageRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return StageRecord{}, err
	}
	validation, err := ValidatePackage(packageDir, m.coreToolNames)
	if err != nil {
		return StageRecord{}, err
	}
	state, stateErr := m.store.load()
	if stateErr != nil {
		return StageRecord{}, stateErr
	}
	previous := ""
	var diff StageDiff
	if installed, ok := state.Plugins[validation.Manifest.ID]; ok {
		previous = installed.Digest
		diff = manifestDiff(installed, validation)
	}
	record = StageRecord{
		ID: id, CreatedAt: time.Now().UTC(), Source: source, Validation: validation,
		PreviousDigest: previous, Diff: diff, PackagePath: packageDir,
	}
	if err := writeStage(m.root, record); err != nil {
		return StageRecord{}, err
	}
	m.changed()
	return record, nil
}

func manifestDiff(previous InstalledPlugin, candidate Validation) StageDiff {
	diff := StageDiff{
		PreviousVersion: previous.Manifest.Version, CodeChanged: previous.Digest != candidate.Digest,
		PermissionsChanged:   !reflect.DeepEqual(previous.Manifest.Permissions, candidate.Manifest.Permissions),
		ToolContractsChanged: !reflect.DeepEqual(previous.Manifest.Contributes.Tools, candidate.Manifest.Contributes.Tools),
	}
	diff.PermissionsAdded, diff.PermissionsRemoved = stringSetDiff(permissionNames(candidate.Manifest.Permissions), permissionNames(previous.Manifest.Permissions))
	diff.ToolsAdded, diff.ToolsRemoved = stringSetDiff(toolNames(candidate.Manifest.Contributes.Tools), toolNames(previous.Manifest.Contributes.Tools))
	diff.ViewsAdded, diff.ViewsRemoved = stringSetDiff(viewNames(candidate.Manifest.Contributes.Views), viewNames(previous.Manifest.Contributes.Views))
	diff.SettingsAdded, diff.SettingsRemoved = stringSetDiff(settingNames(candidate.Manifest.Contributes.Settings), settingNames(previous.Manifest.Contributes.Settings))
	return diff
}

func permissionNames(values []Permission) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}
func toolNames(values []ToolContribution) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Name)
	}
	return result
}
func viewNames(values []ViewContribution) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.ID)
	}
	return result
}
func settingNames(values []SettingContribution) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Key)
	}
	return result
}

func stringSetDiff(next, previous []string) ([]string, []string) {
	nextSet := map[string]bool{}
	previousSet := map[string]bool{}
	for _, value := range next {
		nextSet[value] = true
	}
	for _, value := range previous {
		previousSet[value] = true
	}
	added := []string{}
	removed := []string{}
	for value := range nextSet {
		if !previousSet[value] {
			added = append(added, value)
		}
	}
	for value := range previousSet {
		if !nextSet[value] {
			removed = append(removed, value)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func (m *Manager) Approve(ctx context.Context, stageID string, approval ApprovalRequest) (CatalogPlugin, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if approval.Scope != "global" && approval.Scope != "workspace" && approval.Scope != "none" {
		return CatalogPlugin{}, fmt.Errorf("approval scope must be global, workspace, or none")
	}
	if approval.Scope == "workspace" && strings.TrimSpace(approval.WorkspaceID) == "" {
		return CatalogPlugin{}, fmt.Errorf("workspaceId is required for workspace approval")
	}
	record, err := readStage(m.root, stageID)
	if err != nil {
		return CatalogPlugin{}, err
	}
	validation, err := ValidatePackage(record.PackagePath, m.coreToolNames)
	if err != nil {
		return CatalogPlugin{}, fmt.Errorf("revalidate plugin stage: %w", err)
	}
	if validation.Digest != record.Validation.Digest || validation.Manifest.ID != record.Validation.Manifest.ID {
		return CatalogPlugin{}, fmt.Errorf("plugin stage changed after validation")
	}
	if !validation.Compatible {
		return CatalogPlugin{}, fmt.Errorf("plugin does not include a backend for %s", validation.Target)
	}
	before, err := m.store.load()
	if err != nil {
		return CatalogPlugin{}, err
	}
	previous, previousExists := before.Plugins[validation.Manifest.ID]
	if previousExists && record.PreviousDigest != previous.Digest || !previousExists && record.PreviousDigest != "" {
		return CatalogPlugin{}, fmt.Errorf("installed plugin changed after this candidate was staged; stage it again")
	}
	releaseBlock := m.blockPlugin(validation.Manifest.ID)
	defer releaseBlock()
	previousHealth := m.healthFor(validation.Manifest.ID)
	var previousRecipe *WorkspaceRecipe
	var previousRecipePath string
	if approval.Enable && approval.Scope == "workspace" && (record.Source.Type == "github" || record.Source.Type == "builtin") {
		if m.workspacePath == nil {
			return CatalogPlugin{}, fmt.Errorf("workspace plugin recipes are unavailable")
		}
		workspacePath, resolveErr := m.workspacePath(approval.WorkspaceID)
		if resolveErr != nil {
			return CatalogPlugin{}, resolveErr
		}
		recipe, recipeErr := loadWorkspaceRecipe(workspacePath)
		if recipeErr != nil {
			return CatalogPlugin{}, recipeErr
		}
		previousRecipe = &recipe
		previousRecipePath = workspacePath
	}
	packageDir := filepath.Join(m.root, "packages", validation.Manifest.ID, validation.Digest)
	if err := InstallPackageSnapshot(record.PackagePath, packageDir, validation.Digest, m.coreToolNames); err != nil {
		return CatalogPlugin{}, fmt.Errorf("install plugin snapshot: %w", err)
	}
	now := time.Now().UTC()
	permissions := make([]string, 0, len(validation.Manifest.Permissions))
	for _, permission := range validation.Manifest.Permissions {
		permissions = append(permissions, permission.Name)
	}
	approvedTools := make([]string, 0, len(validation.Manifest.Contributes.Tools))
	for _, tool := range validation.Manifest.Contributes.Tools {
		approvedTools = append(approvedTools, tool.Name)
	}
	var installed InstalledPlugin
	if err := m.store.update(func(state *registryFile) error {
		previous := state.Plugins[validation.Manifest.ID]
		preserved := previous
		if preserved.Manifest.ID == "" {
			preserved = state.Retained[validation.Manifest.ID]
			preserved.GlobalEnabled = false
			preserved.WorkspaceEnabled = map[string]bool{}
		}
		installed = InstalledPlugin{
			Manifest: validation.Manifest, Digest: validation.Digest, PackagePath: packageDir,
			Source: record.Source, ApprovedPermissions: permissions, ApprovedTools: approvedTools,
			GlobalEnabled: preserved.GlobalEnabled, WorkspaceEnabled: preserved.WorkspaceEnabled,
			GlobalConfig: preserved.GlobalConfig, GlobalSecretRefs: preserved.GlobalSecretRefs, WorkspaceConfig: preserved.WorkspaceConfig,
			WorkspaceSecretRefs: preserved.WorkspaceSecretRefs,
			InstalledAt:         preserved.InstalledAt, UpdatedAt: now,
		}
		if installed.InstalledAt.IsZero() {
			installed.InstalledAt = now
		}
		if installed.WorkspaceEnabled == nil {
			installed.WorkspaceEnabled = map[string]bool{}
		}
		if installed.GlobalConfig == nil {
			installed.GlobalConfig = map[string]any{}
		}
		if installed.GlobalSecretRefs == nil {
			installed.GlobalSecretRefs = map[string]SecretReference{}
		}
		if installed.WorkspaceConfig == nil {
			installed.WorkspaceConfig = map[string]map[string]any{}
		}
		if installed.WorkspaceSecretRefs == nil {
			installed.WorkspaceSecretRefs = map[string]map[string]SecretReference{}
		}
		if approval.Enable {
			switch approval.Scope {
			case "global":
				installed.GlobalEnabled = true
			case "workspace":
				if record.Source.Type == "github" || record.Source.Type == "builtin" {
					if err := m.setWorkspaceRecipeLocked(approval.WorkspaceID, installed, true, nil); err != nil {
						return err
					}
				} else {
					installed.WorkspaceEnabled[approval.WorkspaceID] = true
				}
			}
		}
		state.Plugins[validation.Manifest.ID] = installed
		return nil
	}); err != nil {
		if previousRecipe != nil && previousRecipePath != "" {
			_ = saveWorkspaceRecipe(previousRecipePath, *previousRecipe)
		}
		return CatalogPlugin{}, err
	}
	m.ClosePluginUISessions(validation.Manifest.ID)
	_ = m.runtimes.Stop(validation.Manifest.ID)
	m.mu.Lock()
	delete(m.health, validation.Manifest.ID)
	m.mu.Unlock()
	shouldActivateRuntime := !m.safeMode && validation.Manifest.Runtime != nil &&
		(installed.GlobalEnabled || m.anyWorkspaceEnabled(installed) || approval.Enable && approval.Scope == "workspace")
	if shouldActivateRuntime {
		if _, err := m.runtimes.Ensure(ctx, installed); err != nil {
			m.rollbackApproval(validation.Manifest.ID, previous, previousExists, previousRecipePath, previousRecipe, previousHealth)
			return CatalogPlugin{}, fmt.Errorf("activate plugin runtime: %w", err)
		}
	}
	if err := m.reconcileTools(); err != nil {
		m.rollbackApproval(validation.Manifest.ID, previous, previousExists, previousRecipePath, previousRecipe, previousHealth)
		return CatalogPlugin{}, err
	}
	_ = m.store.update(func(state *registryFile) error {
		delete(state.Retained, validation.Manifest.ID)
		return nil
	})
	_ = os.RemoveAll(filepath.Join(m.root, "staging", stageID))
	m.changed()
	return m.catalogPlugin(installed, approval.WorkspaceID), nil
}

func (m *Manager) rollbackApproval(pluginID string, previous InstalledPlugin, previousExists bool, workspacePath string, recipe *WorkspaceRecipe, previousHealth string) {
	_ = m.runtimes.Stop(pluginID)
	_ = m.store.update(func(state *registryFile) error {
		if previousExists {
			state.Plugins[pluginID] = previous
		} else {
			delete(state.Plugins, pluginID)
		}
		return nil
	})
	if recipe != nil && workspacePath != "" {
		_ = saveWorkspaceRecipe(workspacePath, *recipe)
	}
	m.setHealth(pluginID, previousHealth)
	_ = m.reconcileTools()
	previousEnabled := previous.GlobalEnabled
	for _, enabled := range previous.WorkspaceEnabled {
		previousEnabled = previousEnabled || enabled
	}
	if recipe != nil {
		for _, entry := range recipe.Plugins {
			if entry.ID == pluginID {
				previousEnabled = previousEnabled || entry.Enabled
			}
		}
	}
	if previousExists && previous.Manifest.Runtime != nil && !m.safeMode && previousEnabled {
		_, _ = m.runtimes.Ensure(context.Background(), previous)
	}
	m.changed()
}

func (m *Manager) RejectStage(stageID string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if _, err := readStage(m.root, stageID); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(m.root, "staging", stageID)); err != nil {
		return err
	}
	m.changed()
	return nil
}

func (m *Manager) Catalog(workspaceID string) (Catalog, error) {
	state, err := m.store.load()
	if err != nil {
		return Catalog{}, err
	}
	result := Catalog{SafeMode: m.safeMode, CredentialStoreAvailable: m.secrets.Available(context.Background()), Plugins: []CatalogPlugin{}, Stages: []StageRecord{}}
	ids := make([]string, 0, len(state.Plugins))
	for id := range state.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		result.Plugins = append(result.Plugins, m.catalogPlugin(state.Plugins[id], workspaceID))
	}
	retainedIDs := make([]string, 0, len(state.Retained))
	for id := range state.Retained {
		retainedIDs = append(retainedIDs, id)
	}
	sort.Strings(retainedIDs)
	for _, id := range retainedIDs {
		retained := state.Retained[id]
		result.Retained = append(result.Retained, CatalogRetained{ID: id, Name: retained.Manifest.Name, Version: retained.Manifest.Version})
	}
	stages, err := listStages(m.root)
	if err != nil {
		return Catalog{}, err
	}
	result.Stages = stages
	if workspaceID != "" && m.workspacePath != nil {
		workspacePath, resolveErr := m.workspacePath(workspaceID)
		if resolveErr == nil {
			recipe, recipeErr := loadWorkspaceRecipe(workspacePath)
			if recipeErr == nil {
				for _, required := range recipe.Plugins {
					installed, ok := state.Plugins[required.ID]
					if !ok {
						result.Missing = append(result.Missing, required)
					} else if !workspaceSourceMatches(installed.Source, required.Source) {
						result.Conflicts = append(result.Conflicts, required)
					}
				}
			}
		}
	}
	return result, nil
}

func (m *Manager) catalogPlugin(installed InstalledPlugin, workspaceID string) CatalogPlugin {
	workspaceEnabled := m.workspaceEnabled(installed, workspaceID)
	effective := !m.safeMode && !m.pluginBlocked(installed.Manifest.ID) && !m.workspaceConflict(installed, workspaceID) && (installed.GlobalEnabled || workspaceEnabled) && m.healthFor(installed.Manifest.ID) == ""
	target := runtime.GOOS + "-" + runtime.GOARCH
	compatible := installed.Manifest.Runtime == nil
	if installed.Manifest.Runtime != nil {
		_, compatible = installed.Manifest.Runtime.Targets[target]
	}
	approvedPermissions := make([]Permission, 0, len(installed.Manifest.Permissions))
	for _, permission := range installed.Manifest.Permissions {
		if containsString(installed.ApprovedPermissions, permission.Name) {
			approvedPermissions = append(approvedPermissions, permission)
		}
	}
	plugin := CatalogPlugin{
		ID: installed.Manifest.ID, Name: installed.Manifest.Name, Version: installed.Manifest.Version,
		Description: installed.Manifest.Description, Digest: installed.Digest, Revision: installed.UpdatedAt.UTC().Format(time.RFC3339Nano), Source: installed.Source,
		GlobalEnabled: installed.GlobalEnabled, WorkspaceEnabled: workspaceEnabled,
		Effective: effective && compatible, Compatible: compatible, Health: m.healthFor(installed.Manifest.ID),
		Permissions: approvedPermissions, ApprovedTools: append([]string(nil), installed.ApprovedTools...),
		Tools: append([]ToolContribution(nil), installed.Manifest.Contributes.Tools...),
	}
	for _, view := range installed.Manifest.Contributes.Views {
		catalogView := CatalogView{
			PluginID: installed.Manifest.ID, ID: view.ID, Kind: view.Kind, Title: view.Title,
			DefaultSize: view.DefaultSize, MinimumSize: view.MinimumSize,
		}
		if view.Icon != "" {
			catalogView.Icon = fmt.Sprintf("/api/plugins/%s/icon/%s?digest=%s", installed.Manifest.ID, view.ID, installed.Digest)
		}
		plugin.Views = append(plugin.Views, catalogView)
	}
	plugin.Settings = m.catalogSettings(installed, workspaceID)
	return plugin
}

func (m *Manager) ownsInstalledPath(pluginID string, installed InstalledPlugin) bool {
	if !pluginIDPattern.MatchString(pluginID) || !validDigest(installed.Digest) {
		return false
	}
	expected, err := filepath.Abs(filepath.Join(m.root, "packages", pluginID, installed.Digest))
	if err != nil {
		return false
	}
	actual, err := filepath.Abs(installed.PackagePath)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(actual), filepath.Clean(expected))
	}
	return filepath.Clean(actual) == filepath.Clean(expected)
}

func (m *Manager) verifyInstalledSnapshot(installed InstalledPlugin) error {
	if !m.ownsInstalledPath(installed.Manifest.ID, installed) {
		return fmt.Errorf("installed package path is outside Echo's immutable package store")
	}
	digest, err := HashPackage(installed.PackagePath)
	if err != nil {
		return fmt.Errorf("verify installed plugin snapshot: %w", err)
	}
	if digest != installed.Digest {
		return fmt.Errorf("installed plugin snapshot no longer matches its approved digest")
	}
	return nil
}

func (m *Manager) IsEnabled(pluginID, workspaceID string) bool {
	if m.safeMode || m.pluginBlocked(pluginID) {
		return false
	}
	state, err := m.store.load()
	if err != nil {
		return false
	}
	installed, ok := state.Plugins[pluginID]
	return ok && m.healthFor(pluginID) == "" && !m.workspaceConflict(installed, workspaceID) && (installed.GlobalEnabled || m.workspaceEnabled(installed, workspaceID))
}

func (m *Manager) IsToolAllowed(pluginID, toolName, workspaceID string) bool {
	if !m.IsEnabled(pluginID, workspaceID) {
		return false
	}
	state, err := m.store.load()
	if err != nil {
		return false
	}
	installed, ok := state.Plugins[pluginID]
	return ok && containsString(installed.ApprovedTools, toolName)
}

func (m *Manager) Installed(pluginID string) (InstalledPlugin, bool, error) {
	state, err := m.store.load()
	if err != nil {
		return InstalledPlugin{}, false, err
	}
	installed, ok := state.Plugins[pluginID]
	return installed, ok, nil
}

func (m *Manager) Action(ctx context.Context, pluginID string, request ActionRequest) error {
	switch request.Action {
	case "enable-global":
		return m.setEnabled(ctx, pluginID, "global", "", true)
	case "disable-global":
		return m.setEnabled(ctx, pluginID, "global", "", false)
	case "enable-workspace":
		return m.setEnabled(ctx, pluginID, "workspace", request.WorkspaceID, true)
	case "disable-workspace":
		return m.setEnabled(ctx, pluginID, "workspace", request.WorkspaceID, false)
	case "reload":
		installed, ok, err := m.Installed(pluginID)
		if err != nil || !ok {
			return fmt.Errorf("plugin was not found")
		}
		if installed.Source.Type != "local" || installed.Source.Path == "" {
			return fmt.Errorf("only local development plugins can be reloaded directly")
		}
		_, err = m.StageLocal(ctx, installed.Source.Path)
		return err
	case "uninstall":
		return m.Uninstall(ctx, pluginID)
	default:
		return fmt.Errorf("unsupported plugin action %q", request.Action)
	}
}

func (m *Manager) StageReload(ctx context.Context, pluginID string) (StageRecord, error) {
	installed, ok, err := m.Installed(pluginID)
	if err != nil {
		return StageRecord{}, err
	}
	if !ok || installed.Source.Type != "local" || installed.Source.Path == "" {
		return StageRecord{}, fmt.Errorf("only installed local development plugins can be reloaded")
	}
	return m.StageLocal(ctx, installed.Source.Path)
}

func (m *Manager) StageUpdate(ctx context.Context, pluginID string) (StageRecord, error) {
	installed, ok, err := m.Installed(pluginID)
	if err != nil {
		return StageRecord{}, err
	}
	if !ok || installed.Source.Type != "github" {
		return StageRecord{}, fmt.Errorf("only GitHub plugins support update checks")
	}
	source := installed.Source
	source.Commit = ""
	return m.StageGitHub(ctx, source)
}

func (m *Manager) setEnabled(ctx context.Context, pluginID, scope, workspaceID string, enabled bool) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if scope == "workspace" && workspaceID == "" {
		return fmt.Errorf("workspaceId is required")
	}
	if enabled && m.healthFor(pluginID) != "" {
		return fmt.Errorf("plugin is unhealthy; stage and approve a reload before enabling it")
	}
	releaseBlock := m.blockPlugin(pluginID)
	defer releaseBlock()
	var previousGlobal bool
	var previousWorkspace bool
	var previousUpdatedAt time.Time
	var previousRecipe *WorkspaceRecipe
	var previousRecipePath string
	if scope == "workspace" {
		if current, ok, _ := m.Installed(pluginID); ok && (current.Source.Type == "github" || current.Source.Type == "builtin") && m.workspacePath != nil {
			if path, resolveErr := m.workspacePath(workspaceID); resolveErr == nil {
				if recipe, recipeErr := loadWorkspaceRecipe(path); recipeErr == nil {
					previousRecipe = &recipe
					previousRecipePath = path
				}
			}
		}
	}
	var installed InstalledPlugin
	if err := m.store.update(func(state *registryFile) error {
		var ok bool
		installed, ok = state.Plugins[pluginID]
		if !ok {
			return fmt.Errorf("plugin %q was not found", pluginID)
		}
		previousUpdatedAt = installed.UpdatedAt
		switch scope {
		case "global":
			previousGlobal = installed.GlobalEnabled
			installed.GlobalEnabled = enabled
		case "workspace":
			previousWorkspace = m.workspaceEnabled(installed, workspaceID)
			if installed.Source.Type == "github" || installed.Source.Type == "builtin" {
				if err := m.setWorkspaceRecipeLocked(workspaceID, installed, enabled, nil); err != nil {
					return err
				}
			} else {
				if installed.WorkspaceEnabled == nil {
					installed.WorkspaceEnabled = map[string]bool{}
				}
				installed.WorkspaceEnabled[workspaceID] = enabled
			}
		default:
			return fmt.Errorf("invalid plugin activation scope")
		}
		installed.UpdatedAt = time.Now().UTC()
		state.Plugins[pluginID] = installed
		return nil
	}); err != nil {
		if previousRecipe != nil && previousRecipePath != "" {
			_ = saveWorkspaceRecipe(previousRecipePath, *previousRecipe)
		}
		return err
	}
	if enabled && installed.Manifest.Runtime != nil {
		if _, err := m.runtimes.Ensure(ctx, installed); err != nil {
			_ = m.store.update(func(state *registryFile) error {
				current, ok := state.Plugins[pluginID]
				if !ok {
					return nil
				}
				if scope == "global" {
					current.GlobalEnabled = previousGlobal
				} else if current.Source.Type != "github" && current.Source.Type != "builtin" {
					current.WorkspaceEnabled[workspaceID] = previousWorkspace
				}
				current.UpdatedAt = previousUpdatedAt
				state.Plugins[pluginID] = current
				return nil
			})
			if previousRecipe != nil && previousRecipePath != "" {
				_ = saveWorkspaceRecipe(previousRecipePath, *previousRecipe)
			}
			m.setHealth(pluginID, err.Error())
			releaseBlock()
			m.CloseInactiveUISessions()
			m.changed()
			return fmt.Errorf("activate plugin runtime: %w", err)
		}
	}
	if !enabled && !installed.GlobalEnabled && !m.anyWorkspaceEnabled(installed) {
		_ = m.runtimes.Stop(pluginID)
	}
	if err := m.reconcileTools(); err != nil {
		return err
	}
	releaseBlock()
	m.CloseInactiveUISessions()
	m.changed()
	return nil
}

func (m *Manager) setWorkspaceRecipeLocked(workspaceID string, installed InstalledPlugin, enabled bool, config map[string]any) error {
	if m.workspacePath == nil {
		return fmt.Errorf("workspace plugin recipes are unavailable")
	}
	workspacePath, err := m.workspacePath(workspaceID)
	if err != nil {
		return err
	}
	recipe, err := loadWorkspaceRecipe(workspacePath)
	if err != nil {
		return err
	}
	found := false
	for index := range recipe.Plugins {
		if recipe.Plugins[index].ID == installed.Manifest.ID {
			recipe.Plugins[index].Source = portableSource(installed.Source)
			recipe.Plugins[index].Enabled = enabled
			if config != nil {
				recipe.Plugins[index].Config = config
			}
			found = true
			break
		}
	}
	if !found {
		recipe.Plugins = append(recipe.Plugins, PluginRecipe{
			ID: installed.Manifest.ID, Source: portableSource(installed.Source), Enabled: enabled, Config: config,
		})
	}
	sort.SliceStable(recipe.Plugins, func(i, j int) bool { return recipe.Plugins[i].ID < recipe.Plugins[j].ID })
	return saveWorkspaceRecipe(workspacePath, recipe)
}

func portableSource(source Source) Source {
	source.Path = ""
	source.Ref = ""
	return source
}

func (m *Manager) workspaceEnabled(installed InstalledPlugin, workspaceID string) bool {
	if workspaceID == "" {
		return false
	}
	if installed.WorkspaceEnabled[workspaceID] {
		return true
	}
	if installed.Source.Type != "github" && installed.Source.Type != "builtin" || m.workspacePath == nil {
		return false
	}
	workspacePath, err := m.workspacePath(workspaceID)
	if err != nil {
		return false
	}
	recipe, err := loadWorkspaceRecipe(workspacePath)
	if err != nil {
		return false
	}
	for _, entry := range recipe.Plugins {
		if entry.ID != installed.Manifest.ID || !entry.Enabled {
			continue
		}
		return workspaceSourceMatches(installed.Source, entry.Source)
	}
	return false
}

func workspaceSourceMatches(installed, required Source) bool {
	installed = normalizeSource(installed)
	required = normalizeSource(required)
	if installed.Type != required.Type {
		return false
	}
	switch required.Type {
	case "github":
		return validGitCommit(required.Commit) && strings.EqualFold(installed.Repository, required.Repository) &&
			installed.Commit == required.Commit && installed.Subdirectory == required.Subdirectory
	case "builtin":
		return installed.Builtin != "" && installed.Builtin == required.Builtin
	default:
		return false
	}
}

func (m *Manager) workspaceConflict(installed InstalledPlugin, workspaceID string) bool {
	if workspaceID == "" || m.workspacePath == nil {
		return false
	}
	workspacePath, err := m.workspacePath(workspaceID)
	if err != nil {
		return true
	}
	recipe, err := loadWorkspaceRecipe(workspacePath)
	if err != nil {
		return true
	}
	for _, entry := range recipe.Plugins {
		if entry.ID == installed.Manifest.ID {
			return !workspaceSourceMatches(installed.Source, entry.Source)
		}
	}
	return false
}

func (m *Manager) anyWorkspaceEnabled(installed InstalledPlugin) bool {
	for _, enabled := range installed.WorkspaceEnabled {
		if enabled {
			return true
		}
	}
	if m.workspaceIDs != nil {
		for _, workspaceID := range m.workspaceIDs() {
			if m.workspaceEnabled(installed, workspaceID) {
				return true
			}
		}
	}
	return false
}

func (m *Manager) enabledAnywhere(installed InstalledPlugin) bool {
	return !m.safeMode && m.healthFor(installed.Manifest.ID) == "" && (installed.GlobalEnabled || m.anyWorkspaceEnabled(installed))
}

func (m *Manager) Uninstall(ctx context.Context, pluginID string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	installed, ok, err := m.Installed(pluginID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("plugin %q was not found", pluginID)
	}
	releaseBlock := m.blockPlugin(pluginID)
	defer releaseBlock()
	_ = m.runtimes.Stop(pluginID)
	m.ClosePluginUISessions(pluginID)
	if err := m.store.update(func(state *registryFile) error {
		installed.GlobalEnabled = false
		installed.WorkspaceEnabled = map[string]bool{}
		installed.PackagePath = ""
		state.Retained[pluginID] = installed
		delete(state.Plugins, pluginID)
		return nil
	}); err != nil {
		return err
	}
	// Only immutable Echo-owned snapshots are removed. Local source folders
	// are never touched.
	_ = os.RemoveAll(filepath.Join(m.root, "packages", pluginID))
	if err := m.reconcileTools(); err != nil {
		return err
	}
	m.changed()
	_ = installed
	return nil
}

func (m *Manager) Shutdown(ctx context.Context) error { return m.runtimes.Shutdown(ctx) }

func (m *Manager) changed() {
	if m.notify != nil {
		m.notify()
	}
}

func (m *Manager) setHealth(pluginID, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if message == "" {
		delete(m.health, pluginID)
	} else {
		m.health[pluginID] = message
	}
}

func (m *Manager) healthFor(pluginID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.health[pluginID]
}

func (m *Manager) blockPlugin(pluginID string) func() {
	m.mu.Lock()
	m.blocked[pluginID]++
	calls := make([]activePluginCall, 0, len(m.activeCalls[pluginID]))
	for _, call := range m.activeCalls[pluginID] {
		calls = append(calls, call)
	}
	m.mu.Unlock()
	for _, call := range calls {
		call.cancel()
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	for _, call := range calls {
		select {
		case <-call.done:
		case <-waitCtx.Done():
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.blocked[pluginID]--
			if m.blocked[pluginID] <= 0 {
				delete(m.blocked, pluginID)
			}
			m.mu.Unlock()
		})
	}
}

func (m *Manager) beginPluginCall(ctx context.Context, pluginID string) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.blocked[pluginID] > 0 {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("plugin lifecycle change is in progress")
	}
	callCtx, cancel := context.WithCancel(ctx)
	id := randomID("call-")
	call := activePluginCall{cancel: cancel, done: make(chan struct{})}
	if m.activeCalls[pluginID] == nil {
		m.activeCalls[pluginID] = map[string]activePluginCall{}
	}
	m.activeCalls[pluginID][id] = call
	m.mu.Unlock()
	var once sync.Once
	finish := func() {
		once.Do(func() {
			cancel()
			m.mu.Lock()
			delete(m.activeCalls[pluginID], id)
			if len(m.activeCalls[pluginID]) == 0 {
				delete(m.activeCalls, pluginID)
			}
			close(call.done)
			m.mu.Unlock()
		})
	}
	return callCtx, finish, nil
}

func (m *Manager) pluginBlocked(pluginID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.blocked[pluginID] > 0
}

func normalizeGitHubRepository(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "https://github.com/") || strings.HasPrefix(value, "http://github.com/") {
		parsed, err := url.Parse(value)
		if err != nil {
			return "", fmt.Errorf("invalid GitHub repository")
		}
		value = strings.Trim(parsed.Path, "/")
	}
	value = strings.TrimSuffix(value, ".git")
	parts := strings.Split(value, "/")
	component := func(value string) bool {
		if len(value) < 1 || len(value) > 100 {
			return false
		}
		for index, character := range value {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || index > 0 && (character == '-' || character == '_' || character == '.') {
				continue
			}
			return false
		}
		return true
	}
	if len(parts) != 2 || !component(parts[0]) || !component(parts[1]) {
		return "", fmt.Errorf("GitHub repository must use owner/name")
	}
	return parts[0] + "/" + parts[1], nil
}

func copyFS(source fs.FS, sourceRoot, destination string) error {
	return fs.WalkDir(source, sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil || relative == "." {
			return err
		}
		target, err := packagePath(destination, filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
			return fmt.Errorf("built-in plugin contains an invalid file")
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
