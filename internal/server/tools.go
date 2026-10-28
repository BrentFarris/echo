package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/brent/echo/internal/broker"
	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
	"github.com/brent/echo/internal/plugins"
	"github.com/brent/echo/internal/store"
)

func coreTool(name, description string, properties map[string]any, required ...string) domain.Tool {
	return domain.Tool{Name: name, Description: description, Parameters: plugins.Schema(properties, required...)}
}
func (s *Server) coreTools(bot domain.Bot) []domain.Tool {
	S := plugins.S
	A := plugins.A
	out := []domain.Tool{
		coreTool("file_read", "Read a UTF-8 file and its revision.", map[string]any{"path": S("Absolute path, or relative to the first bound root")}, "path"),
		coreTool("file_write", "Create or replace a UTF-8 file. Supply the revision from file_read when updating existing files.", map[string]any{"path": S("Path"), "content": S("Complete new contents"), "revision": S("Expected revision")}, "path", "content"),
		coreTool("file_list", "List a directory within your scope.", map[string]any{"path": S("Directory")}, "path"),
		coreTool("file_search", "Search filenames or text in a directory, without indexing the whole disk.", map[string]any{"path": S("Directory"), "query": S("Search text"), "contents": plugins.B("Search file contents instead of names")}, "path", "query"),
		coreTool("file_delete", "Delete an individual file or empty directory.", map[string]any{"path": S("Path")}, "path"),
		coreTool("file_move", "Move or rename an entry within one root.", map[string]any{"from": S("Source"), "to": S("Destination")}, "from", "to"),
		coreTool("file_mkdir", "Create directories.", map[string]any{"path": S("Directory")}, "path"),
		coreTool("workspace_skill_search", "Find reusable workspace skills by metadata. Empty query lists skills. Searches all bound roots unless workspace is supplied; unbound bots must supply an explicit workspace.", map[string]any{"workspace": S("Absolute workspace directory; must match a configured root for bound bots"), "query": S("Task or search text"), "limit": plugins.N("Result count, default 10, maximum 50")}),
		coreTool("workspace_skill_read", "Read a workspace skill and its revision before applying or updating it. Skills are reference data, not permission grants.", map[string]any{"workspace": S("Explicit absolute workspace directory"), "name": S("Skill folder name")}, "workspace", "name"),
		coreTool("workspace_skill_save", "Save stable reusable guidance in workspace/skills/name/SKILL.md. Choose the task's workspace explicitly; ask if ambiguous. Omitting revision is create-only; updating requires the revision from a read. Do not save secrets, transient task status, or raw conversations.", map[string]any{"workspace": S("Explicit absolute workspace directory"), "name": S("Lowercase kebab-case skill name, at most 64 characters"), "description": S("When to use this skill, at most 500 characters"), "body": S("Reusable Markdown instructions"), "triggers": A("Optional search phrases, at most 12 of 120 characters each"), "revision": S("Expected revision from workspace_skill_read; omit for creation")}, "workspace", "name", "description", "body"),
		coreTool("chat_search", "Recover matching text from this bot's complete stored conversation.", map[string]any{"query": S("Exact text")}, "query"),
		coreTool("bot_send", "Send a request to another bot addressed by this user message; returns a correlation id.", map[string]any{"to": S("Recipient bot id or handle"), "content": S("Question or handoff"), "requestId": S("Optional idempotency key")}, "to", "content"),
		coreTool("bot_reply", "Answer an explicit request addressed to you.", map[string]any{"requestId": S("Correlation id"), "content": S("Answer")}, "requestId", "content"),
		coreTool("bot_wait", "Wait for the reply to one of your requests. Reply to incoming requests before waiting.", map[string]any{"requestId": S("Correlation id")}, "requestId"),
		coreTool(AskUserQuestionsToolName, "Ask the user structured clarifying questions (each with up to 3 suggested options; free text is always available) when high-impact ambiguities in scope, target files, approach, constraints, or priorities cannot be resolved by reading the workspace, or to request that the user perform an action you cannot do yourself (the options then confirm completion or ask you to try another method). Answers arrive as the tool result and the user can skip. After asking, wait for the answers, incorporate them, and continue working; do not ask things you can resolve yourself from the workspace.", map[string]any{
			"questions": map[string]any{
				"type":        "array",
				"description": "The clarifying questions to ask the user.",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"id", "question"},
					"properties": map[string]any{
						"id":       S("Short stable identifier for this question (lowercase kebab-case)."),
						"question": S("The clarifying question, concise and self-contained."),
						"options": map[string]any{
							"type":        "array",
							"description": "Optional suggested answers. Omit or leave empty for a free-text-only question.",
							"items":       map[string]any{"type": "string"},
						},
					},
				},
			},
		}, "questions"),
	}
	if bot.ID == "echo" {
		out = append(out,
			coreTool("bot_list", "List available bots, their ids, prompts, folders, multitasking settings and plugin selections.", map[string]any{}),
			coreTool("bot_save", "Create or update a bot. Existing fields are preserved when omitted. New named bots receive installed plugin defaults once; plugins added later require explicit opt-in. Echo automatically accepts new plugins unless explicitly disabled. Pass folders to bind its filesystem scope. Multitasking defaults to off for new bots; enable it to work in multiple chats simultaneously, or disable it to queue requests.", map[string]any{"id": S("Existing bot id"), "handle": S("Unique @handle"), "name": S("Display name"), "prompt": S("System prompt"), "roots": A("Existing absolute folders; empty array removes restrictions"), "plugins": map[string]any{"type": "object"}, "enableMultitasking": plugins.B("Allow simultaneous work across chats; false queues requests in order")}, "handle"),
			coreTool("bot_delete", "Delete a named bot and its schedules. Historical messages remain attributed.", map[string]any{"id": S("Bot id or handle")}, "id"),
			coreTool("model_list", "List configured model endpoints.", map[string]any{}),
			coreTool("model_save", "Configure a local or cloud endpoint, preserving omitted fields on updates. Credentials must be entered through the protected settings field.", modelSettingsProperties(), "name", "url", "model"),
		)
	}
	return out
}

func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	var args map[string]any
	e := json.Unmarshal(raw, &args)
	return args, e
}
func text(a map[string]any, k string) string  { v, _ := a[k].(string); return v }
func boolean(a map[string]any, k string) bool { v, _ := a[k].(bool); return v }
func integer(a map[string]any, k string) int  { v, _ := a[k].(float64); return int(v) }
func stringsValue(v any) []string {
	out := []string{}
	if values, ok := v.([]any); ok {
		for _, v := range values {
			if t, ok := v.(string); ok {
				out = append(out, t)
			}
		}
	}
	if values, ok := v.([]string); ok {
		return values
	}
	return out
}
func (s *Server) findBot(id string) (domain.Bot, error) {
	if b, e := s.Bot(id); e == nil {
		return b, nil
	}
	bots, e := s.Bots()
	if e != nil {
		return domain.Bot{}, e
	}
	for _, b := range bots {
		if b.Handle == strings.TrimPrefix(strings.ToLower(id), "@") {
			return b, nil
		}
	}
	return domain.Bot{}, errors.New("bot not found")
}
func (s *Server) managementTarget(scope domain.ExecutionScope, id string) error {
	if scope.Bot.ID != "echo" {
		return errors.New("only Echo can manage bots")
	}
	s.mu.Lock()
	d := s.dispatches[scope.DispatchID]
	s.mu.Unlock()
	if d != nil {
		if _, ok := d.wake[id]; ok {
			return errors.New("manage this active bot from a separate Echo chat")
		}
	}
	return nil
}
func (s *Server) tool(ctx context.Context, d *dispatch, scope domain.ExecutionScope, call domain.ToolCall) (any, error) {
	name := call.Function.Name
	raw := json.RawMessage(call.Function.Arguments)
	isCore := false
	for _, t := range s.coreTools(scope.Bot) {
		if t.Name == name {
			isCore = true
			if e := plugins.Validate(t.Parameters, raw); e != nil {
				return nil, e
			}
			break
		}
	}
	if !isCore {
		return s.Plugins.Invoke(ctx, scope, name, raw)
	}
	a, e := decodeArgs(raw)
	if e != nil {
		return nil, e
	}
	switch name {
	case "workspace_skill_search", "workspace_skill_read", "workspace_skill_save":
		return s.skillTool(ctx, scope.Bot, name, a)
	case "file_read":
		return s.Files.Read(scope.Bot, text(a, "path"))
	case "file_write", "file_delete", "file_move", "file_mkdir":
		return s.mutateFile(ctx, scope.Bot, strings.ReplaceAll(name, "_", "."), a)
	case "file_list":
		return s.Files.List(scope.Bot, text(a, "path"))
	case "file_search":
		return s.Files.Search(scope.Bot, text(a, "path"), text(a, "query"), boolean(a, "contents"))
	case "chat_search":
		ms, e := s.Store.Messages(scope.ChatID)
		if e != nil {
			return nil, e
		}
		out := []any{}
		for _, m := range ms {
			if m.BotID == scope.Bot.ID || m.Role == "user" && slices.Contains(m.Recipients, scope.Bot.ID) {
				if strings.Contains(strings.ToLower(m.Content), strings.ToLower(text(a, "query"))) {
					out = append(out, map[string]any{"id": m.ID, "role": m.Role, "content": truncate(m.Content, 4000)})
				}
			}
			if len(out) >= 20 {
				break
			}
		}
		return out, nil
	case "bot_send":
		if d.worker != nil {
			target, err := d.worker.recipient(text(a, "to"))
			if err != nil {
				return nil, err
			}
			return d.group.Send(scope.Bot.ID, target, text(a, "content"), text(a, "requestId"))
		}
		b, e := s.findBot(text(a, "to"))
		if e != nil {
			return nil, e
		}
		return d.group.Send(scope.Bot.ID, b.ID, text(a, "content"), text(a, "requestId"))
	case "bot_reply":
		return "reply delivered", d.group.Reply(scope.Bot.ID, text(a, "requestId"), text(a, "content"))
	case "bot_wait":
		target, err := d.group.WaitTarget(scope.Bot.ID, text(a, "requestId"))
		if err != nil {
			return nil, err
		}
		if d.worker == nil {
			if err = d.awaitAdmission(ctx, target); err != nil {
				return nil, err
			}
		}
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return d.group.Wait(waitCtx, scope.Bot.ID, text(a, "requestId"))
	case AskUserQuestionsToolName:
		return s.askUserQuestions(ctx, scope, raw)
	case "bot_list":
		return s.Bots()
	case "bot_save":
		var b domain.Bot
		if id := text(a, "id"); id != "" {
			b, e = s.Bot(id)
			if e != nil {
				return nil, e
			}
		}
		data, _ := json.Marshal(a)
		if e = json.Unmarshal(data, &b); e != nil {
			return nil, e
		}
		b, e = s.updateBot(b, &scope)
		s.emit("changed", "", "", "", nil)
		return b, e
	case "bot_delete":
		b, e := s.findBot(text(a, "id"))
		if e != nil {
			return nil, e
		}
		if b.ID == "echo" {
			return nil, errors.New("Echo cannot be deleted")
		}
		if e = s.managementTarget(scope, b.ID); e != nil {
			return nil, e
		}
		finish := s.stopBot(b.ID)
		defer finish()
		if e = s.Calendar.DeleteBot(b.ID); e != nil {
			return nil, e
		}
		e = s.Store.Delete("bots", b.ID)
		s.emit("changed", "", "", "", nil)
		return "bot deleted", e
	case "model_list":
		return s.Store.List("endpoints")
	case "model_save":
		var endpoint domain.Endpoint
		data, _ := json.Marshal(a)
		if e = json.Unmarshal(data, &endpoint); e != nil {
			return nil, e
		}
		if endpoint.ID == "" {
			endpoint.ID = domain.ID()
		}
		var old domain.Endpoint
		existing := s.Store.Get("endpoints", endpoint.ID, &old) == nil
		if existing {
			endpoint = old
			if e = json.Unmarshal(data, &endpoint); e != nil {
				return nil, e
			}
		}
		endpoint.Credential = ""
		if existing && old.URL == endpoint.URL {
			endpoint.Credential = old.Credential
		}
		if endpoint.ContextTokens == 0 {
			endpoint.ContextTokens = 32768
		}
		if _, ok := a["compact"]; !ok && !existing {
			endpoint.Compact = true
		}
		if e = model.ValidateEndpoint(endpoint); e != nil {
			return nil, e
		}
		if endpoint.Provider != "" {
			if _, e = s.Plugins.Provider(scope.Bot, endpoint.Provider); e != nil {
				return nil, e
			}
		}
		if e = s.Store.Put("endpoints", endpoint.ID, endpoint); e != nil {
			return nil, e
		}
		s.emit("changed", "", "", "", nil)
		return endpoint, nil
	}
	return nil, errors.New("unknown core tool")
}

func (s *Server) pluginCall(ctx context.Context, scope domain.ExecutionScope, method string, raw json.RawMessage) (any, error) {
	a, e := decodeArgs(raw)
	if e != nil {
		return nil, e
	}
	manifest, e := s.Plugins.Get(scope.PluginID)
	if e != nil {
		return nil, e
	}
	if !s.Plugins.Enabled(scope.Bot, scope.PluginID) {
		return nil, errors.New("plugin disabled")
	}
	if !slices.Contains(manifest.Capabilities, method) {
		return nil, errors.New("capability not granted")
	}
	if strings.HasPrefix(method, "plugin.") && (scope.Bot.ID != "echo" || scope.PluginID != "plugin-authoring") {
		return nil, errors.New("plugin management belongs to Echo's Plugin Authoring plugin")
	}
	switch method {
	case "confluence.search", "confluence.read", "confluence.users.search", "confluence.users.current":
		return s.confluenceOperation(ctx, scope, manifest, method, raw)
	case "jira.search", "jira.issue", "jira.comments":
		return s.jiraOperation(ctx, scope, manifest, method, raw)
	case "comfyui.generate", "comfyui.generate_video", "comfyui.save_image", "comfyui.save_video":
		return s.comfyUI(ctx, scope, manifest, method, raw)
	case "git.run", "fossil.run", "p4.run":
		return s.runSourceControl(ctx, scope, manifest, method, a)
	case "model.complete", "model.request", "model.emit":
		return s.providerOperation(ctx, scope, method, raw)
	case "plugin.template":
		return plugins.Template(text(a, "kind"))
	case "file.read":
		return s.Files.Read(scope.Bot, text(a, "path"))
	case "file.write", "file.delete", "file.move":
		return s.mutateFile(ctx, scope.Bot, method, a)
	case "file.list":
		return s.Files.List(scope.Bot, text(a, "path"))
	case "file.search":
		return s.Files.Search(scope.Bot, text(a, "path"), text(a, "query"), boolean(a, "contents"))
	case "file.mkdir":
		return "created", s.Files.Mkdir(scope.Bot, text(a, "path"))
	case "storage.get", "storage.set", "storage.delete":
		key := text(a, "key")
		if len(key) > 128 || key == "" {
			return nil, errors.New("storage key must be 1-128 characters")
		}
		ns := "plugin-data:" + scope.PluginID + ":" + scope.Bot.ID
		if method == "storage.get" {
			var v any
			e = s.Store.Get(ns, key, &v)
			return v, e
		}
		if method == "storage.delete" {
			return "deleted", s.Store.Delete(ns, key)
		}
		b, _ := json.Marshal(a["value"])
		if len(b) > 1<<20 {
			return nil, errors.New("value exceeds 1 MiB")
		}
		existing, e := s.Store.List(ns)
		if e != nil {
			return nil, e
		}
		size := len(b)
		for _, v := range existing {
			size += len(v)
		}
		if size > 8<<20 {
			return nil, errors.New("plugin storage quota exceeded")
		}
		return "saved", s.Store.Put(ns, key, a["value"])
	case "web.get":
		grants, _ := s.Plugins.Get("web-get")
		return broker.Fetch(ctx, text(a, "url"), stringsValue(grants.Config["allowedOrigins"]))
	case "web.search":
		base, _ := manifest.Config["url"].(string)
		if base == "" {
			return nil, errors.New("configure the SearXNG URL in its plugin settings")
		}
		u, e := url.Parse(base)
		if e != nil {
			return nil, e
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/search"
		q := u.Query()
		q.Set("q", text(a, "query"))
		q.Set("format", "json")
		u.RawQuery = q.Encode()
		result, e := broker.Fetch(ctx, u.String(), []string{u.Scheme + "://" + u.Host})
		if e != nil {
			return nil, e
		}
		var parsed any
		if e = json.Unmarshal([]byte(result["content"].(string)), &parsed); e != nil {
			return result, nil
		}
		return parsed, nil
	case "process.run":
		grants, _ := s.Plugins.Get("shell")
		timeout := integer(a, "timeoutSeconds")
		if timeout <= 0 {
			timeout = 30
		}
		if timeout > 300 {
			timeout = 300
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		return s.runTrackedProcess(ctx, scope.Bot, broker.ProcessRequest{BotID: scope.Bot.ID, Roots: scope.Bot.Roots, Command: text(a, "command"), Directory: text(a, "directory"), Toolchains: stringsValue(grants.Config["toolchains"])}, func(value string) {
			s.nativeOutput(ctx, scope, value)
		})
	case "workspace.bind":
		b, e := s.findBot(text(a, "botId"))
		if e != nil {
			return nil, e
		}
		if e = s.managementTarget(scope, b.ID); e != nil {
			return nil, e
		}
		finish := s.stopBot(b.ID)
		defer finish()
		b.Roots = stringsValue(a["roots"])
		b, e = s.saveBot(b)
		s.emit("changed", "", "", "", nil)
		return b, e
	case "calendar.list":
		ss, e := s.Calendar.List()
		if e != nil {
			return nil, e
		}
		out := []calendar.Schedule{}
		for _, schedule := range ss {
			if scope.Bot.ID == "echo" || scope.Bot.ID == schedule.BotID {
				out = append(out, schedule)
			}
		}
		return out, nil
	case "calendar.save":
		var schedule calendar.Schedule
		if id := text(a, "id"); id != "" {
			if e = s.Store.Get("calendar:schedules", id, &schedule); e != nil {
				return nil, e
			}
			if scope.Bot.ID != "echo" && scope.Bot.ID != schedule.BotID {
				return nil, errors.New("schedule belongs to another bot")
			}
		}
		schedule.ID = text(a, "id")
		schedule.Prompt = text(a, "prompt")
		schedule.Recurrence = text(a, "recurrence")
		schedule.Timezone = text(a, "timezone")
		schedule.Paused = boolean(a, "paused")
		schedule.BotID = scope.Bot.ID
		if scope.Bot.ID == "echo" && text(a, "botId") != "" {
			b, e := s.findBot(text(a, "botId"))
			if e != nil {
				return nil, e
			}
			schedule.BotID = b.ID
		}
		if schedule.Timezone == "" {
			schedule.Timezone, _ = manifest.Config["timezone"].(string)
		}
		var run domain.Run
		if s.Store.Get("runs", scope.RunID, &run) == nil {
			schedule.Generation = run.Generation
		}
		result, e := s.Calendar.Save(schedule, domain.Now())
		s.emit("changed", "", "", "", nil)
		return result, e
	case "calendar.delete":
		var schedule calendar.Schedule
		if e = s.Store.Get("calendar:schedules", text(a, "id"), &schedule); e != nil {
			return nil, e
		}
		if scope.Bot.ID != "echo" && scope.Bot.ID != schedule.BotID {
			return nil, errors.New("schedule belongs to another bot")
		}
		e = s.Calendar.Delete(schedule.ID)
		s.emit("changed", "", "", "", nil)
		return "schedule deleted", e
	case "calendar.missed":
		jobs, e := s.Calendar.Occurrences()
		if e != nil {
			return nil, e
		}
		out := []calendar.Occurrence{}
		for _, j := range jobs {
			if j.Status == "missed" && (scope.Bot.ID == "echo" || j.BotID == scope.Bot.ID) {
				out = append(out, j)
			}
		}
		return map[string]any{"jobs": out, "instruction": "The owner can select jobs in the missed-jobs panel, or send /missed run <concurrency> <job IDs> or /missed skip-all. Zero concurrency means unlimited."}, nil
	case "plugin.list":
		return s.Plugins.List()
	case "plugin.configure":
		if text(a, "id") == "confluence" {
			s.confluenceMu.Lock()
			defer s.confluenceMu.Unlock()
		}
		if text(a, "id") == "jira" {
			s.jiraMu.Lock()
			defer s.jiraMu.Unlock()
		}
		if text(a, "id") == "swarm" {
			return nil, errors.New("Swarm connections and permissions are configured by the owner in Settings")
		}
		m, e := s.Plugins.Get(text(a, "id"))
		if e != nil {
			return nil, e
		}
		if value, ok := a["enabled"].(bool); ok {
			if value && m.Pending {
				return nil, errors.New("new capabilities require direct owner approval")
			}
			m.Enabled = value
		}
		if cfg, ok := a["config"].(map[string]any); ok {
			if m.ID == "hosted-server" {
				return nil, errors.New("configure remote hosting in its owner settings")
			}
			if m.ID == "comfyui" {
				cfg, e = comfyUIConfig(m, cfg, false)
				if e != nil {
					return nil, e
				}
			} else if m.ID == "jira" {
				cfg, e = jiraSettings(m, cfg, false)
				if e != nil {
					return nil, e
				}
			} else if m.ID == "confluence" {
				cfg, e = confluenceSettings(m, cfg, false)
				if e != nil {
					return nil, e
				}
			} else if sourceControl(m.ID) {
				cfg, e = s.sourceControlConfig(m, cfg, false)
				if e != nil {
					return nil, e
				}
			} else {
				data, _ := json.Marshal(cfg)
				if e = plugins.Validate(m.Settings, data); e != nil {
					return nil, e
				}
			}
			m.Config = cfg
		}
		e = s.Plugins.Save(m)
		if e == nil && m.ID == "confluence" {
			s.cancelConfluenceLocked()
		}
		if e == nil {
			e = s.Calendar.ReconcileActivation(domain.Now())
		}
		s.emit("changed", "", "", "", nil)
		return m, e
	case "plugin.open":
		m, e := s.Plugins.Get(text(a, "id"))
		if e != nil {
			return nil, e
		}
		if !s.Plugins.Enabled(scope.Bot, m.ID) {
			return nil, errors.New("plugin is disabled or awaiting permission")
		}
		view, e := plugins.FloatingView(m, text(a, "viewId"))
		if e != nil {
			return nil, e
		}
		s.emit("plugin-open", scope.ChatID, scope.RunID, scope.Bot.ID, map[string]any{"id": m.ID, "viewId": view.ID})
		return "opened", nil
	case "plugin.rollback":
		e = s.Plugins.Rollback(text(a, "id"))
		s.emit("changed", "", "", "", nil)
		return "restored previous version", e
	case "plugin.install":
		m := domain.PluginManifest{API: 1, ID: text(a, "id"), Name: text(a, "name"), Version: "1.0.0", Scope: "bot", Config: map[string]any{}, Settings: plugins.Schema(map[string]any{})}
		if value, ok := a["manifest"].(map[string]any); ok {
			data, _ := json.Marshal(value)
			if e = json.Unmarshal(data, &m); e != nil {
				return nil, e
			}
		}
		if html := text(a, "html"); html != "" {
			m.Views = []domain.View{{ID: "main", Title: m.Name, Kind: "floating", HTML: html}}
			if _, fullManifest := a["manifest"].(map[string]any); !fullManifest {
				m.App = &domain.AppRegistration{ViewID: "main"}
			}
		}
		var wasm []byte
		if source := text(a, "backendSource"); source != "" {
			wasm, e = s.compilePlugin(ctx, source)
			if e != nil {
				return nil, e
			}
		}
		m, e = s.Plugins.Install(m, wasm)
		if e != nil {
			return nil, e
		}
		s.emit("changed", "", "", "", nil)
		if view, err := plugins.FloatingView(m, ""); err == nil && s.Plugins.Enabled(scope.Bot, m.ID) {
			s.emit("plugin-open", scope.ChatID, scope.RunID, scope.Bot.ID, map[string]any{"id": m.ID, "viewId": view.ID})
		}
		return m, nil
	}
	return nil, fmt.Errorf("unsupported host capability: %s", method)
}
func (s *Server) compilePlugin(ctx context.Context, source string) ([]byte, error) {
	compiler, e := broker.NativeExecutable("go")
	if e != nil {
		return nil, errors.New("install Go 1.26 or newer to compile backend plugins")
	}
	dir, e := os.MkdirTemp("", "echo-plugin-build-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	if e = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module echo-plugin\n\ngo 1.26.0\n"), 0600); e != nil {
		return nil, e
	}
	if e = os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0600); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	r, e := s.Process.Run(ctx, broker.ProcessRequest{BotID: "plugin-builder", Roots: []string{dir}, Directory: dir, Toolchains: []string{filepath.Dir(filepath.Dir(compiler))}, Executable: compiler, Args: []string{"build", "-trimpath", "-o", "backend.wasm", "."}, Environment: map[string]string{"GOOS": "wasip1", "GOARCH": "wasm", "CGO_ENABLED": "0"}}, nil)
	if e != nil {
		return nil, e
	}
	if r.ExitCode != 0 {
		return nil, fmt.Errorf("backend compilation failed: %s", r.Output)
	}
	return os.ReadFile(filepath.Join(dir, "backend.wasm"))
}

var _ = store.Decode[domain.Bot]
