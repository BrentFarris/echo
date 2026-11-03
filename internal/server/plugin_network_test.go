package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
	"github.com/gorilla/websocket"
)

func networkTestPlugin(t *testing.T, s *Server, origins []string) domain.PluginManifest {
	t.Helper()
	m, err := s.Plugins.Install(domain.PluginManifest{API: 1, ID: "network-test", Name: "Network test", Capabilities: []string{"web.get", "web.request", "websocket.open", "websocket.send", "websocket.receive", "websocket.close"}, Network: &domain.PluginNetwork{Origins: origins}, Views: []domain.View{{ID: "main", Kind: "floating", HTML: "<p>Network</p>"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Plugins.ApproveDigest(m.ID, m.Digest)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPluginNetworkLegacyPolicyAndFreshBotScope(t *testing.T) {
	s := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer upstream.Close()
	m := networkTestPlugin(t, s, nil)
	bot, _ := s.Bot("echo")
	scope := domain.ExecutionScope{Bot: bot, PluginID: m.ID}
	raw, _ := json.Marshal(map[string]string{"url": upstream.URL})
	// Even another plugin's explicit private-network grant must not help.
	web, _ := s.Plugins.Get("web-get")
	web.Config["allowedOrigins"] = []string{upstream.URL}
	s.Plugins.Save(web)
	if _, err := s.pluginCall(context.Background(), scope, "web.get", raw); err == nil || !strings.Contains(err.Error(), "network.origins") {
		t.Fatal(err)
	}
	m = networkTestPlugin(t, s, []string{upstream.URL})
	if _, err := s.pluginCall(context.Background(), scope, "web.request", raw); err != nil {
		t.Fatal(err)
	}
	bot.Plugins[m.ID] = false
	if _, err := s.updateBot(bot, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pluginCall(context.Background(), scope, "web.request", raw); err == nil {
		t.Fatal("stale bot scope authorized network")
	}
}

func networkRelayCall(t *testing.T, conn *websocket.Conn, method string, params any) map[string]any {
	t.Helper()
	id := domain.ID()
	if err := conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var reply map[string]any
	if err := conn.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if reply["id"] != id || reply["error"] != nil {
		t.Fatal(reply)
	}
	return reply["result"].(map[string]any)
}

func TestPluginNetworkRelayExchangeIsolationAndRevocation(t *testing.T) {
	for _, revoke := range []string{"plugin", "bot", "session", "package"} {
		t.Run(revoke, func(t *testing.T) {
			s := testServer(t)
			setRemotePassword(t, s)
			cookie := remoteLogin(t, s)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/socket" {
					io.WriteString(w, "host HTTP")
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					kind, data, err := conn.ReadMessage()
					if err != nil {
						return
					}
					if conn.WriteMessage(kind, data) != nil {
						return
					}
				}
			}))
			defer upstream.Close()
			wsOrigin := "ws" + strings.TrimPrefix(upstream.URL, "http")
			m := networkTestPlugin(t, s, []string{upstream.URL, wsOrigin})
			host := httptest.NewServer(s.Handler())
			defer host.Close()
			query := url.Values{"botId": {"echo"}, "viewId": {"main"}, "digest": {m.Digest}}
			address := "ws" + strings.TrimPrefix(host.URL, "http") + "/api/v1/plugins/" + m.ID + "/network?" + query.Encode()
			headers := http.Header{"Origin": {host.URL}, "Cookie": {cookie.Name + "=" + cookie.Value}}
			if conn, _, err := websocket.DefaultDialer.Dial(address, http.Header{"Origin": {"https://foreign.invalid"}, "Cookie": headers["Cookie"]}); err == nil {
				conn.Close()
				t.Fatal("foreign origin accepted")
			}
			conn, _, err := websocket.DefaultDialer.Dial(address, headers)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			result := networkRelayCall(t, conn, "web.request", map[string]string{"url": upstream.URL})
			if result["content"] != "host HTTP" {
				t.Fatal(result)
			}
			handle := networkRelayCall(t, conn, "websocket.open", map[string]string{"url": wsOrigin + "/socket"})["handle"].(string)
			networkRelayCall(t, conn, "websocket.send", map[string]any{"handle": handle, "data": "AAE=", "binary": true, "encoding": "base64"})
			result = networkRelayCall(t, conn, "websocket.receive", map[string]string{"handle": handle})
			if result["data"] != "AAE=" || result["binary"] != true {
				t.Fatal(result)
			}
			// Keep a receive active while permission is revoked.
			conn.WriteJSON(map[string]any{"id": "pending", "method": "websocket.receive", "params": map[string]string{"handle": handle}})
			switch revoke {
			case "plugin":
				m.Enabled = false
				err = s.Plugins.Save(m)
			case "bot":
				bot, _ := s.Bot("echo")
				bot.Plugins[m.ID] = false
				_, err = s.updateBot(bot, nil)
			case "package":
				m.Version = "2"
				_, err = s.Plugins.Install(m, nil)
			case "session":
				owner := remoteLogin(t, s)
				w := remoteRequest(s, "DELETE", "/hosting/sessions/"+cookieKey(cookie), "", owner)
				if w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			for {
				_, _, err = conn.ReadMessage()
				if err != nil {
					break
				}
			}
			if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
				t.Fatal("relay survived revocation")
			}
		})
	}
}

func TestPluginNetworkRelaySwarmOwner(t *testing.T) {
	a, _ := swarmFixture(t, "network-entry", remotePassword)
	b, bh := swarmFixture(t, "network-owner", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "remote owner") }))
	defer upstream.Close()
	m := networkTestPlugin(t, b, []string{upstream.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := a.Swarm.Watch(ctx, b.Swarm.Node().ID, "/api/v1/plugins/"+m.ID+"/network?botId=echo&viewId=main&digest="+m.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	result := networkRelayCall(t, conn, "web.request", map[string]string{"url": upstream.URL})
	if result["content"] != "remote owner" {
		t.Fatal(result)
	}
	if _, err = a.Plugins.Get(m.ID); err == nil {
		t.Fatal("fixture should have no local plugin")
	}
}

func TestPluginNetworkWASIInvocation(t *testing.T) {
	s := testServer(t)
	disconnected := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/socket" {
			io.WriteString(w, "backend HTTP")
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { conn.Close(); disconnected <- struct{}{} }()
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if conn.WriteMessage(kind, data) != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	source := strings.Replace(plugins.BackendTemplate, `json.NewEncoder(os.Stdout).Encode(map[string]any{"result":input.Params})`, `
 httpResponse:=call("web.request",map[string]any{"url":input.Params["http"]})
 var opened struct{Result struct{Handle string};Error string}
 json.Unmarshal(call("websocket.open",map[string]any{"url":input.Params["socket"]}),&opened)
 if opened.Error!=""{json.NewEncoder(os.Stdout).Encode(map[string]any{"error":opened.Error});return}
 call("websocket.send",map[string]any{"handle":opened.Result.Handle,"data":"backend socket"})
 message:=call("websocket.receive",map[string]any{"handle":opened.Result.Handle})
 json.NewEncoder(os.Stdout).Encode(map[string]any{"result":map[string]any{"http":httpResponse,"socket":message}})
 // Deliberately omit close: invocation teardown must close the connection.
 `, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	binary := filepath.Join(dir, "backend.wasm")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", binary, path)
	command.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	wasm, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	wsOrigin := "ws" + strings.TrimPrefix(upstream.URL, "http")
	m := domain.PluginManifest{API: 1, ID: "network-backend", Name: "Backend", Capabilities: []string{"web.request", "websocket.open", "websocket.send", "websocket.receive"}, Network: &domain.PluginNetwork{Origins: []string{upstream.URL, wsOrigin}}, Tools: []domain.Tool{{Name: "network_backend_test", Parameters: plugins.Schema(map[string]any{"http": plugins.S("HTTP URL"), "socket": plugins.S("WebSocket URL")})}}}
	m, err = s.Plugins.Install(m, wasm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Plugins.ApproveDigest(m.ID, m.Digest); err != nil {
		t.Fatal(err)
	}
	bot, _ := s.Bot("echo")
	args, _ := json.Marshal(map[string]string{"http": upstream.URL, "socket": wsOrigin + "/socket"})
	value, err := s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: bot}, "network_backend_test", args)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	if !strings.Contains(string(encoded), "backend HTTP") || !strings.Contains(string(encoded), "backend socket") {
		t.Fatal(string(encoded))
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("socket outlived WASI invocation")
	}
}
