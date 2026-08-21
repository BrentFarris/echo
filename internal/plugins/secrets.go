package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ErrSecretNotFound = errors.New("plugin secret was not found")

const maxSecretValueBytes = 16 << 10

type SecretStore interface {
	Available(context.Context) bool
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}

// layeredSecretStore adds session-only secrets without ever persisting them to
// Echo's JSON state. The platform store is the only durable layer.
type layeredSecretStore struct {
	platform SecretStore
	mu       sync.RWMutex
	session  map[string]string
}

func NewDefaultSecretStore() SecretStore {
	return &layeredSecretStore{platform: newPlatformSecretStore(), session: map[string]string{}}
}

func (s *layeredSecretStore) Available(ctx context.Context) bool {
	return s.platform != nil && s.platform.Available(ctx)
}

func (s *layeredSecretStore) Get(ctx context.Context, key string) (string, error) {
	s.mu.RLock()
	if value, ok := s.session[key]; ok {
		s.mu.RUnlock()
		return value, nil
	}
	s.mu.RUnlock()
	if s.platform == nil {
		return "", ErrSecretNotFound
	}
	return s.platform.Get(ctx, key)
}

func (s *layeredSecretStore) Set(ctx context.Context, key, value string) error {
	if s.platform == nil || !s.platform.Available(ctx) {
		return fmt.Errorf("OS credential store is unavailable")
	}
	s.mu.Lock()
	delete(s.session, key)
	s.mu.Unlock()
	return s.platform.Set(ctx, key, value)
}

func (s *layeredSecretStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	delete(s.session, key)
	s.mu.Unlock()
	if s.platform == nil || !s.platform.Available(ctx) {
		return nil
	}
	err := s.platform.Delete(ctx, key)
	if errors.Is(err, ErrSecretNotFound) {
		return nil
	}
	return err
}

func (s *layeredSecretStore) SetSession(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session[key] = value
}

func (s *layeredSecretStore) SessionConfigured(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.session[key]
	return ok
}

func (s *layeredSecretStore) GetSession(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.session[key]
	return value, ok
}

type SecretUpdate struct {
	Source      string `json:"source"`
	Value       string `json:"value,omitempty"`
	Environment string `json:"environment,omitempty"`
}

type ConfigUpdate struct {
	WorkspaceID string                  `json:"workspaceId,omitempty"`
	Values      map[string]any          `json:"values,omitempty"`
	Secrets     map[string]SecretUpdate `json:"secrets,omitempty"`
}

func (m *Manager) UpdateConfig(ctx context.Context, pluginID string, update ConfigUpdate) error {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	installed, ok, err := m.Installed(pluginID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("plugin %q was not found", pluginID)
	}
	previousInstalled := installed
	globalValues := cloneAnyMap(installed.GlobalConfig)
	workspaceValues := cloneAnyMap(installed.WorkspaceConfig[update.WorkspaceID])
	workspaceChanged := false
	portableWorkspace := update.WorkspaceID != "" && (installed.Source.Type == "github" || installed.Source.Type == "builtin")
	if portableWorkspace {
		if m.workspacePath == nil {
			return fmt.Errorf("workspace plugin recipes are unavailable")
		}
		workspacePath, resolveErr := m.workspacePath(update.WorkspaceID)
		if resolveErr != nil {
			return resolveErr
		}
		recipe, recipeErr := loadWorkspaceRecipe(workspacePath)
		if recipeErr != nil {
			return recipeErr
		}
		for _, entry := range recipe.Plugins {
			if entry.ID != pluginID {
				continue
			}
			for key, value := range entry.Config {
				setting, ok := settingByKey(installed.Manifest, key)
				if ok && !setting.Secret() && setting.Scope == "workspace" && validateSettingValue(setting, value) == nil {
					workspaceValues[key] = value
				}
			}
			break
		}
	}
	for key, value := range update.Values {
		setting, exists := settingByKey(installed.Manifest, key)
		if !exists || setting.Secret() {
			return fmt.Errorf("unknown or secret plugin setting %q", key)
		}
		if setting.Scope == "workspace" && update.WorkspaceID == "" {
			return fmt.Errorf("setting %q requires a workspace", key)
		}
		if err := validateSettingValue(setting, value); err != nil {
			return err
		}
		if setting.Scope == "global" {
			globalValues[key] = value
		} else {
			workspaceValues[key] = value
			workspaceChanged = true
		}
	}
	globalSecretRefs := cloneSecretReferences(installed.GlobalSecretRefs)
	workspaceSecretRefs := cloneSecretReferences(installed.WorkspaceSecretRefs[update.WorkspaceID])
	for key, secret := range update.Secrets {
		setting, exists := settingByKey(installed.Manifest, key)
		if !exists || !setting.Secret() {
			return fmt.Errorf("unknown plugin secret setting %q", key)
		}
		if len(secret.Value) > maxSecretValueBytes {
			return fmt.Errorf("secret %q exceeds the size limit", key)
		}
		if setting.Scope == "workspace" && update.WorkspaceID == "" {
			return fmt.Errorf("secret %q requires a workspace", key)
		}
		scopeID := "global"
		secretRefs := globalSecretRefs
		if setting.Scope == "workspace" {
			scopeID = update.WorkspaceID
			secretRefs = workspaceSecretRefs
			workspaceChanged = true
		}
		secretKey, err := m.secretKey(pluginID, scopeID, key)
		if err != nil {
			return err
		}
		switch secret.Source {
		case "os":
			if secret.Value == "" {
				return fmt.Errorf("secret %q value is required", key)
			}
			if err := m.secrets.Set(ctx, secretKey, secret.Value); err != nil {
				return err
			}
			secretRefs[key] = SecretReference{Source: "os"}
		case "session":
			if secret.Value == "" {
				return fmt.Errorf("secret %q value is required", key)
			}
			layered, ok := m.secrets.(*layeredSecretStore)
			if !ok {
				return fmt.Errorf("session secret storage is unavailable")
			}
			_ = m.secrets.Delete(ctx, secretKey)
			layered.SetSession(secretKey, secret.Value)
			secretRefs[key] = SecretReference{Source: "session"}
		case "environment":
			environment := strings.TrimSpace(secret.Environment)
			if environment == "" {
				environment = strings.TrimSpace(setting.Environment)
			}
			if !validEnvironmentName(environment) {
				return fmt.Errorf("secret %q environment variable name is invalid", key)
			}
			_ = m.secrets.Delete(ctx, secretKey)
			secretRefs[key] = SecretReference{Source: "environment", Environment: environment}
		case "clear":
			_ = m.secrets.Delete(ctx, secretKey)
			delete(secretRefs, key)
		default:
			return fmt.Errorf("secret %q source must be os, session, environment, or clear", key)
		}
	}
	if err := m.store.update(func(state *registryFile) error {
		current, exists := state.Plugins[pluginID]
		if !exists || current.Digest != installed.Digest {
			return fmt.Errorf("plugin changed while configuration was being saved")
		}
		current.GlobalConfig = globalValues
		current.GlobalSecretRefs = globalSecretRefs
		if current.WorkspaceConfig == nil {
			current.WorkspaceConfig = map[string]map[string]any{}
		}
		if update.WorkspaceID != "" {
			current.WorkspaceConfig[update.WorkspaceID] = workspaceValues
			if current.WorkspaceSecretRefs == nil {
				current.WorkspaceSecretRefs = map[string]map[string]SecretReference{}
			}
			current.WorkspaceSecretRefs[update.WorkspaceID] = workspaceSecretRefs
		}
		current.UpdatedAt = time.Now().UTC()
		state.Plugins[pluginID] = current
		installed = current
		return nil
	}); err != nil {
		return err
	}
	if workspaceChanged && portableWorkspace {
		if err := m.updateWorkspaceConfiguration(update.WorkspaceID, installed, workspaceValues, workspaceSecretRefs); err != nil {
			rollbackErr := m.store.update(func(state *registryFile) error {
				current, exists := state.Plugins[pluginID]
				if !exists || current.Digest != installed.Digest || !current.UpdatedAt.Equal(installed.UpdatedAt) {
					return fmt.Errorf("plugin changed while configuration rollback was in progress")
				}
				state.Plugins[pluginID] = previousInstalled
				return nil
			})
			if rollbackErr != nil {
				return fmt.Errorf("save workspace plugin configuration: %v (registry rollback failed: %w)", err, rollbackErr)
			}
			return err
		}
	}
	_ = m.runtimes.NotifyConfigChanged(pluginID, update.WorkspaceID)
	m.changed()
	return nil
}

func (m *Manager) updateWorkspaceConfiguration(workspaceID string, installed InstalledPlugin, config map[string]any, secretRefs map[string]SecretReference) error {
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
			recipe.Plugins[index].Config = config
			recipe.Plugins[index].SecretRefs = secretRefs
			found = true
			break
		}
	}
	if !found {
		recipe.Plugins = append(recipe.Plugins, PluginRecipe{
			ID: installed.Manifest.ID, Source: portableSource(installed.Source),
			Enabled: false, Config: config, SecretRefs: secretRefs,
		})
	}
	return saveWorkspaceRecipe(workspacePath, recipe)
}

func (m *Manager) catalogSettings(installed InstalledPlugin, workspaceID string) []CatalogSetting {
	resolved, references := m.nonSecretConfigAndRefs(installed, workspaceID)
	settings := make([]CatalogSetting, 0, len(installed.Manifest.Contributes.Settings))
	for _, contribution := range installed.Manifest.Contributes.Settings {
		entry := CatalogSetting{SettingContribution: contribution}
		if contribution.Secret() {
			reference := references[contribution.Key]
			entry.SecretSource = reference.Source
			entry.Configured = m.secretConfigured(installed.Manifest.ID, workspaceID, contribution, reference)
			entry.Default = nil
		} else if value, ok := resolved[contribution.Key]; ok {
			entry.Value = value
		}
		settings = append(settings, entry)
	}
	return settings
}

func (m *Manager) ResolvedConfig(ctx context.Context, installed InstalledPlugin, workspaceID string) (map[string]any, error) {
	config, references := m.nonSecretConfigAndRefs(installed, workspaceID)
	for _, setting := range installed.Manifest.Contributes.Settings {
		if !setting.Secret() {
			if setting.Required {
				value, configured := config[setting.Key]
				if !configured || validateSettingValue(setting, value) != nil {
					return nil, fmt.Errorf("required plugin setting %q is unavailable", setting.Key)
				}
			}
			continue
		}
		reference := references[setting.Key]
		value, err := m.resolveSecret(ctx, installed.Manifest.ID, workspaceID, setting, reference)
		if err != nil {
			if setting.Required {
				return nil, fmt.Errorf("required plugin secret %q is unavailable", setting.Key)
			}
			continue
		}
		if len(value) > maxSecretValueBytes {
			return nil, fmt.Errorf("plugin secret %q exceeds the size limit", setting.Key)
		}
		config[setting.Key] = value
		m.rememberSecret(installed.Manifest.ID, value)
	}
	return config, nil
}

func (m *Manager) rememberSecret(pluginID, value string) {
	if value == "" {
		return
	}
	m.redactionMu.Lock()
	defer m.redactionMu.Unlock()
	for _, existing := range m.redactions[pluginID] {
		if existing == value {
			return
		}
	}
	m.redactions[pluginID] = append(m.redactions[pluginID], value)
}

func (m *Manager) redactText(pluginID, value string) string {
	m.redactionMu.RLock()
	secrets := append([]string(nil), m.redactions[pluginID]...)
	m.redactionMu.RUnlock()
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func (m *Manager) redactValue(pluginID string, value any) any {
	switch typed := value.(type) {
	case string:
		return m.redactText(pluginID, typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = m.redactValue(pluginID, item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = m.redactValue(pluginID, item)
		}
		return result
	default:
		return value
	}
}

func (m *Manager) nonSecretConfigAndRefs(installed InstalledPlugin, workspaceID string) (map[string]any, map[string]SecretReference) {
	config := map[string]any{}
	refs := map[string]SecretReference{}
	for key, reference := range installed.GlobalSecretRefs {
		setting, ok := settingByKey(installed.Manifest, key)
		if ok && setting.Secret() && setting.Scope == "global" {
			refs[key] = reference
		}
	}
	for _, setting := range installed.Manifest.Contributes.Settings {
		if !setting.Secret() && len(setting.Default) > 0 && (setting.Scope == "global" || workspaceID != "") {
			var value any
			if json.Unmarshal(setting.Default, &value) == nil {
				config[setting.Key] = value
			}
		}
	}
	for key, value := range installed.GlobalConfig {
		setting, ok := settingByKey(installed.Manifest, key)
		if ok && !setting.Secret() && setting.Scope == "global" && validateSettingValue(setting, value) == nil {
			config[key] = value
		}
	}
	for key, value := range installed.WorkspaceConfig[workspaceID] {
		setting, ok := settingByKey(installed.Manifest, key)
		if ok && !setting.Secret() && setting.Scope == "workspace" && validateSettingValue(setting, value) == nil {
			config[key] = value
		}
	}
	for key, reference := range installed.WorkspaceSecretRefs[workspaceID] {
		setting, ok := settingByKey(installed.Manifest, key)
		if ok && setting.Secret() && setting.Scope == "workspace" {
			refs[key] = reference
		}
	}
	if workspaceID != "" && m.workspacePath != nil {
		if workspacePath, err := m.workspacePath(workspaceID); err == nil {
			if recipe, err := loadWorkspaceRecipe(workspacePath); err == nil {
				for _, entry := range recipe.Plugins {
					if entry.ID == installed.Manifest.ID {
						for key, value := range entry.Config {
							setting, ok := settingByKey(installed.Manifest, key)
							if !ok || setting.Secret() || setting.Scope != "workspace" || validateSettingValue(setting, value) != nil {
								continue
							}
							config[key] = value
						}
						break
					}
				}
			}
		}
	}
	return config, refs
}

func (m *Manager) secretConfigured(pluginID, workspaceID string, setting SettingContribution, reference SecretReference) bool {
	if reference.Source == "environment" {
		_, ok := os.LookupEnv(reference.Environment)
		return ok
	}
	scopeID := "global"
	if setting.Scope == "workspace" {
		scopeID = workspaceID
	}
	key, err := m.secretKey(pluginID, scopeID, setting.Key)
	if err != nil {
		return false
	}
	if reference.Source == "session" {
		if layered, ok := m.secrets.(*layeredSecretStore); ok {
			return layered.SessionConfigured(key)
		}
		return false
	}
	if reference.Source != "os" {
		return false
	}
	_, err = m.secrets.Get(context.Background(), key)
	return err == nil
}

func (m *Manager) resolveSecret(ctx context.Context, pluginID, workspaceID string, setting SettingContribution, reference SecretReference) (string, error) {
	if reference.Source == "environment" {
		if value, ok := os.LookupEnv(reference.Environment); ok {
			return value, nil
		}
		return "", ErrSecretNotFound
	}
	scopeID := "global"
	if setting.Scope == "workspace" {
		scopeID = workspaceID
	}
	key, err := m.secretKey(pluginID, scopeID, setting.Key)
	if err != nil {
		return "", err
	}
	if reference.Source == "session" {
		if layered, ok := m.secrets.(*layeredSecretStore); ok {
			if value, found := layered.GetSession(key); found {
				return value, nil
			}
		}
		return "", ErrSecretNotFound
	}
	if reference.Source != "os" {
		return "", ErrSecretNotFound
	}
	return m.secrets.Get(ctx, key)
}

func (m *Manager) secretKey(pluginID, scopeID, settingKey string) (string, error) {
	state, err := m.store.load()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Echo/%s/plugin/%s/%s/%s", state.InstallationID, pluginID, scopeID, settingKey), nil
}

func settingByKey(manifest Manifest, key string) (SettingContribution, bool) {
	for _, setting := range manifest.Contributes.Settings {
		if setting.Key == key {
			return setting, true
		}
	}
	return SettingContribution{}, false
}

func validateSettingValue(setting SettingContribution, value any) error {
	switch setting.Type {
	case "string", "url", "select":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("setting %q must be a string", setting.Key)
		}
		if setting.Required && strings.TrimSpace(text) == "" {
			return fmt.Errorf("setting %q is required", setting.Key)
		}
		if setting.Pattern != "" {
			matched, _ := regexpMatch(setting.Pattern, text)
			if !matched {
				return fmt.Errorf("setting %q does not match its required pattern", setting.Key)
			}
		}
		if setting.Type == "url" && text != "" {
			parsed, err := url.ParseRequestURI(text)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("setting %q must be an absolute URL", setting.Key)
			}
		}
		if setting.Type == "select" {
			valid := false
			for _, option := range setting.Options {
				valid = valid || option.Value == text
			}
			if !valid {
				return fmt.Errorf("setting %q has an unsupported value", setting.Key)
			}
		}
	case "number":
		number, ok := value.(float64)
		if !ok {
			return fmt.Errorf("setting %q must be a number", setting.Key)
		}
		if setting.Minimum != nil && number < *setting.Minimum || setting.Maximum != nil && number > *setting.Maximum {
			return fmt.Errorf("setting %q is outside its allowed range", setting.Key)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("setting %q must be true or false", setting.Key)
		}
	default:
		return fmt.Errorf("setting %q cannot be configured as a normal value", setting.Key)
	}
	return nil
}

func regexpMatch(pattern, value string) (bool, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	return compiled.MatchString(value), nil
}

func validEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func cloneAnyMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneSecretReferences(source map[string]SecretReference) map[string]SecretReference {
	clone := make(map[string]SecretReference, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
