package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func configureConfluence(t *testing.T, s *Server, base string) {
	t.Helper()
	s.Secrets = scmSecrets{}
	for _, step := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/plugins/confluence", map[string]any{"enabled": true, "config": map[string]any{"deployment": "datacenter", "url": base}}},
		{"POST", "/plugins/confluence/credential", map[string]any{"secret": "confluence-private-token"}},
	} {
		r := scmAPI(t, s, step.method, step.path, step.body)
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
	}
}

func TestConfluenceCredentialLifecycleAndOwnerSettings(t *testing.T) {
	s := testServer(t)
	m, err := s.Plugins.Get("confluence")
	if err != nil || !m.Builtin || !m.Enabled || m.Scope != "bot" {
		t.Fatal(m, err)
	}
	configureConfluence(t, s, "http://confluence.internal/confluence")
	secrets := s.Secrets.(scmSecrets)
	m, _ = s.Plugins.Get("confluence")
	old := text(m.Config, "credential")
	if secrets[old] != "confluence-private-token" {
		t.Fatal("missing vaulted token")
	}
	if result := scmAPI(t, s, "GET", "/state", nil); strings.Contains(result.Body.String(), "confluence-private-token") {
		t.Fatal("token leaked into state")
	}
	for _, updates := range []map[string]any{{"url": "http://evil.example"}, {"email": "other@example.com"}, {"tokenType": "scoped"}} {
		if _, err = confluenceSettings(m, updates, false); err == nil {
			t.Fatal("bot changed connection")
		}
	}
	for _, updates := range []map[string]any{{"credential": "injected"}, {"token": "plain-text"}} {
		if _, err = confluenceSettings(m, updates, true); err == nil {
			t.Fatal("unsafe settings accepted")
		}
	}
	bot, _ := s.Bot("echo")
	scope := domain.ExecutionScope{Bot: bot, PluginID: "plugin-authoring"}
	if _, err = s.pluginCall(context.Background(), scope, "plugin.configure", json.RawMessage(`{"id":"confluence","config":{"url":"http://evil.example"}}`)); err == nil {
		t.Fatal("management tool changed connection")
	}
	if r := scmAPI(t, s, "PATCH", "/plugins/confluence", map[string]any{"enabled": false, "config": m.Config}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	m, _ = s.Plugins.Get("confluence")
	if text(m.Config, "credential") != old {
		t.Fatal("disable lost token")
	}
	if r := scmAPI(t, s, "POST", "/plugins/confluence/credential", map[string]any{"secret": "replacement"}); r.Code != 200 || secrets[old] != "" || len(secrets) != 1 {
		t.Fatal("rotation failed", r.Body.String())
	}
	if r := scmAPI(t, s, "PATCH", "/plugins/confluence", map[string]any{"enabled": true, "config": map[string]any{"url": "http://new.internal/confluence"}}); r.Code != 200 || len(secrets) != 0 {
		t.Fatal("identity change retained token", r.Body.String())
	}
	if r := scmAPI(t, s, "POST", "/plugins/confluence/credential", map[string]any{"secret": "replacement"}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := scmAPI(t, s, "POST", "/plugins/confluence/credential", map[string]any{"secret": ""}); r.Code != 200 || len(secrets) != 0 {
		t.Fatal("clear failed", r.Body.String())
	}
	for _, path := range []string{"credential", "test"} {
		r := httptest.NewRequest("POST", "http://localhost/api/v1/plugins/confluence/"+path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("unauthenticated owner endpoint", path, w.Code)
		}
	}
}

func TestConfluenceWASIBridgeAndBotAvailability(t *testing.T) {
	s := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer confluence-private-token" {
			t.Error("missing token")
		}
		switch r.URL.Path {
		case "/confluence/rest/api/search":
			fmt.Fprint(w, `{"results":[{"content":{"id":"1","type":"page","title":"Read this"}}]}`)
		case "/confluence/rest/api/content/1":
			fmt.Fprint(w, `{"id":"1","type":"page","title":"Read this","version":{"number":2},"body":{"storage":{"value":"<h1>Full page</h1><p>Complete body</p>"}}}`)
		default:
			t.Error("unexpected URL", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	configureConfluence(t, s, upstream.URL+"/confluence")
	if r := scmAPI(t, s, "POST", "/plugins/confluence/test", map[string]any{}); r.Code != 200 || !strings.Contains(r.Body.String(), `"connected":true`) {
		t.Fatal(r.Body.String())
	}
	bot, _ := s.Bot("echo")
	scope := domain.ExecutionScope{Bot: bot}
	for _, step := range []struct{ name, args, want string }{{"confluence_search", `{"cql":"type=page"}`, "Read this"}, {"confluence_read", `{"id":"1"}`, "Complete body"}, {"confluence_read", `{"id":"1","format":"storage"}`, "<h1>Full page</h1>"}} {
		result, err := s.Plugins.Invoke(context.Background(), scope, step.name, json.RawMessage(step.args))
		raw, _ := json.Marshal(result)
		if err != nil || !strings.Contains(string(raw), strings.ReplaceAll(strings.ReplaceAll(step.want, "<", "\\u003c"), ">", "\\u003e")) {
			t.Fatal(string(raw), err)
		}
		if strings.Contains(string(raw), "confluence-private-token") {
			t.Fatal("token leaked through WASI")
		}
	}
	scope.Bot.Plugins = map[string]bool{"confluence": false}
	if _, err := s.Plugins.Invoke(context.Background(), scope, "confluence_search", json.RawMessage(`{"cql":"type=page"}`)); err == nil {
		t.Fatal("disabled bot accessed plugin")
	}
	m, _ := s.Plugins.Get("confluence")
	if _, err := s.confluenceOperation(context.Background(), domain.ExecutionScope{PluginID: "other"}, m, "confluence.search", json.RawMessage(`{"cql":"type=page"}`)); err == nil {
		t.Fatal("foreign plugin accessed credential")
	}
}

func TestConfluenceSettingsCancelActiveRequests(t *testing.T) {
	for _, action := range []string{"rotate", "clear", "destination", "disable"} {
		t.Run(action, func(t *testing.T) {
			s := testServer(t)
			started := make(chan struct{})
			canceled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(canceled) }))
			defer upstream.Close()
			configureConfluence(t, s, upstream.URL+"/confluence")
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() { finished <- scmAPI(t, s, "POST", "/plugins/confluence/test", map[string]any{}) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not start")
			}
			method, path, body := "POST", "/plugins/confluence/credential", map[string]any{"secret": "new-token"}
			switch action {
			case "clear":
				body["secret"] = ""
			case "destination":
				method = "PATCH"
				path = "/plugins/confluence"
				body = map[string]any{"enabled": true, "config": map[string]any{"url": "http://other.internal"}}
			case "disable":
				method = "PATCH"
				path = "/plugins/confluence"
				body = map[string]any{"enabled": false}
			}
			if r := scmAPI(t, s, method, path, body); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
			select {
			case <-canceled:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream request survived configuration change")
			}
			select {
			case r := <-finished:
				if r.Code == 200 {
					t.Fatal("canceled request succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("owner request did not finish")
			}
		})
	}
}

func TestConfluenceUserToolsResolveAuthorThroughWASI(t *testing.T) {
	s := testServer(t)
	authorQuery := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer confluence-private-token" {
			t.Error("missing host-injected credential")
		}
		switch r.URL.Path {
		case "/confluence/rest/api/user/current":
			fmt.Fprint(w, `{"type":"known","username":"service.account","userKey":"service-key","displayName":"Service Account"}`)
		case "/confluence/rest/api/search":
			cql := r.URL.Query().Get("cql")
			if strings.HasPrefix(cql, "user.fullname") {
				if cql != `user.fullname ~ "Brent Farris"` {
					t.Error("incorrect name query", cql)
				}
				fmt.Fprint(w, `{"results":[{"user":{"username":"brent.farris","userKey":"brent-hash","displayName":"Brent Farris"}}]}`)
			} else {
				authorQuery = cql
				if r.URL.Query().Get("limit") != "3" {
					t.Error("missing three-page limit")
				}
				fmt.Fprint(w, `{"results":[{"content":{"id":"3","type":"page","title":"Third"}},{"content":{"id":"2","type":"page","title":"Second"}},{"content":{"id":"1","type":"page","title":"First"}}]}`)
			}
		default:
			t.Error("unexpected request", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	configureConfluence(t, s, upstream.URL+"/confluence")
	bot, _ := s.Bot("echo")
	scope := domain.ExecutionScope{Bot: bot}
	current, err := s.Plugins.Invoke(context.Background(), scope, "confluence_get_current_user", json.RawMessage(`{}`))
	if err != nil || current.(map[string]any)["username"] != "service.account" {
		t.Fatal(current, err)
	}
	users, err := s.Plugins.Invoke(context.Background(), scope, "confluence_search_users", json.RawMessage(`{"query":"Brent Farris"}`))
	if err != nil {
		t.Fatal(err)
	}
	matched := users.(map[string]any)["results"].([]any)[0].(map[string]any)
	if matched["userKey"] != "brent-hash" {
		t.Fatal(matched)
	}
	cql := fmt.Sprintf(`type = page AND creator = %q ORDER BY created DESC`, matched["username"])
	args, _ := json.Marshal(map[string]any{"cql": cql, "limit": 3})
	pages, err := s.Plugins.Invoke(context.Background(), scope, "confluence_search", args)
	if err != nil || len(pages.(map[string]any)["results"].([]any)) != 3 || authorQuery != `type = page AND creator = "brent.farris" ORDER BY created DESC` {
		t.Fatal(pages, err, authorQuery)
	}
	for _, value := range []any{current, users, pages} {
		data, _ := json.Marshal(value)
		if strings.Contains(string(data), "confluence-private-token") {
			t.Fatal("credential escaped the host")
		}
	}
	scope.Bot.Plugins = map[string]bool{"confluence": false}
	for _, tool := range []struct{ name, args string }{{"confluence_get_current_user", `{}`}, {"confluence_search_users", `{"query":"Brent"}`}} {
		if _, err = s.Plugins.Invoke(context.Background(), scope, tool.name, json.RawMessage(tool.args)); err == nil {
			t.Fatal("disabled bot accessed", tool.name)
		}
	}
}
