package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func textCommand(name string) domain.SlashCommand {
	return domain.SlashCommand{Name: name, Action: domain.SlashCommandAction{Type: "text", Text: "hello"}}
}

func TestSlashCommandsPersistenceValidationAndIsolation(t *testing.T) {
	s := testServer(t)
	commands, err := s.slashCommands()
	if err != nil || len(commands) != 1 || commands[0].Action.Text != domain.PlanCommandText {
		t.Fatal(commands, err)
	}
	custom, err := s.saveSlashCommand(textCommand("Review"), "")
	if err != nil || custom.Name != "review" || custom.Builtin || !custom.Enabled || custom.Action.Position != "insert" {
		t.Fatal(custom, err)
	}
	for _, name := range []string{"REVIEW", "PLAN", "bad name", "/bad", "1bad", "bad/path"} {
		if _, err := s.saveSlashCommand(textCommand(name), ""); err == nil {
			t.Fatal("accepted invalid/reserved name", name)
		}
	}
	if _, err := s.saveSlashCommand(textCommand("plan"), "plan"); err == nil {
		t.Fatal("edited builtin")
	}
	if err := s.removeSlashCommand("plan", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.saveSlashCommand(textCommand("plan"), ""); err == nil {
		t.Fatal("removed builtin name not reserved")
	}
	other := testServer(t)
	otherCommands, _ := other.slashCommands()
	if len(otherCommands) != 1 || !otherCommands[0].Enabled {
		t.Fatal("commands leaked to another instance")
	}
	dir := s.Dir
	s.Close()
	guest, _ := os.ReadFile("../plugins/guest/builtin.wasm")
	reopened, err := New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	commands, err = reopened.slashCommands()
	if err != nil || len(commands) != 2 || commands[0].Enabled || commands[1].ID != custom.ID {
		t.Fatal("not persisted", commands, err)
	}
	if err := reopened.removeSlashCommand("plan", true); err != nil {
		t.Fatal(err)
	}
	custom.Action.Text = "changed"
	if _, err := reopened.saveSlashCommand(custom, custom.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.removeSlashCommand(custom.ID, false); err != nil {
		t.Fatal(err)
	}
	commands, _ = reopened.slashCommands()
	if len(commands) != 1 || !commands[0].Enabled {
		t.Fatal(commands)
	}
}

func TestSlashCommandHTTPAndPluginExecution(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	owner, second := remoteLogin(t, s), remoteLogin(t, s)
	for _, path := range []string{"/slash-commands", "/slash-commands/catalog"} {
		if w := remoteRequest(s, "GET", path, "", nil); w.Code != 401 {
			t.Fatal("unauthenticated read", w.Code)
		}
	}
	command := domain.SlashCommand{Name: "schedules", Action: domain.SlashCommandAction{Type: "plugin", PluginID: "calendar", Tool: "schedule_list", Arguments: json.RawMessage(`{}`)}}
	raw, _ := json.Marshal(command)
	if w := remoteRequest(s, "POST", "/slash-commands", string(raw), nil); w.Code != 401 {
		t.Fatal("unauthenticated write", w.Code)
	}
	w := remoteRequest(s, "POST", "/slash-commands", string(raw), owner)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &command) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	w = remoteRequest(s, "GET", "/state", "", second)
	if !strings.Contains(w.Body.String(), `"name":"schedules"`) {
		t.Fatal("not shared with second browser", w.Body.String())
	}
	path := "/slash-commands/" + command.ID + "/invoke"
	if w := remoteRequest(s, "POST", path, `{"botId":"echo"}`, nil); w.Code != 401 {
		t.Fatal("unauthenticated invocation")
	}
	manifest, _ := s.Plugins.Get("calendar")
	input, _ := json.Marshal(map[string]string{"botId": "echo", "digest": manifest.Digest})
	if w := remoteRequest(s, "POST", path, string(input), owner); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("runtime invocation failed", w.Code, w.Body.String())
	}
	if w := remoteRequest(s, "POST", path, `{"botId":"echo","digest":"stale"}`, owner); w.Code != 400 {
		t.Fatal("stale package executed")
	}
	if w := remoteRequest(s, "POST", path, `{"botId":"missing"}`, owner); w.Code != 400 {
		t.Fatal("missing selected bot accepted")
	}
	command.Action.BotID = "echo"
	if _, err := s.saveSlashCommand(command, command.ID); err != nil {
		t.Fatal(err)
	}
	fixedInput, _ := json.Marshal(map[string]string{"botId": "missing", "digest": manifest.Digest})
	if w := remoteRequest(s, "POST", path, string(fixedInput), owner); w.Code != 200 {
		t.Fatal("fixed bot did not override selected bot", w.Body.String())
	}
	echo, _ := s.Bot("echo")
	if _, err := s.Plugins.InvokePlugin(context.Background(), domain.ExecutionScope{Bot: echo, Standalone: true}, "calendar", "web_get", json.RawMessage(`{"url":"https://example.com"}`)); err == nil {
		t.Fatal("invoked a tool from another plugin")
	}
	echo.Plugins["calendar"] = false
	s.Store.Put("bots", echo.ID, echo)
	if w := remoteRequest(s, "POST", path, string(input), owner); w.Code != 400 {
		t.Fatal("bot permissions bypassed")
	}
	if w := remoteRequest(s, "DELETE", "/slash-commands/"+command.ID, "", owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := remoteRequest(s, "POST", path, string(input), owner); w.Code != 400 {
		t.Fatal("deleted command executed")
	}
}

func TestSlashCommandUnavailableActionsAndFeatures(t *testing.T) {
	s := testServer(t)
	for _, action := range []domain.SlashCommandAction{
		{Type: "plugin", PluginID: "comfyui", Tool: "comfyui_generate", Arguments: json.RawMessage(`{"prompt":"test"}`)},
		{Type: "plugin", PluginID: "web-get", Tool: "web_get", Arguments: json.RawMessage(`{}`)},
		{Type: "plugin", PluginID: "calendar", Tool: "web_get", Arguments: json.RawMessage(`{}`)},
		{Type: "app", PluginID: "monaco-editor", ViewID: "main", Feature: "missing"},
		{Type: "text", Text: "text", Position: "invalid"},
	} {
		if _, err := s.saveSlashCommand(domain.SlashCommand{Name: "bad", Action: action}, ""); err == nil {
			t.Fatal("invalid action accepted", action)
		}
	}
	if _, err := s.saveSlashCommand(domain.SlashCommand{Name: "scm", Action: domain.SlashCommandAction{Type: "app", PluginID: "monaco-editor", ViewID: "main", Feature: "source-control"}}, ""); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.slashCommandCatalog()
	if err != nil {
		t.Fatal(err)
	}
	blocked := false
	for _, plugin := range catalog {
		if plugin.ID == "comfyui" {
			for _, tool := range plugin.Tools {
				blocked = blocked || tool.Unavailable != ""
			}
		}
	}
	if !blocked {
		t.Fatal("media tools did not explain their requirements")
	}
	echo, _ := s.Bot("echo")
	if _, err := s.pluginCall(context.Background(), domain.ExecutionScope{Bot: echo, PluginID: "comfyui", Standalone: true}, "comfyui.generate", json.RawMessage(`{"prompt":"test"}`)); err == nil || !strings.Contains(err.Error(), "saved chat") {
		t.Fatal("guest bypassed standalone boundary", err)
	}
}

func TestSlashLegacyCommandsAreLiteralMessages(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	owner := remoteLogin(t, s)
	received := make(chan struct{}, 2)
	generation := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		stream(w, "Literal text received.", nil)
	})
	for _, content := range []string{"/approve missing digest", "/missed skip-all"} {
		c := chat(t, s, generation)
		raw, _ := json.Marshal(SendRequest{Content: content, Generation: generation, RequestID: domain.ID()})
		w := remoteRequest(s, "POST", "/chats/"+c.ID+"/messages", string(raw), owner)
		if w.Code != 200 || strings.Contains(w.Body.String(), "ownerCommand") {
			t.Fatal("legacy command intercepted", w.Code, w.Body.String())
		}
		select {
		case <-received:
		case <-time.After(10 * time.Second):
			messages, _ := s.Store.Messages(c.ID)
			runs, _ := s.Store.List("runs")
			t.Fatalf("literal message did not reach model: %+v; runs: %s", messages, runs)
		}
	}
}
