//go:build windows

package server

import (
	"context"
	"encoding/json"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/plugins"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNativeBackendAuthoringTemplate(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("native compiler integration")
	}
	s := testServer(t)
	wasm, e := s.compilePlugin(context.Background(), plugins.BackendTemplate)
	if e != nil {
		t.Fatal(e)
	}
	template, e := plugins.Template("backend")
	if e != nil {
		t.Fatal(e)
	}
	manifest := template.(map[string]any)["manifest"].(domain.PluginManifest)
	installed, e := s.Plugins.Install(manifest, wasm)
	if e != nil || installed.Pending {
		t.Fatal("pure backend failed to activate", e)
	}
	echo, _ := s.Bot("echo")
	result, e := s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: echo}, "my_plugin_echo", json.RawMessage(`{"text":"compiled within AppContainer"}`))
	data, _ := json.Marshal(result)
	if e != nil || !strings.Contains(string(data), "compiled within AppContainer") {
		t.Fatal(string(data), e)
	}
}

func TestNativeProviderTemplate(t *testing.T) {
	if os.Getenv("ECHO2_NATIVE_TESTS") != "1" {
		t.Skip("native compiler integration")
	}
	s := testServer(t)
	wasm, e := s.compilePlugin(context.Background(), plugins.ProviderTemplate)
	if e != nil {
		t.Fatal(e)
	}
	template, e := plugins.Template("provider")
	if e != nil {
		t.Fatal(e)
	}
	manifest := template.(map[string]any)["manifest"].(domain.PluginManifest)
	installed, e := s.Plugins.Install(manifest, wasm)
	if e != nil || !installed.Pending {
		t.Fatal("provider capability must await approval", e)
	}
	if _, e = s.Plugins.ApproveDigest(installed.ID, installed.Digest); e != nil {
		t.Fatal(e)
	}
	echo, _ := s.Bot("echo")
	for _, tool := range s.Plugins.Tools(echo) {
		if tool.Name == manifest.Providers[0] {
			t.Fatal("provider appeared in model tools")
		}
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-provider-key" {
			t.Error("credential missing from host request")
		}
		stream(w, "Through WASI: secret-provider-key", nil)
	}))
	defer remote.Close()
	endpoint := domain.Endpoint{ID: "custom", URL: remote.URL, Provider: manifest.Providers[0], Model: "fixture"}
	ctx := context.WithValue(context.Background(), providerKey{}, &providerInvocation{Scope: domain.ExecutionScope{Bot: echo}})
	streamed := ""
	result, e := s.complete(ctx, endpoint, domain.Generation{Model: "fixture"}, []model.Message{{Role: "user", Content: "hello"}}, nil, "secret-provider-key", func(d model.Delta) { streamed += d.Content })
	if e != nil || result.Content != "Through WASI: [redacted]" || streamed != result.Content {
		t.Fatal(result, streamed, e)
	}
	if _, e = s.providerOperation(context.Background(), domain.ExecutionScope{Bot: echo, PluginID: manifest.ID}, "model.request", json.RawMessage(`{}`)); e == nil {
		t.Fatal("model access without provider scope")
	}
	bot, e := s.saveBot(domain.Bot{Handle: "restricted", Name: "Restricted", Plugins: map[string]bool{manifest.ID: false}})
	if e != nil {
		t.Fatal(e)
	}
	s.Store.Put("endpoints", endpoint.ID, endpoint)
	g := domain.Generation{EndpointID: endpoint.ID, Model: "fixture"}
	c := chat(t, s, g)
	if _, e = s.send(c.ID, SendRequest{Content: "@" + bot.Handle + " hello", Generation: g}); e == nil {
		t.Fatal("disabled provider dispatched")
	}
	messages, e := s.Store.Messages(c.ID)
	if e != nil || len(messages) != 0 {
		t.Fatal("rejected provider persisted a message", messages, e)
	}
}
