package plugins

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	uiSessionLifetime = 30 * time.Minute
	maxUIAssetBytes   = 16 << 20
	maxStorageBytes   = 1 << 20
	maxStorageValue   = 64 << 10
)

var storageKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type UISession struct {
	ID          string         `json:"id"`
	Token       string         `json:"bridgeToken"`
	Nonce       string         `json:"nonce"`
	PluginID    string         `json:"pluginId"`
	PluginName  string         `json:"pluginName"`
	ViewID      string         `json:"viewId"`
	ViewTitle   string         `json:"viewTitle"`
	ViewKind    string         `json:"viewKind"`
	WorkspaceID string         `json:"workspaceId,omitempty"`
	Digest      string         `json:"digest"`
	Revision    string         `json:"revision"`
	EntryURL    string         `json:"entryUrl"`
	ExpiresAt   time.Time      `json:"expiresAt"`
	Config      map[string]any `json:"config,omitempty"`
}

type UIAsset struct {
	Data        []byte
	ContentType string
	Digest      string
}

type UIBridgeRequest struct {
	Nonce  string         `json:"nonce"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

func (m *Manager) CreateUISession(pluginID, viewID, workspaceID string) (UISession, error) {
	if workspaceID != "" && !validOpaqueID(workspaceID) {
		return UISession{}, fmt.Errorf("invalid workspace id")
	}
	if !m.IsEnabled(pluginID, workspaceID) {
		return UISession{}, fmt.Errorf("plugin is not enabled for this workspace")
	}
	installed, ok, err := m.Installed(pluginID)
	if err != nil || !ok {
		return UISession{}, fmt.Errorf("plugin was not found")
	}
	if err := m.verifyInstalledSnapshot(installed); err != nil {
		m.setHealth(pluginID, err.Error())
		_ = m.reconcileTools()
		m.ClosePluginUISessions(pluginID)
		m.changed()
		return UISession{}, err
	}
	view, ok := installed.Manifest.View(viewID)
	if !ok {
		return UISession{}, fmt.Errorf("plugin view was not found")
	}
	config, _ := m.nonSecretConfigAndRefs(installed, workspaceID)
	token := secureToken(32)
	nonce := secureToken(24)
	session := UISession{
		ID: randomID("ui-"), Token: token, Nonce: nonce,
		PluginID: pluginID, PluginName: installed.Manifest.Name, ViewID: view.ID,
		ViewTitle: view.Title, ViewKind: view.Kind, WorkspaceID: workspaceID,
		Digest: installed.Digest, Revision: installed.UpdatedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: time.Now().UTC().Add(uiSessionLifetime), Config: config,
	}
	session.EntryURL = fmt.Sprintf("/plugin-ui/%s/%s?digest=%s", token, filepath.ToSlash(view.Entry), installed.Digest)
	m.uiMu.Lock()
	m.pruneUISessionsLocked()
	m.uiSessions[token] = session
	m.uiMu.Unlock()
	return session, nil
}

func (m *Manager) UIAsset(token, requestPath string) (UIAsset, error) {
	session, installed, err := m.activeUISession(token, "")
	if err != nil {
		return UIAsset{}, err
	}
	requestPath = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(requestPath), "/"))
	if requestPath == "" || !(strings.HasPrefix(requestPath, "ui/") || strings.HasPrefix(requestPath, "assets/")) {
		return UIAsset{}, fmt.Errorf("plugin asset path is not allowed")
	}
	path, err := packagePath(installed.PackagePath, requestPath)
	if err != nil {
		return UIAsset{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxUIAssetBytes {
		return UIAsset{}, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return UIAsset{}, err
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_ = session
	return UIAsset{Data: data, ContentType: contentType, Digest: installed.Digest}, nil
}

func (m *Manager) InvokeUIBridge(ctx context.Context, token string, request UIBridgeRequest) (any, error) {
	session, _, err := m.activeUISession(token, request.Nonce)
	if err != nil {
		return nil, err
	}
	switch request.Method {
	case "rpc.invoke":
		method, _ := request.Params["method"].(string)
		return m.InvokeUI(ctx, session.PluginID, method, session.WorkspaceID, session.ID, request.Params["params"])
	case "storage.get":
		key, scope, err := storageRequest(request.Params, session.WorkspaceID)
		if err != nil {
			return nil, err
		}
		value, found, err := m.readStorage(session.PluginID, scope, key)
		return map[string]any{"value": value, "found": found}, err
	case "storage.set":
		key, scope, err := storageRequest(request.Params, session.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if err := m.writeStorage(session.PluginID, scope, key, request.Params["value"]); err != nil {
			return nil, err
		}
		return map[string]any{"stored": true}, nil
	case "storage.delete":
		key, scope, err := storageRequest(request.Params, session.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if err := m.deleteStorage(session.PluginID, scope, key); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, nil
	default:
		return nil, fmt.Errorf("UI bridge method is not allowed")
	}
}

func (m *Manager) activeUISession(token, nonce string) (UISession, InstalledPlugin, error) {
	m.uiMu.Lock()
	m.pruneUISessionsLocked()
	session, ok := m.uiSessions[token]
	if ok {
		session.ExpiresAt = time.Now().UTC().Add(uiSessionLifetime)
		m.uiSessions[token] = session
	}
	m.uiMu.Unlock()
	if !ok || (nonce != "" && nonce != session.Nonce) {
		return UISession{}, InstalledPlugin{}, fmt.Errorf("invalid or expired plugin UI session")
	}
	installed, found, err := m.Installed(session.PluginID)
	if err != nil || !found || installed.Digest != session.Digest || !m.IsEnabled(session.PluginID, session.WorkspaceID) {
		return UISession{}, InstalledPlugin{}, fmt.Errorf("plugin UI session is no longer active")
	}
	return session, installed, nil
}

func (m *Manager) ClosePluginUISessions(pluginID string) {
	m.uiMu.Lock()
	defer m.uiMu.Unlock()
	for token, session := range m.uiSessions {
		if session.PluginID == pluginID {
			delete(m.uiSessions, token)
		}
	}
}

func (m *Manager) CloseUISession(token string) {
	m.uiMu.Lock()
	defer m.uiMu.Unlock()
	delete(m.uiSessions, token)
}

func (m *Manager) CloseInactiveUISessions() {
	m.uiMu.Lock()
	sessions := make([]UISession, 0, len(m.uiSessions))
	for _, session := range m.uiSessions {
		sessions = append(sessions, session)
	}
	m.uiMu.Unlock()
	for _, session := range sessions {
		if !m.IsEnabled(session.PluginID, session.WorkspaceID) {
			m.uiMu.Lock()
			delete(m.uiSessions, session.Token)
			m.uiMu.Unlock()
		}
	}
}

func (m *Manager) pruneUISessionsLocked() {
	now := time.Now()
	for token, session := range m.uiSessions {
		if now.After(session.ExpiresAt) {
			delete(m.uiSessions, token)
		}
	}
}

func (m *Manager) readStorage(pluginID, scope, key string) (any, bool, error) {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	values, _, err := m.loadStorage(pluginID, scope)
	if err != nil {
		return nil, false, err
	}
	value, ok := values[key]
	return value, ok, nil
}

func (m *Manager) writeStorage(pluginID, scope, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxStorageValue {
		return fmt.Errorf("plugin storage value is invalid or too large")
	}
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	values, path, err := m.loadStorage(pluginID, scope)
	if err != nil {
		return err
	}
	values[key] = value
	data, err := json.Marshal(values)
	if err != nil || len(data) > maxStorageBytes {
		return fmt.Errorf("plugin storage quota exceeded")
	}
	return writeAtomic(path, data, 0o600)
}

func (m *Manager) deleteStorage(pluginID, scope, key string) error {
	m.storageMu.Lock()
	defer m.storageMu.Unlock()
	values, path, err := m.loadStorage(pluginID, scope)
	if err != nil {
		return err
	}
	delete(values, key)
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

func (m *Manager) loadStorage(pluginID, scope string) (map[string]any, string, error) {
	path, err := packagePath(filepath.Join(m.root, "data", pluginID), filepath.ToSlash(filepath.Join(scope, "storage.json")))
	if err != nil {
		return nil, "", fmt.Errorf("invalid plugin storage scope")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	if len(data) > maxStorageBytes {
		return nil, path, fmt.Errorf("plugin storage is corrupt")
	}
	values := map[string]any{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, path, fmt.Errorf("plugin storage is corrupt")
	}
	return values, path, nil
}

func storageRequest(params map[string]any, workspaceID string) (string, string, error) {
	key, _ := params["key"].(string)
	if !storageKeyPattern.MatchString(key) {
		return "", "", fmt.Errorf("invalid plugin storage key")
	}
	scope, _ := params["scope"].(string)
	if scope == "global" || scope == "" {
		return key, "global", nil
	}
	if scope == "workspace" && workspaceID != "" {
		return key, filepath.Join("workspaces", workspaceID), nil
	}
	return "", "", fmt.Errorf("workspace storage is unavailable")
}

func secureToken(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return randomID("token-")
	}
	return base64.RawURLEncoding.EncodeToString(value)
}
