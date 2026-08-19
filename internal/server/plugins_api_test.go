package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPluginAPITestServer(t *testing.T, safeMode bool) *Server {
	t.Helper()
	directory, err := os.MkdirTemp(".", ".plugin-api-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(absolute) })
	if err := os.WriteFile(filepath.Join(absolute, "index.html"), []byte("<!doctype html><title>Echo</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := NewWithSettingsPathOptions("127.0.0.1:0", absolute, filepath.Join(absolute, "echo.json"), ServerOptions{SafeMode: safeMode})
	server.authDisabled = true
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	return server
}

func pluginAPIRequest(t *testing.T, server *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(data))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	return response
}

func pluginAPIData(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, response.Body.String())
	}
	if !envelope.OK {
		t.Fatalf("API failed: %s", envelope.Error)
	}
	return envelope.Data
}

func TestPluginAPIStagesApprovesAndServesSandboxedUI(t *testing.T) {
	server := newPluginAPITestServer(t, false)
	staged := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/stages", map[string]any{"source": map[string]any{"type": "builtin", "builtin": "calculator"}})
	if staged.Code != http.StatusCreated {
		t.Fatalf("stage: %d %s", staged.Code, staged.Body.String())
	}
	stage := pluginAPIData(t, staged)["stage"].(map[string]any)
	stageID := stage["id"].(string)
	approved := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/stages/"+stageID+"/approve", map[string]any{"scope": "global", "enable": true})
	if approved.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	catalogResponse := pluginAPIRequest(t, server, http.MethodGet, "/api/plugins", nil)
	catalog := pluginAPIData(t, catalogResponse)
	plugins := catalog["plugins"].([]any)
	if len(plugins) != 1 || !plugins[0].(map[string]any)["effective"].(bool) {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}

	created := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/calculator/views/calculator/sessions", map[string]any{})
	if created.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", created.Code, created.Body.String())
	}
	session := pluginAPIData(t, created)["session"].(map[string]any)
	entryURL := session["entryUrl"].(string)
	asset := pluginAPIRequest(t, server, http.MethodGet, entryURL, nil)
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "Calculator") {
		t.Fatalf("asset: %d %s", asset.Code, asset.Body.String())
	}
	if asset.Header().Get("X-Frame-Options") != "" {
		t.Fatalf("plugin iframe was blocked by X-Frame-Options: %v", asset.Header())
	}
	csp := asset.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "frame-ancestors 'self'") || !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("plugin CSP is unsafe: %q", csp)
	}
	if strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "/plugin-ui/") {
		t.Fatalf("plugin scripts were not confined to the UI session: %q", csp)
	}
	if asset.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("sandboxed module scripts cannot load without credential-free CORS: %v", asset.Header())
	}

	token := session["bridgeToken"].(string)
	forged := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/ui-sessions/"+token+"/bridge", map[string]any{"nonce": "forged", "method": "storage.get", "params": map[string]any{"scope": "global", "key": "value"}})
	if forged.Code != http.StatusForbidden {
		t.Fatalf("forged nonce accepted: %d", forged.Code)
	}
	valid := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/ui-sessions/"+token+"/bridge", map[string]any{"nonce": session["nonce"], "method": "storage.set", "params": map[string]any{"scope": "global", "key": "value", "value": 42}})
	if valid.Code != http.StatusOK {
		t.Fatalf("bridge: %d %s", valid.Code, valid.Body.String())
	}

	disabled := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/calculator/actions", map[string]any{"action": "disable-global"})
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", disabled.Code, disabled.Body.String())
	}
	asset = pluginAPIRequest(t, server, http.MethodGet, entryURL, nil)
	if asset.Code != http.StatusNotFound {
		t.Fatalf("disabled UI session remained valid: %d", asset.Code)
	}
}

func TestPluginAPISafeModeReportsButDoesNotActivate(t *testing.T) {
	server := newPluginAPITestServer(t, true)
	staged := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/stages", map[string]any{"source": map[string]any{"type": "builtin", "builtin": "calculator"}})
	stageID := pluginAPIData(t, staged)["stage"].(map[string]any)["id"].(string)
	approved := pluginAPIRequest(t, server, http.MethodPost, "/api/plugins/stages/"+stageID+"/approve", map[string]any{"scope": "global", "enable": true})
	if approved.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	catalog := pluginAPIData(t, pluginAPIRequest(t, server, http.MethodGet, "/api/plugins", nil))
	if !catalog["safeMode"].(bool) || catalog["plugins"].([]any)[0].(map[string]any)["effective"].(bool) {
		t.Fatalf("safe mode activation leak: %#v", catalog)
	}
}
