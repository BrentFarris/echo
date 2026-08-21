package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	echotools "github.com/brent/echo/internal/tools"
)

type memorySecretStore struct{ values map[string]string }

func (s *memorySecretStore) Available(context.Context) bool { return true }
func (s *memorySecretStore) Get(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}
func (s *memorySecretStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}
func (s *memorySecretStore) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func newTestManager(t *testing.T, safeMode bool) (*Manager, string, string) {
	t.Helper()
	base := localTestDir(t)
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{
		RootDir: filepath.Join(base, "echo-data", "plugins"), SafeMode: safeMode,
		WorkspacePath: func(id string) (string, error) {
			if id != "workspace-1" {
				return "", os.ErrNotExist
			}
			return workspace, nil
		},
		WorkspaceIDs: func() []string { return []string{"workspace-1"} },
		Secrets:      &memorySecretStore{values: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager, base, workspace
}

func registryHasTool(registry *echotools.Registry, name string) bool {
	for _, tool := range registry.Registered() {
		if tool.Metadata().Name == name {
			return true
		}
	}
	return false
}

func TestStageApproveActivationAndUISession(t *testing.T) {
	manager, base, _ := newTestManager(t, false)
	source := filepath.Join(base, "source")
	manifest := testUIManifest("lifecycle-test")
	writeTestPlugin(t, source, manifest)
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if installed.Effective {
		t.Fatal("disabled install became effective")
	}
	if err := manager.Action(context.Background(), manifest.ID, ActionRequest{Action: "enable-workspace", WorkspaceID: "workspace-1"}); err != nil {
		t.Fatal(err)
	}
	if !manager.IsEnabled(manifest.ID, "workspace-1") {
		t.Fatal("workspace activation was not effective")
	}
	session, err := manager.CreateUISession(manifest.ID, "main", "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := manager.UIAsset(session.Token, "ui/main/index.html")
	if err != nil || !strings.Contains(string(asset.Data), "Plugin") {
		t.Fatalf("asset failure: %v", err)
	}
	if _, err := manager.InvokeUIBridge(context.Background(), session.Token, UIBridgeRequest{Nonce: "forged", Method: "storage.get", Params: map[string]any{"scope": "workspace", "key": "value"}}); err == nil {
		t.Fatal("forged nonce accepted")
	}
	if _, err := manager.InvokeUIBridge(context.Background(), session.Token, UIBridgeRequest{Nonce: session.Nonce, Method: "storage.set", Params: map[string]any{"scope": "workspace", "key": "value", "value": "saved"}}); err != nil {
		t.Fatal(err)
	}
	result, err := manager.InvokeUIBridge(context.Background(), session.Token, UIBridgeRequest{Nonce: session.Nonce, Method: "storage.get", Params: map[string]any{"scope": "workspace", "key": "value"}})
	if err != nil || result.(map[string]any)["value"] != "saved" {
		t.Fatalf("storage result: %#v, %v", result, err)
	}
	if err := manager.Action(context.Background(), manifest.ID, ActionRequest{Action: "disable-workspace", WorkspaceID: "workspace-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UIAsset(session.Token, "ui/main/index.html"); err == nil {
		t.Fatal("disabled plugin retained a UI session")
	}
	dataPath := filepath.Join(manager.root, "data", manifest.ID)
	if _, err := os.Stat(dataPath); err != nil {
		t.Fatal("plugin data was not written")
	}
	if err := manager.Uninstall(context.Background(), manifest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataPath); err != nil {
		t.Fatal("uninstall unexpectedly removed plugin data")
	}
	catalog, err := manager.Catalog("workspace-1")
	if err != nil || len(catalog.Retained) != 1 || catalog.Retained[0].ID != manifest.ID {
		t.Fatalf("uninstall did not retain configuration metadata: %#v, %v", catalog, err)
	}
	if err := manager.RemoveData(context.Background(), manifest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Fatal("remove data left namespaced plugin data")
	}
	catalog, _ = manager.Catalog("workspace-1")
	if len(catalog.Retained) != 0 {
		t.Fatal("remove data left retained metadata")
	}
}

func TestWorkspaceApprovalRequiresRecipeSupport(t *testing.T) {
	base := localTestDir(t)
	manager, err := NewManager(Options{RootDir: filepath.Join(base, "plugins"), Builtins: BuiltinPackages()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	stage, err := manager.StageBuiltin(context.Background(), "calculator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "workspace", WorkspaceID: "workspace-1", Enable: true}); err == nil || !strings.Contains(err.Error(), "recipes are unavailable") {
		t.Fatalf("expected unavailable workspace recipe error, got %v", err)
	}
}

func TestUISessionRejectsSnapshotChangedAfterApproval(t *testing.T) {
	manager, base, _ := newTestManager(t, false)
	source := filepath.Join(base, "source")
	manifest := testUIManifest("ui-integrity")
	writeTestPlugin(t, source, manifest)
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "global", Enable: true}); err != nil {
		t.Fatal(err)
	}
	installed, _, err := manager.Installed(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(installed.PackagePath, filepath.FromSlash(manifest.Contributes.Views[0].Entry))
	file, err := os.OpenFile(entry, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("<!-- tampered -->"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateUISession(manifest.ID, "main", "workspace-1"); err == nil || !strings.Contains(err.Error(), "approved digest") {
		t.Fatalf("expected changed UI snapshot rejection, got %v", err)
	}
	if manager.IsEnabled(manifest.ID, "workspace-1") {
		t.Fatal("integrity failure did not quarantine the plugin")
	}
}

func TestSafeModePreventsEffectiveActivation(t *testing.T) {
	manager, base, _ := newTestManager(t, true)
	source := filepath.Join(base, "source")
	writeTestPlugin(t, source, testUIManifest("safe-test"))
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "global", Enable: true}); err != nil {
		t.Fatal(err)
	}
	catalog, err := manager.Catalog("workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Plugins) != 1 || catalog.Plugins[0].Effective || !catalog.Plugins[0].GlobalEnabled {
		t.Fatalf("unexpected safe-mode catalog: %#v", catalog)
	}
}

func TestCorruptPluginRegistryFailsClosedWithoutTakingDownCoreTools(t *testing.T) {
	base := localTestDir(t)
	root := filepath.Join(base, "plugins")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registry.json"), []byte(`{"version":1,"installationId":"../unsafe","plugins":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatalf("optional plugin corruption prevented manager construction: %v", err)
	}
	registry := echotools.CloneDefaultRegistry()
	coreCount := len(registry.Registered())
	if err := manager.BindTools(registry); err == nil {
		t.Fatal("corrupt registry unexpectedly bound plugin tools")
	}
	if len(registry.Registered()) != coreCount {
		t.Fatal("corrupt plugin registry changed immutable core registrations")
	}
}

func TestLifecycleBlockCancelsCallsAndRejectsNewDispatch(t *testing.T) {
	manager, _, _ := newTestManager(t, false)
	callContext, finish, err := manager.beginPluginCall(context.Background(), "lifecycle-gate")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-callContext.Done()
		finish()
	}()
	release := manager.blockPlugin("lifecycle-gate")
	defer release()
	if callContext.Err() == nil {
		t.Fatal("lifecycle block did not cancel the active call")
	}
	if _, _, err := manager.beginPluginCall(context.Background(), "lifecycle-gate"); err == nil {
		t.Fatal("lifecycle block accepted a new call")
	}
}

func TestDisabledPluginDisposesOwnedToolRegistration(t *testing.T) {
	manager, base, _ := newTestManager(t, false)
	source := filepath.Join(base, "source")
	manifest := testUIManifest("owned-tools")
	target := runtime.GOOS + "-" + runtime.GOARCH
	manifest.Runtime = &Runtime{Protocol: RPCProtocol, Targets: map[string]RuntimeTarget{target: {Path: "backend/" + target + "/owned-tools"}}}
	manifest.Contributes.Tools = []ToolContribution{{
		Name: "owned_tools_lookup", Description: "Look up a value", Method: "tools.lookup", ReadOnly: true,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}}
	writeTestPlugin(t, source, manifest)
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "none"}); err != nil {
		t.Fatal(err)
	}
	registry := echotools.CloneDefaultRegistry()
	if err := manager.BindTools(registry); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.update(func(state *registryFile) error {
		installed := state.Plugins[manifest.ID]
		installed.GlobalEnabled = true
		state.Plugins[manifest.ID] = installed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileTools(); err != nil {
		t.Fatal(err)
	}
	if !registryHasTool(registry, "owned_tools_lookup") {
		t.Fatal("enabled plugin tool was not registered")
	}
	if err := manager.Action(context.Background(), manifest.ID, ActionRequest{Action: "disable-global"}); err != nil {
		t.Fatal(err)
	}
	if registryHasTool(registry, "owned_tools_lookup") {
		t.Fatal("disabled plugin retained its owned tool registration")
	}
}

func TestWorkspacePinConflictOverridesGlobalEnablement(t *testing.T) {
	manager, base, workspace := newTestManager(t, false)
	source := filepath.Join(base, "source")
	manifest := testUIManifest("pin-conflict")
	writeTestPlugin(t, source, manifest)
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "global", Enable: true}); err != nil {
		t.Fatal(err)
	}
	installedCommit := strings.Repeat("a", 40)
	if err := manager.store.update(func(state *registryFile) error {
		installed := state.Plugins[manifest.ID]
		installed.Source = Source{Type: "github", Repository: "Owner/Repository", Commit: installedCommit}
		state.Plugins[manifest.ID] = installed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recipe := WorkspaceRecipe{Version: 1, Plugins: []PluginRecipe{{
		ID: manifest.ID, Enabled: true,
		Source: Source{Type: "github", Repository: "owner/repository", Commit: strings.Repeat("b", 40)},
	}}}
	if err := saveWorkspaceRecipe(workspace, recipe); err != nil {
		t.Fatal(err)
	}
	if manager.IsEnabled(manifest.ID, "workspace-1") {
		t.Fatal("a conflicting workspace pin inherited global activation")
	}
	catalog, err := manager.Catalog("workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Conflicts) != 1 || catalog.Plugins[0].Effective {
		t.Fatalf("workspace pin conflict was not reported and failed closed: %#v", catalog)
	}
}

func TestSecretValuesNeverEnterJSONState(t *testing.T) {
	manager, base, _ := newTestManager(t, false)
	source := filepath.Join(base, "source")
	manifest := testUIManifest("secret-test")
	manifest.Contributes.Settings = []SettingContribution{{Key: "api-token", Type: "secret", Scope: "global", Label: "API token", Required: true}}
	writeTestPlugin(t, source, manifest)
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "none"}); err != nil {
		t.Fatal(err)
	}
	const secret = "never-write-this-secret"
	if err := manager.UpdateConfig(context.Background(), manifest.ID, ConfigUpdate{Secrets: map[string]SecretUpdate{"api-token": {Source: "os", Value: secret}}}); err != nil {
		t.Fatal(err)
	}
	registry, err := os.ReadFile(filepath.Join(manager.root, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(registry), secret) {
		t.Fatal("secret leaked into registry JSON")
	}
	installed, ok, err := manager.Installed(manifest.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	config, err := manager.ResolvedConfig(context.Background(), installed, "")
	if err != nil {
		t.Fatal(err)
	}
	if config["api-token"] != secret {
		t.Fatal("credential-store secret did not resolve")
	}
	redacted := manager.redactValue(manifest.ID, map[string]any{"nested": []any{"prefix " + secret + " suffix"}}).(map[string]any)
	if strings.Contains(redacted["nested"].([]any)[0].(string), secret) {
		t.Fatal("secret was not redacted from plugin output")
	}
	catalog, err := manager.Catalog("")
	if err != nil {
		t.Fatal(err)
	}
	setting := catalog.Plugins[0].Settings[0]
	if !setting.Configured || setting.Value != nil {
		t.Fatalf("secret catalog was not redacted: %#v", setting)
	}
}

func TestRequiredNonSecretSettingBlocksInvocationConfiguration(t *testing.T) {
	manager, base, _ := newTestManager(t, false)
	source := filepath.Join(base, "source")
	manifest := testUIManifest("required-setting")
	manifest.Contributes.Settings = []SettingContribution{{Key: "service-url", Type: "url", Scope: "global", Label: "Service URL", Required: true}}
	writeTestPlugin(t, source, manifest)
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "none"}); err != nil {
		t.Fatal(err)
	}
	installed, _, _ := manager.Installed(manifest.ID)
	if _, err := manager.ResolvedConfig(context.Background(), installed, ""); err == nil || !strings.Contains(err.Error(), "required plugin setting") {
		t.Fatalf("expected missing required setting rejection, got %v", err)
	}
	if err := manager.UpdateConfig(context.Background(), manifest.ID, ConfigUpdate{Values: map[string]any{"service-url": "https://example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	installed, _, _ = manager.Installed(manifest.ID)
	config, err := manager.ResolvedConfig(context.Background(), installed, "")
	if err != nil || config["service-url"] != "https://example.invalid" {
		t.Fatalf("required setting did not resolve after configuration: %#v, %v", config, err)
	}
}

func TestWorkspaceRecipeCannotSelectAnEnvironmentSecretWithoutMachineApproval(t *testing.T) {
	manager, base, workspace := newTestManager(t, false)
	source := filepath.Join(base, "source")
	manifest := testUIManifest("recipe-secret-test")
	manifest.Contributes.Settings = []SettingContribution{
		{Key: "display-name", Type: "string", Scope: "workspace", Label: "Display name", Default: json.RawMessage(`"Default"`)},
		{Key: "api-token", Type: "secret", Scope: "workspace", Label: "API token"},
	}
	writeTestPlugin(t, source, manifest)
	stage, err := manager.StageLocal(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Approve(context.Background(), stage.ID, ApprovalRequest{Scope: "none"}); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	if err := manager.store.update(func(state *registryFile) error {
		installed := state.Plugins[manifest.ID]
		installed.Source = Source{Type: "github", Repository: "owner/repository", Commit: commit}
		state.Plugins[manifest.ID] = installed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECIPE_SHOULD_NOT_READ_THIS", "repository-selected-secret")
	recipe := WorkspaceRecipe{Version: 1, Plugins: []PluginRecipe{{
		ID: manifest.ID, Source: Source{Type: "github", Repository: "owner/repository", Commit: commit}, Enabled: true,
		Config:     map[string]any{"display-name": "From recipe"},
		SecretRefs: map[string]SecretReference{"api-token": {Source: "environment", Environment: "RECIPE_SHOULD_NOT_READ_THIS"}},
	}}}
	if err := saveWorkspaceRecipe(workspace, recipe); err != nil {
		t.Fatal(err)
	}
	installed, _, _ := manager.Installed(manifest.ID)
	config, err := manager.ResolvedConfig(context.Background(), installed, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if config["display-name"] != "From recipe" {
		t.Fatalf("valid non-secret workspace configuration was not applied: %#v", config)
	}
	if _, found := config["api-token"]; found {
		t.Fatal("repository-owned secret reference was resolved without a machine-local approval")
	}
	if err := manager.UpdateConfig(context.Background(), manifest.ID, ConfigUpdate{WorkspaceID: "workspace-1", Secrets: map[string]SecretUpdate{
		"api-token": {Source: "environment", Environment: "RECIPE_SHOULD_NOT_READ_THIS"},
	}}); err != nil {
		t.Fatal(err)
	}
	installed, _, _ = manager.Installed(manifest.ID)
	config, err = manager.ResolvedConfig(context.Background(), installed, "workspace-1")
	if err != nil || config["api-token"] != "repository-selected-secret" {
		t.Fatalf("explicit machine approval did not activate the environment reference: %#v, %v", config, err)
	}
}
