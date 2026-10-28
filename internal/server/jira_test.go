package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/plugins"
)

func configureJira(t *testing.T, s *Server, base string) {
	t.Helper()
	s.Secrets = scmSecrets{}
	for _, step := range []struct {
		path, method string
		body         any
	}{
		{"/plugins/jira", "PATCH", map[string]any{"enabled": true, "config": map[string]any{"deployment": "datacenter", "url": base}}},
		{"/plugins/jira/credential", "POST", map[string]any{"secret": "jira-private-token"}},
	} {
		response := scmAPI(t, s, step.method, step.path, step.body)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}

func TestJiraCredentialLifecycleAndOwnerSettings(t *testing.T) {
	s := testServer(t)
	m, err := s.Plugins.Get("jira")
	if err != nil || !m.Builtin || m.Enabled || m.Scope != "bot" {
		t.Fatal(m, err)
	}
	configureJira(t, s, "http://jira.example/jira")
	secrets := s.Secrets.(scmSecrets)
	m, _ = s.Plugins.Get("jira")
	oldRef := text(m.Config, "credential")
	if oldRef == "" || secrets[oldRef] != "jira-private-token" {
		t.Fatal("credential not stored")
	}
	state := scmAPI(t, s, "GET", "/state", nil)
	if strings.Contains(state.Body.String(), "jira-private-token") {
		t.Fatal("token exposed in state")
	}
	if _, err := jiraSettings(m, map[string]any{"url": "https://elsewhere"}, false); err == nil {
		t.Fatal("model changed destination")
	}
	if _, err := jiraSettings(m, map[string]any{"credential": "invented"}, true); err == nil {
		t.Fatal("credential reference injection")
	}
	if _, err := jiraSettings(m, map[string]any{"token": "unsafe"}, true); err == nil {
		t.Fatal("token persisted as config")
	}
	response := scmAPI(t, s, "PATCH", "/plugins/jira", map[string]any{"enabled": false, "config": m.Config})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	m, _ = s.Plugins.Get("jira")
	if text(m.Config, "credential") != oldRef {
		t.Fatal("enable switch lost credential")
	}
	response = scmAPI(t, s, "POST", "/plugins/jira/credential", map[string]any{"secret": "replacement-token"})
	if response.Code != 200 || len(secrets) != 1 || secrets[oldRef] != "" {
		t.Fatal("replacement failed", response.Body.String())
	}
	m, _ = s.Plugins.Get("jira")
	for _, update := range []map[string]any{{"url": "http://other.example/jira"}, {"deployment": "cloud", "url": "https://jira.example", "email": "a@example.com"}, {"deployment": "cloud", "url": "https://jira.example", "email": "b@example.com"}, {"cloudId": "cloud-1"}} {
		response = scmAPI(t, s, "PATCH", "/plugins/jira", map[string]any{"enabled": true, "config": update})
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		m, _ = s.Plugins.Get("jira")
		if text(m.Config, "credential") != "" || len(secrets) != 0 {
			t.Fatal("credential survived identity change")
		}
		response = scmAPI(t, s, "POST", "/plugins/jira/credential", map[string]any{"secret": "replacement-token"})
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
	}
	response = scmAPI(t, s, "POST", "/plugins/jira/credential", map[string]any{"secret": ""})
	if response.Code != 200 || len(secrets) != 0 {
		t.Fatal("clear failed")
	}
	request := httptest.NewRequest("POST", "http://localhost/api/v1/plugins/jira/credential", strings.NewReader(`{"secret":"stolen"}`))
	result := httptest.NewRecorder()
	s.Handler().ServeHTTP(result, request)
	if result.Code != 401 {
		t.Fatal("credential endpoint unauthenticated", result.Code)
	}
}

func TestJiraWASIBridgeConnectionAndPersistence(t *testing.T) {
	s := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jira-private-token" {
			t.Error("missing token")
		}
		switch r.URL.Path {
		case "/jira/rest/api/2/myself":
			fmt.Fprint(w, `{"displayName":"Jira Reader","name":"reader"}`)
		case "/jira/rest/api/2/search":
			fmt.Fprint(w, `{"issues":[{"key":"APP-1","fields":{"summary":"Read me"}}],"total":1}`)
		case "/jira/rest/api/2/issue/APP-1":
			fmt.Fprint(w, `{"key":"APP-1","fields":{"customfield_1":"custom"}}`)
		case "/jira/rest/api/2/issue/APP-1/comment":
			fmt.Fprint(w, `{"comments":[{"body":"discussion"}],"total":1}`)
		default:
			t.Error(r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	configureJira(t, s, upstream.URL+"/jira")
	response := scmAPI(t, s, "POST", "/plugins/jira/test", map[string]any{})
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Jira Reader") {
		t.Fatal(response.Code, response.Body.String())
	}
	bot, _ := s.Bot("echo")
	for tool, args := range map[string]string{"jira_search": `{"jql":"project = APP"}`, "jira_get_issue": `{"issue":"APP-1"}`, "jira_get_comments": `{"issue":"APP-1"}`} {
		result, err := s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: bot}, tool, json.RawMessage(args))
		if err != nil {
			t.Fatal(tool, err)
		}
		b, _ := json.Marshal(result)
		if strings.Contains(string(b), "jira-private-token") {
			t.Fatal("token escaped WASI")
		}
	}
	bot.Plugins = map[string]bool{"jira": false}
	if _, err := s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: bot}, "jira_search", json.RawMessage(`{"jql":"project=APP"}`)); err == nil {
		t.Fatal("disabled bot invoked Jira")
	}
	if _, err := s.jiraOperation(context.Background(), domain.ExecutionScope{PluginID: "other"}, domain.PluginManifest{Builtin: true}, "jira.search", nil); err == nil {
		t.Fatal("foreign plugin got Jira credentials")
	}
	// Recreate the plugin runtime against persisted records, as startup does.
	guest, _ := os.ReadFile("../plugins/guest/builtin.wasm")
	s.Plugins.Close(context.Background())
	var err error
	s.Plugins, err = plugins.New(context.Background(), s.Store, filepath.Join(s.Dir, "plugins"), guest, s.pluginCall)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.Plugins.Get("jira")
	if !m.Enabled || text(m.Config, "url") != upstream.URL+"/jira" {
		t.Fatal("settings did not persist", m)
	}
	if _, err := s.jiraClient(context.Background(), m); err != nil {
		t.Fatal("credential reference did not persist", err)
	}
}

func TestJiraDisableAndSettingsCancelRequests(t *testing.T) {
	for _, action := range []string{"stop", "disable", "settings"} {
		t.Run(action, func(t *testing.T) {
			s := testServer(t)
			started := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
			}))
			defer upstream.Close()
			configureJira(t, s, upstream.URL)
			bot, _ := s.Bot("echo")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := s.Plugins.Invoke(ctx, domain.ExecutionScope{Bot: bot}, "jira_search", json.RawMessage(`{"jql":"project=APP"}`))
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("Jira request did not start")
			}
			if action == "stop" {
				cancel()
			} else {
				m, _ := s.Plugins.Get("jira")
				if action == "disable" {
					m.Enabled = false
				} else {
					m.Config["url"] = upstream.URL + "/jira"
				}
				if err := s.Plugins.Save(m); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("request was not cancelled")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Jira request did not stop")
			}
		})
	}
}

func TestJiraBotSearchAndReadToolLoop(t *testing.T) {
	s := testServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/2/search":
			fmt.Fprint(w, `{"issues":[{"key":"APP-123","fields":{"summary":"A ticket"}}],"total":1}`)
		case "/rest/api/2/issue/APP-123":
			fmt.Fprint(w, `{"key":"APP-123","fields":{"description":"Full ticket details"}}`)
		case "/rest/api/2/issue/APP-123/comment":
			fmt.Fprint(w, `{"comments":[{"body":"Full discussion"}],"total":1}`)
		default:
			t.Error(r.URL)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	configureJira(t, s, upstream.URL)
	round := 0
	generation := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if strings.Contains(string(data), "jira-private-token") {
			t.Error("Jira token entered model context")
		}
		switch round {
		case 0:
			if !strings.Contains(string(data), "jira_search") || !strings.Contains(string(data), "jira_get_issue") {
				t.Error("Jira tools unavailable to bot")
			}
			stream(w, "", []domain.ToolCall{{ID: "search", Type: "function", Function: domain.FunctionCall{Name: "jira_search", Arguments: `{"jql":"project = APP"}`}}})
		case 1:
			if !strings.Contains(string(data), "APP-123") {
				t.Error("search result missing")
			}
			stream(w, "", []domain.ToolCall{{ID: "read", Type: "function", Function: domain.FunctionCall{Name: "jira_get_issue", Arguments: `{"issue":"APP-123"}`}}})
		default:
			if !strings.Contains(string(data), "Full ticket details") || !strings.Contains(string(data), "Full discussion") {
				t.Error("ticket or discussion missing")
			}
			stream(w, "Read APP-123 and its full discussion.", nil)
		}
		round++
	})
	conversation := chat(t, s, generation)
	dispatch, err := s.send(conversation.ID, SendRequest{Content: "Search Jira and read APP-123", Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, dispatch)
	messages, err := s.messages(conversation.ID)
	if err != nil || round != 3 || messages[len(messages)-1].Content != "Read APP-123 and its full discussion." {
		t.Fatal(round, messages, err)
	}
}
