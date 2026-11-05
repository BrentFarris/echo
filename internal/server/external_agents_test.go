package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/calendar"
	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/externalagent/testdata/mockcli"
)

func init() { mockcli.Run() }

type externalInvocation struct {
	Args             []string
	Input, Directory string
}

func externalFixture(t *testing.T, s *Server, kind string) (domain.Generation, string) {
	t.Helper()
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(key, "")
	}
	t.Setenv("ECHO_EXTERNAL_TEST_CLI", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "invocations.jsonl")
	t.Setenv("ECHO_EXTERNAL_TEST_LOG", log)
	ep := domain.Endpoint{ID: "external", Kind: kind, Name: "External fixture", Executable: exe, WorkingDirectory: dir}
	if err = s.Store.Put("endpoints", ep.ID, ep); err != nil {
		t.Fatal(err)
	}
	return domain.Generation{EndpointID: ep.ID}, log
}
func externalCalls(t *testing.T, path string) []externalInvocation {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []externalInvocation
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var c externalInvocation
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}
func externalSend(t *testing.T, s *Server, c domain.Chat, req SendRequest) *dispatch {
	t.Helper()
	if req.Generation.EndpointID == "" {
		req.Generation = c.Generation
	}
	d, err := s.send(c.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	return d
}
func externalSaved(t *testing.T, s *Server, chat, bot string) externalSession {
	t.Helper()
	var v externalSession
	if err := s.Store.Get("external-sessions", externalHash([]string{chat, bot, "external"}), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestExternalAgentChatResumeActivitiesAndHTTPContext(t *testing.T) {
	for _, kind := range []string{"codex-cli", "claude-code-cli"} {
		t.Run(kind, func(t *testing.T) {
			s := testServer(t)
			g, log := externalFixture(t, s, kind)
			c := chat(t, s, g)
			externalSend(t, s, c, SendRequest{Content: "remember FIRST"})
			first := externalSaved(t, s, c.ID, "echo")
			if !first.Ready || first.SessionID == "" {
				t.Fatal(first)
			}
			externalSend(t, s, c, SendRequest{Content: "SECOND followup"})
			second := externalSaved(t, s, c.ID, "echo")
			if first.SessionID != second.SessionID {
				t.Fatal(first, second)
			}
			calls := externalCalls(t, log)
			if len(calls) != 2 || strings.Contains(calls[1].Input, "FIRST") || strings.Contains(calls[1].Input, "Remembered") || !strings.Contains(calls[1].Input, "SECOND") || strings.Contains(calls[0].Input, "bot_send") {
				t.Fatal(calls)
			}
			messages, _ := s.Store.Messages(c.ID)
			activities := 0
			for _, m := range messages {
				if m.Role == "activity" {
					activities++
					if len(m.Calls) > 0 {
						t.Fatal("activity exposed as Echo calls")
					}
				}
				if m.Role == "tool" {
					t.Fatal("native event entered Echo tool loop")
				}
			}
			if activities < 4 || messages[len(messages)-1].Role != "assistant" {
				t.Fatal(messages)
			}
			bot, _ := s.Bot("echo")
			history, err := s.history(bot, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range history {
				if m.Role == "activity" {
					t.Fatal("activity leaked to HTTP protocol")
				}
			}
			runs, _ := s.Store.ChatRuns(c.ID)
			for _, r := range runs {
				if r.Statistics == nil || !r.Statistics.ExternalAgent || r.Statistics.InputTokens != 20 || r.Statistics.ToolCalls != 1 {
					t.Fatal(r)
				}
			}
		})
	}
}
func TestExternalSessionsRestartForkRerunAndReset(t *testing.T) {
	s := testServer(t)
	g, log := externalFixture(t, s, "codex-cli")
	c := chat(t, s, g)
	externalSend(t, s, c, SendRequest{Content: "remember first"})
	first := externalSaved(t, s, c.ID, "echo")
	dir := s.Dir
	s.Close()
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	s, err = New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	externalSend(t, s, c, SendRequest{Content: "second"})
	if externalSaved(t, s, c.ID, "echo").SessionID != first.SessionID {
		t.Fatal("restart lost session")
	}
	ms, _ := s.Store.Messages(c.ID)
	last := ms[len(ms)-1]
	forkID, err := s.forkChat(context.Background(), c.ID, last.ID, "external-fork")
	if err != nil {
		t.Fatal(err)
	}
	fork, _ := s.Store.Chat(forkID)
	externalSend(t, s, fork, SendRequest{Content: "fork followup"})
	if externalSaved(t, s, fork.ID, "echo").SessionID == first.SessionID {
		t.Fatal("fork reused native session")
	}
	externalSend(t, s, c, SendRequest{rerunMessageID: last.ID, RequestID: "external-rerun"})
	if externalSaved(t, s, c.ID, "echo").SessionID == first.SessionID {
		t.Fatal("rerun kept discarded native context")
	}
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/external-agents/reset", strings.NewReader(`{"chatId":"`+c.ID+`"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	externalSend(t, s, c, SendRequest{Content: "after reset"})
	calls := externalCalls(t, log)
	if slices.Contains(calls[len(calls)-1].Args, "resume") {
		t.Fatal("reset did not clear session")
	}
}
func TestExternalQueuedTasksContinueAndBotsStaySeparate(t *testing.T) {
	s := testServer(t)
	g, log := externalFixture(t, s, "codex-cli")
	c := chat(t, s, g)
	d1, err := s.send(c.ID, SendRequest{Content: "first input", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.send(c.ID, SendRequest{Content: "second input", Generation: g})
	if err == nil {
		t.Fatal("concurrent chat send bypassed the existing dispatch guard")
	}
	waitDispatch(t, d1)
	d2 = externalSend(t, s, c, SendRequest{Content: "second input", Generation: g})
	if d1.err != nil || d2.err != nil {
		t.Fatal(d1.err, d2.err)
	}
	calls := externalCalls(t, log)
	if len(calls) != 2 || strings.Contains(calls[0].Input, "second input") || !slices.Contains(calls[1].Args, "resume") || strings.Contains(calls[1].Input, "first input") {
		t.Fatal(calls)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "externalhelper", Name: "External Helper"})
	if err != nil {
		t.Fatal(err)
	}
	externalSend(t, s, c, SendRequest{Content: "@echo @externalhelper fanout"})
	if externalSaved(t, s, c.ID, "echo").SessionID == externalSaved(t, s, c.ID, bot.ID).SessionID {
		t.Fatal("bots share native context")
	}
	other := chat(t, s, g)
	externalSend(t, s, other, SendRequest{Content: "different chat"})
	if externalSaved(t, s, c.ID, "echo").SessionID == externalSaved(t, s, other.ID, "echo").SessionID {
		t.Fatal("chats share native context")
	}
}
func TestExternalConfigurationAttachmentsSchedulesAndFailure(t *testing.T) {
	s := testServer(t)
	g, log := externalFixture(t, s, "codex-cli")
	c := chat(t, s, g)
	a := domain.Attachment{ID: domain.ID(), ChatID: c.ID, Name: "upload.txt", MediaType: "text/plain", Size: 6}
	if err := s.Store.AddAttachment(a, []byte("upload")); err != nil {
		t.Fatal(err)
	}
	externalSend(t, s, c, SendRequest{Content: "read attachment", Attachments: []string{a.ID}})
	path, err := s.Store.ExternalAttachment(c.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(externalCalls(t, log)[0].Input, path) {
		t.Fatal("attachment omitted")
	}
	old := externalSaved(t, s, c.ID, "echo").SessionID
	bot, _ := s.Bot("echo")
	bot.Prompt = "Changed bot instructions"
	if _, err = s.saveBot(bot); err != nil {
		t.Fatal(err)
	}
	externalSend(t, s, c, SendRequest{Content: "new instructions"})
	if externalSaved(t, s, c.ID, "echo").SessionID == old {
		t.Fatal("changed instructions reused session")
	}
	t.Setenv("ECHO_EXTERNAL_TEST_MODE", "crash")
	d, err := s.send(c.ID, SendRequest{Content: "fail", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err == nil || externalSaved(t, s, c.ID, "echo").Ready {
		t.Fatal("failed session remained resumable")
	}
	t.Setenv("ECHO_EXTERNAL_TEST_MODE", "")
	externalSend(t, s, c, SendRequest{Content: "inspect interrupted work"})
	calls := externalCalls(t, log)
	if slices.Contains(calls[len(calls)-1].Args, "resume") {
		t.Fatal("uncertain session resumed")
	}
	if err = s.scheduled(context.Background(), calendar.Occurrence{BotID: "echo", Prompt: "scheduled external task", Generation: g}); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.DeleteChat(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("attachment cache survived deletion", err)
	}
	var session externalSession
	if err = s.Store.Get("external-sessions", externalHash([]string{c.ID, "echo", "external"}), &session); err == nil {
		t.Fatal("session survived deletion")
	}
}
func TestExternalOwnerConfigurationAndInlineRejection(t *testing.T) {
	s := testServer(t)
	g, _ := externalFixture(t, s, "codex-cli")
	bot, _ := s.Bot("echo")
	for _, args := range []string{`{"id":"external","name":"changed"}`, `{"kind":"codex-cli","name":"new"}`} {
		_, err := s.tool(context.Background(), nil, domain.ExecutionScope{Bot: bot}, domain.ToolCall{Function: domain.FunctionCall{Name: "model_save", Arguments: args}})
		if err == nil {
			t.Fatal("model configured host executable")
		}
	}
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/endpoints/external/check", strings.NewReader(`{}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/plugins/monaco-editor/bots/echo/inline-chat", strings.NewReader(`{"message":"edit","generation":{"endpointId":"`+g.EndpointID+`"},"file":{"title":"new","content":"","selections":[]}}`)))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "external agents") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestExternalSwarmUsesOwningMachine(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "worker", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	ga := attachModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		t.Error("local model invoked for external worker")
		stream(w, "wrong", nil)
	})
	gb, log := externalFixture(t, b, "codex-cli")
	c := chat(t, a, ga)
	d, err := a.send(c.ID, SendRequest{Content: "@worker.echo remote task", Generation: ga, GenerationByNode: map[string]domain.Generation{b.Swarm.Node().ID: gb}})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	if len(externalCalls(t, log)) != 1 {
		t.Fatal("remote invocation missing")
	}
	ms, err := a.messages(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ms, func(m domain.Message) bool { return m.Role == "activity" }) || !slices.ContainsFunc(ms, func(m domain.Message) bool { return m.Content == "Mock response" }) {
		t.Fatal(ms)
	}
	d, err = a.send(c.ID, SendRequest{Content: "@worker.echo followup", Generation: ga, GenerationByNode: map[string]domain.Generation{b.Swarm.Node().ID: gb}})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil {
		t.Fatal(d.err)
	}
	calls := externalCalls(t, log)
	if len(calls) != 2 || !slices.Contains(calls[1].Args, "resume") || strings.Contains(calls[1].Input, "remote task") {
		t.Fatal(calls)
	}
}

func TestExternalStopDeleteShutdownAndRecovery(t *testing.T) {
	for _, action := range []string{"stop", "delete", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			s := testServer(t)
			g, log := externalFixture(t, s, "codex-cli")
			c := chat(t, s, g)
			t.Setenv("ECHO_EXTERNAL_TEST_MODE", "cancel")
			started := filepath.Join(t.TempDir(), "started")
			t.Setenv("ECHO_EXTERNAL_STARTED", started)
			d, err := s.send(c.ID, SendRequest{Content: "long task", Generation: g})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native process did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch action {
			case "stop":
				s.stopChat(c.ID)()
			case "delete":
				mux := http.NewServeMux()
				s.routes(mux)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/chats/"+c.ID, nil))
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			case "shutdown":
				s.Close()
			}
			select {
			case <-d.done:
			case <-time.After(5 * time.Second):
				t.Fatal("native run survived cancellation")
			}
			if d.err == nil || len(externalCalls(t, log)) != 1 {
				t.Fatal("cancelled task succeeded or replayed", d.err)
			}
			if action == "delete" {
				raw, _ := s.Store.List("external-sessions")
				if len(raw) != 0 {
					t.Fatal("deleted chat retained sessions")
				}
				return
			}
			if action == "shutdown" {
				guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
				if err != nil {
					t.Fatal(err)
				}
				s, err = New(s.Dir, nil, guest)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(s.Close)
			}
			if externalSaved(t, s, c.ID, "echo").Ready {
				t.Fatal("cancelled native session can resume")
			}
			// Emulate a crash record and verify startup recovery before a fresh task.
			m := domain.Message{ID: domain.ID(), ChatID: c.ID, BotID: "echo", Role: "activity", Status: "running", Content: "unfinished operation", CreatedAt: domain.Now()}
			if err := s.Store.AddMessage(m); err != nil {
				t.Fatal(err)
			}
			if err := s.Store.Recover(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ECHO_EXTERNAL_TEST_MODE", "")
			externalSend(t, s, c, SendRequest{Content: "continue after interruption"})
			calls := externalCalls(t, log)
			if len(calls) != 2 || slices.Contains(calls[1].Args, "resume") || !strings.Contains(calls[1].Input, "[interrupted]") || !strings.Contains(calls[1].Input, "fresh native session") {
				t.Fatal(calls)
			}
		})
	}
}

func TestExternalIncomingHandoffReturnsNativeFinal(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	b, bh := swarmFixture(t, "worker", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	gb, log := externalFixture(t, b, "claude-code-cli")
	step := 0
	ga := attachModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		step++
		switch step {
		case 1:
			stream(w, "", []domain.ToolCall{{ID: "send", Type: "function", Function: domain.FunctionCall{Name: "bot_send", Arguments: `{"to":"worker.echo","content":"delegated native question","requestId":"external-handoff"}`}}})
		case 2:
			stream(w, "", []domain.ToolCall{{ID: "wait", Type: "function", Function: domain.FunctionCall{Name: "bot_wait", Arguments: `{"requestId":"external-handoff"}`}}})
		default:
			if !strings.Contains(string(body), "Mock response") {
				t.Error("native final was not delivered to sender")
			}
			stream(w, "handoff received", nil)
		}
	})
	c := chat(t, a, ga)
	d, err := a.send(c.ID, SendRequest{Content: "@echo @worker.echo coordinate", Generation: ga, GenerationByNode: map[string]domain.Generation{b.Swarm.Node().ID: gb}})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	if d.err != nil || step != 3 {
		t.Fatal("handoff failed", step, d.err)
	}
	if !slices.ContainsFunc(externalCalls(t, log), func(call externalInvocation) bool { return strings.Contains(call.Input, "delegated native question") }) {
		t.Fatal("CLI did not receive handoff")
	}
}
