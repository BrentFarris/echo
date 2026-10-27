package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/brent/echo/internal/credentials"
	"github.com/brent/echo/internal/domain"
)

type scmSecrets map[string]string

func (s scmSecrets) Available(context.Context) bool { return true }
func (s scmSecrets) Get(_ context.Context, key string) (string, error) {
	v, ok := s[key]
	if !ok {
		return "", credentials.ErrSecretNotFound
	}
	return v, nil
}
func (s scmSecrets) Set(_ context.Context, key, value string) error { s[key] = value; return nil }
func (s scmSecrets) Delete(_ context.Context, key string) error     { delete(s, key); return nil }

func TestSourceControlContractsAndSettings(t *testing.T) {
	s := testServer(t)
	echo, _ := s.Bot("echo")
	shell, _ := s.Plugins.Get("shell")
	shell.Enabled = false
	s.Plugins.Save(shell)
	for _, id := range []string{"git", "fossil", "p4"} {
		m, e := s.Plugins.Get(id)
		if e != nil || !m.Builtin || !m.Enabled || m.Scope != "bot" {
			t.Fatal(id, m, e)
		}
		count := 0
		for _, tool := range s.Plugins.Tools(echo) {
			if strings.HasPrefix(tool.Name, id+"_") {
				count++
			}
		}
		if count != 3 {
			t.Fatal(id, "tools depend on Shell", count)
		}
		disabled := echo
		disabled.Plugins = map[string]bool{id: false}
		if _, e = s.Plugins.Invoke(context.Background(), domain.ExecutionScope{Bot: disabled}, id+"_command", json.RawMessage(`{"command":"version"}`)); e == nil {
			t.Fatal("disabled tool ran", id)
		}
		for _, grant := range []map[string]any{{"internet": true}, {"privateNetwork": true}, {"executable": `C:\Tools\client.exe`}, {"toolchains": []string{t.TempDir()}}} {
			if _, e = s.sourceControlConfig(m, grant, false); e == nil {
				t.Fatal("model expanded grants", id, grant)
			}
		}
		config, e := s.sourceControlConfig(m, map[string]any{"internet": true}, true)
		if e != nil || config["internet"] != true {
			t.Fatal(e, config)
		}
		m.Config = config
		if _, e = s.sourceControlConfig(m, map[string]any{"internet": false}, false); e != nil {
			t.Fatal("model cannot narrow grant", e)
		}
	}
}

func TestSourceControlLiteralArgumentsAndSecretFilter(t *testing.T) {
	literal := `a file "quoted" ; $(bad) & | C:\tail\`
	args, _, e := scmArguments("git", "add", []string{"--", literal}, nil)
	if e != nil || !reflect.DeepEqual(args, []string{"--no-pager", "add", "--", literal}) {
		t.Fatal(args, e)
	}
	if _, _, e = scmArguments("git", "status;evil", nil, nil); e == nil {
		t.Fatal("accepted shell command")
	}
	args, env, e := scmArguments("p4", "client", []string{"-i"}, map[string]any{"port": "ssl:server:1666", "user": "artist", "client": "game"})
	if e != nil || !reflect.DeepEqual(args, []string{"-p", "ssl:server:1666", "-u", "artist", "-c", "game", "client", "-i"}) || env["P4PASSWD"] == "" {
		t.Fatal(args, env, e)
	}
	for _, command := range []string{"-p", "login", "tickets", "set", "passwd", "clone", "init", "fetch", "push", "remote"} {
		if _, _, e = scmArguments("p4", command, nil, nil); e == nil {
			t.Fatal("credential bypass", command)
		}
	}
	if _, _, e = scmArguments("p4", "info", nil, map[string]any{"port": "rsh:arbitrary command"}); e == nil {
		t.Fatal("shell transport allowed")
	}
	filter := &secretFilter{secret: "abc-secret-xyz"}
	out := ""
	for _, part := range []string{"safe abc-", "secret-x", "yz done abc-se"} {
		out += filter.push(part, false)
	}
	out += filter.push("", true)
	if out != "safe [redacted] done [redacted]" {
		t.Fatal(out)
	}
}

func scmAPI(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	session := httptest.NewRequest("POST", "http://localhost/api/v1/session", strings.NewReader(`{}`))
	session.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler := s.Handler()
	handler.ServeHTTP(rr, session)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	data, _ := json.Marshal(body)
	request := httptest.NewRequest(method, "http://localhost/api/v1"+path, strings.NewReader(string(data)))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Content-Type", "application/json")
	for _, cookie := range rr.Result().Cookies() {
		request.AddCookie(cookie)
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}

func TestP4CredentialOwnerAPIAndDestinationBinding(t *testing.T) {
	s := testServer(t)
	secrets := scmSecrets{}
	s.Secrets = secrets
	config := map[string]any{"port": "ssl:server:1666", "user": "artist", "client": "game"}
	r := scmAPI(t, s, "PATCH", "/plugins/p4", map[string]any{"enabled": true, "config": config})
	if r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	r = scmAPI(t, s, "POST", "/plugins/p4/credential", map[string]any{"secret": "fixture-ticket"})
	if r.Code != 200 || strings.Contains(r.Body.String(), "fixture-ticket") {
		t.Fatal(r.Body.String())
	}
	m, _ := s.Plugins.Get("p4")
	ref := text(m.Config, "credential")
	if secrets[ref] != "fixture-ticket" {
		t.Fatal("secret was not protected")
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "fixture-ticket") {
		t.Fatal("credential exposed in config")
	}
	if _, e := s.sourceControlConfig(m, map[string]any{"port": "attacker:1666"}, false); e == nil {
		t.Fatal("model redirected credential")
	}
	config["client"] = "game2"
	r = scmAPI(t, s, "PATCH", "/plugins/p4", map[string]any{"enabled": true, "config": config})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	m, _ = s.Plugins.Get("p4")
	if text(m.Config, "credential") != ref {
		t.Fatal("client change lost credential")
	}
	config["port"] = "other:1666"
	r = scmAPI(t, s, "PATCH", "/plugins/p4", map[string]any{"enabled": true, "config": config})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	m, _ = s.Plugins.Get("p4")
	if text(m.Config, "credential") != "" || len(secrets) != 0 {
		t.Fatal("credential survived destination change")
	}
}
