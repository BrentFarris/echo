package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) Icon(pluginID, viewID string) ([]byte, string, string, error) {
	installed, ok, err := m.Installed(pluginID)
	if err != nil || !ok {
		return nil, "", "", os.ErrNotExist
	}
	view, ok := installed.Manifest.View(viewID)
	if !ok || view.Icon == "" {
		return nil, "", "", os.ErrNotExist
	}
	path, err := packagePath(installed.PackagePath, view.Icon)
	if err != nil {
		return nil, "", "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, "", "", os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", "", err
	}
	contentType := "image/svg+xml"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		contentType = "image/png"
	case ".webp":
		contentType = "image/webp"
	}
	return data, contentType, installed.Digest, nil
}

func (m *Manager) Log(pluginID string) ([]byte, error) {
	if !pluginIDPattern.MatchString(pluginID) {
		return nil, fmt.Errorf("invalid plugin id")
	}
	path := filepath.Join(m.root, "logs", pluginID+".log")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		data = data[len(data)-(1<<20):]
	}
	return []byte(m.redactText(pluginID, string(data))), nil
}

func (m *Manager) RemoveData(ctx context.Context, pluginID string) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	installed, ok, err := m.Installed(pluginID)
	if err != nil {
		return err
	}
	retainedOnly := false
	if !ok {
		state, loadErr := m.store.load()
		if loadErr != nil {
			return loadErr
		}
		installed, ok = state.Retained[pluginID]
		retainedOnly = ok
	}
	if !ok {
		return fmt.Errorf("plugin %q has no retained data", pluginID)
	}
	releaseBlock := m.blockPlugin(pluginID)
	defer releaseBlock()
	_ = m.runtimes.Stop(pluginID)
	m.ClosePluginUISessions(pluginID)
	state, err := m.store.load()
	if err != nil {
		return err
	}
	scopes := map[string]bool{"global": true}
	for workspaceID := range installed.WorkspaceEnabled {
		scopes[workspaceID] = true
	}
	for workspaceID := range installed.WorkspaceConfig {
		scopes[workspaceID] = true
	}
	for workspaceID := range installed.WorkspaceSecretRefs {
		scopes[workspaceID] = true
	}
	if m.workspaceIDs != nil {
		for _, workspaceID := range m.workspaceIDs() {
			if validOpaqueID(workspaceID) {
				scopes[workspaceID] = true
			}
		}
	}
	credentialKeys := map[string]bool{}
	addCredentialKey := func(scope, settingKey string) {
		credentialKeys[fmt.Sprintf("Echo/%s/plugin/%s/%s/%s", state.InstallationID, pluginID, scope, settingKey)] = true
	}
	for _, setting := range installed.Manifest.Contributes.Settings {
		if !setting.Secret() {
			continue
		}
		for scope := range scopes {
			if setting.Scope == "global" && scope != "global" || setting.Scope == "workspace" && scope == "global" {
				continue
			}
			addCredentialKey(scope, setting.Key)
		}
	}
	// Preserve deletion coverage when an update removed a secret declaration
	// but its old reference is still retained for rollback/reinstallation.
	for settingKey := range installed.GlobalSecretRefs {
		addCredentialKey("global", settingKey)
	}
	for workspaceID, references := range installed.WorkspaceSecretRefs {
		for settingKey := range references {
			addCredentialKey(workspaceID, settingKey)
		}
	}
	for key := range credentialKeys {
		_ = m.secrets.Delete(ctx, key)
	}
	if err := m.store.update(func(registry *registryFile) error {
		if retainedOnly {
			delete(registry.Retained, pluginID)
		} else {
			current := registry.Plugins[pluginID]
			current.GlobalConfig = map[string]any{}
			current.GlobalSecretRefs = map[string]SecretReference{}
			current.WorkspaceConfig = map[string]map[string]any{}
			current.WorkspaceSecretRefs = map[string]map[string]SecretReference{}
			current.UpdatedAt = time.Now().UTC()
			registry.Plugins[pluginID] = current
		}
		return nil
	}); err != nil {
		return err
	}
	if m.workspacePath != nil {
		for workspaceID := range scopes {
			if workspaceID == "global" {
				continue
			}
			workspacePath, resolveErr := m.workspacePath(workspaceID)
			if resolveErr != nil {
				continue
			}
			recipe, recipeErr := loadWorkspaceRecipe(workspacePath)
			if recipeErr != nil {
				continue
			}
			changed := false
			for index := range recipe.Plugins {
				if recipe.Plugins[index].ID == pluginID {
					recipe.Plugins[index].Config = nil
					recipe.Plugins[index].SecretRefs = nil
					changed = true
				}
			}
			if changed {
				_ = saveWorkspaceRecipe(workspacePath, recipe)
			}
		}
	}
	dataPath := filepath.Join(m.root, "data", pluginID)
	if err := os.RemoveAll(dataPath); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.root, "logs", pluginID+".log"))
	_ = os.Remove(filepath.Join(m.root, "logs", pluginID+".log.1"))
	m.changed()
	return nil
}
