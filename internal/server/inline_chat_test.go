package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/editorfs"
	"github.com/brent/echo/internal/model"
)

func inlineFixture(t *testing.T, s *Server) (domain.Bot, inlineChatRequest, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "code.ts")
	if err := os.WriteFile(path, []byte("const value = 0;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bot, err := s.saveBot(domain.Bot{Handle: "inline-bot", Name: "Inline Bot", Prompt: "Use the selected bot's special conventions.", Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	roots, err := s.EditorFS.Roots(bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	return bot, inlineChatRequest{Message: "edit", File: inlineChatFile{Title: "code.ts", Ref: &editorfs.FileRef{RootID: roots[0].ID, Path: "code.ts"}, Content: "const value = 1;\n", Selections: []inlineSelection{{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 17, Text: "const value = 1;"}}}}, path
}

func inlineHTTP(s *Server, bot domain.Bot, input inlineChatRequest, cookie *http.Cookie, ctx context.Context) *httptest.ResponseRecorder {
	data, _ := json.Marshal(input)
	r := httptest.NewRequest("POST", "https://echo.example/api/v1/plugins/monaco-editor/bots/"+bot.ID+"/inline-chat", strings.NewReader(string(data))).WithContext(ctx)
	r.RemoteAddr = "192.0.2.10:54321"
	r.Header.Set("Origin", "https://echo.example")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func inlineEvents(t *testing.T, w *httptest.ResponseRecorder) []inlineChatEvent {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	events := []inlineChatEvent{}
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		var event inlineChatEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err, w.Body.String())
		}
		events = append(events, event)
	}
	return events
}

func TestInlineChatBotGenerationPreviewAndFollowUp(t *testing.T) {
	s := testServer(t)
	bot, input, path := inlineFixture(t, s)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	var turns atomic.Int32
	input.Generation = attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model       string          `json:"model"`
			Thinking    string          `json:"reasoning_effort"`
			Temperature float64         `json:"temperature"`
			Messages    []model.Message `json:"messages"`
			Tools       []struct {
				Function domain.Tool `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != "chosen-model" || body.Thinking != "high" || body.Temperature != .23 {
			t.Errorf("generation not forwarded: %+v", body)
		}
		if !strings.Contains(body.Messages[0].Content, bot.Prompt) || !strings.Contains(body.Messages[0].Content, "@inline-bot") {
			t.Error("wrong bot instructions")
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == "file_write" || tool.Function.Name == "bot_save" {
				t.Error("mutating tool exposed")
			}
		}
		if body.Messages[len(body.Messages)-1].Role == "tool" {
			stream(w, "Preview ready.", nil)
			return
		}
		before, after := "const value = 1;", "const value = 2;"
		if turns.Add(1) == 2 {
			before, after = after, "const value = 3;"
		}
		context := body.Messages[len(body.Messages)-2].Content
		if !strings.Contains(context, before) {
			t.Error("candidate missing from context", context)
		}
		args, _ := json.Marshal(map[string]any{"edits": []any{map[string]string{"oldText": before, "newText": after}}})
		stream(w, "", []domain.ToolCall{{ID: "edit", Function: domain.FunctionCall{Name: inlineEditTool, Arguments: string(args)}}})
	})
	input.Generation.Model = "chosen-model"
	input.Generation.Thinking = "high"
	input.Generation.Temperature = .23
	for i := 0; i < 2; i++ {
		events := inlineEvents(t, inlineHTTP(s, bot, input, cookie, context.Background()))
		if events[len(events)-1].Type != "done" {
			t.Fatal(events)
		}
		found := false
		for _, event := range events {
			if event.Type == "proposal" {
				found = true
				input.Proposal = event.Content
				if len(event.Changes) == 0 {
					t.Fatal("missing line preview")
				}
			}
		}
		if !found {
			t.Fatal("no proposal", events)
		}
		input.History = append(input.History, inlineChatMessage{Role: "user", Content: input.Message}, inlineChatMessage{Role: "assistant", Content: "Preview ready."})
		input.Message = "follow up"
	}
	if input.Proposal == nil || *input.Proposal != "const value = 3;\n" {
		t.Fatal(input.Proposal)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "const value = 0;\n" {
		t.Fatal("inline chat changed disk")
	}
	chats, _ := s.Store.Chats()
	if len(chats) != 0 {
		t.Fatal("inline chat created durable history")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dispatches) != 0 || len(s.botReservations) != 0 {
		t.Fatal("inline reservation leaked")
	}
}

func TestInlineChatRejectsMutationAndReadsCandidate(t *testing.T) {
	s := testServer(t)
	bot, input, path := inlineFixture(t, s)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	candidate := "candidate from preceding proposal"
	input.Proposal = &candidate
	input.Generation = attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Messages[len(body.Messages)-1].Role == "tool" {
			if !strings.Contains(body.Messages[len(body.Messages)-1].Content, candidate) {
				t.Error("read did not use candidate")
			}
			if !strings.Contains(body.Messages[len(body.Messages)-2].Content, "not available in inline chat") {
				t.Error("mutation was not rejected")
			}
			stream(w, "Done", nil)
			return
		}
		args, _ := json.Marshal(map[string]string{"path": path})
		stream(w, "", []domain.ToolCall{{ID: "bad", Function: domain.FunctionCall{Name: "file_delete", Arguments: string(args)}}, {ID: "read", Function: domain.FunctionCall{Name: "file_read", Arguments: string(args)}}})
	})
	events := inlineEvents(t, inlineHTTP(s, bot, input, cookie, context.Background()))
	if events[len(events)-1].Type != "done" {
		t.Fatal(events)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("file was mutated", err)
	}
	input.File.Ref.Path = "../outside"
	if w := inlineHTTP(s, bot, input, cookie, context.Background()); w.Code != 400 {
		t.Fatal("escaping ref accepted", w.Body.String())
	}
}

func TestInlineChatValidationAndAuthentication(t *testing.T) {
	s := testServer(t)
	bot, input, _ := inlineFixture(t, s)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	input.Generation = attachModel(t, s, func(w http.ResponseWriter, r *http.Request) { stream(w, "Explanation", nil) })
	if w := inlineHTTP(s, bot, input, nil, context.Background()); w.Code != 401 {
		t.Fatal("missing authentication accepted", w.Code)
	}
	for _, mutate := range []func(*inlineChatRequest){
		func(r *inlineChatRequest) { r.File.Selections[0].Text = "wrong" },
		func(r *inlineChatRequest) { r.Generation.EndpointID = "missing" },
		func(r *inlineChatRequest) { r.Generation.Thinking = "unknown" },
		func(r *inlineChatRequest) { r.Generation.Temperature = 3 },
		func(r *inlineChatRequest) { r.File.Content = strings.Repeat("x", maxEditorContextBytes+1) },
		func(r *inlineChatRequest) { r.History = []inlineChatMessage{{Role: "system", Content: "fake"}} },
	} {
		raw, _ := json.Marshal(input)
		var changed inlineChatRequest
		json.Unmarshal(raw, &changed)
		mutate(&changed)
		if w := inlineHTTP(s, bot, changed, cookie, context.Background()); w.Code != 400 {
			t.Fatal("invalid input accepted", w.Code, w.Body.String())
		}
	}
	var endpoint domain.Endpoint
	s.Store.Get("endpoints", input.Generation.EndpointID, &endpoint)
	endpoint.Provider = "unavailable-provider"
	s.Store.Put("endpoints", endpoint.ID, endpoint)
	if w := inlineHTTP(s, bot, input, cookie, context.Background()); w.Code != 403 {
		t.Fatal("unavailable provider accepted", w.Code)
	}
	remoteRequest(s, "PATCH", "/plugins/monaco-editor", `{"enabled":false,"config":{}}`, cookie)
	if w := inlineHTTP(s, bot, input, cookie, context.Background()); w.Code != 403 {
		t.Fatal("disabled editor accepted", w.Code)
	}
}

func waitInline(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("inline lifecycle did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInlineChatAdmissionCancellationAndRevocation(t *testing.T) {
	s := testServer(t)
	bot, input, _ := inlineFixture(t, s)
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	var requests atomic.Int32
	input.Generation = attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Working\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	c := chat(t, s, input.Generation)
	d, err := s.send(c.ID, SendRequest{Content: "@inline-bot wait", Generation: input.Generation})
	if err != nil {
		t.Fatal(err)
	}
	waitInline(t, func() bool { return requests.Load() == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- inlineHTTP(s, bot, input, cookie, ctx) }()
	waitInline(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.botQueues[bot.ID]) == 1 })
	if requests.Load() != 1 {
		t.Fatal("inline bypassed bot queue")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("queued cancellation stuck")
	}
	d.cancel()
	waitDispatch(t, d)
	// An admitted inline run is also included in stopBot's revocation barrier.
	go func() { finished <- inlineHTTP(s, bot, input, cookie, context.Background()) }()
	waitInline(t, func() bool { return requests.Load() == 2 })
	bot.Prompt = "Updated bot scope"
	if _, err = s.updateBot(bot, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-finished:
		if !strings.Contains(w.Body.String(), `"type":"error"`) {
			t.Fatal(w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scope revocation stuck")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.botReservations) != 0 || len(s.botQueues) != 0 {
		t.Fatal("admission leaked")
	}
}

func TestInlineEditsAtomicValidationAndLineEndings(t *testing.T) {
	for _, test := range []struct {
		name, before, args, want string
		invalid                  bool
	}{
		{"insert empty", "", `{"edits":[{"oldText":"","newText":"hello\n"}]}`, "hello\n", false},
		{"delete", "hello\n", `{"edits":[{"oldText":"hello\n","newText":""}]}`, "", false},
		{"crlf unicode", "α\r\n😀\r\n", `{"edits":[{"oldText":"α\n","newText":"β\nγ\n"}]}`, "β\r\nγ\r\n😀\r\n", false},
		{"ambiguous", "x x", `{"edits":[{"oldText":"x","newText":"y"}]}`, "x x", true},
		{"overlapping ambiguous", "aaa", `{"edits":[{"oldText":"aa","newText":"b"}]}`, "aaa", true},
		{"atomic", "a", `{"edits":[{"oldText":"a","newText":"b"},{"oldText":"missing","newText":"c"}]}`, "a", true},
		{"missing newText", "a", `{"edits":[{"oldText":"a"}]}`, "a", true},
		{"trailing json", "a", `{"edits":[{"oldText":"a","newText":"b"}]} {}`, "a", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := applyInlineEdits(test.before, test.args)
			if got != test.want || (err != nil) != test.invalid {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
	selection := inlineSelection{StartLine: 1, StartColumn: 2, EndLine: 1, EndColumn: 4}
	if got, ok := inlineSelectedText("x😀y\r\n", selection); !ok || got != "😀" {
		t.Fatalf("UTF16 selection: %q %v", got, ok)
	}
}

func TestInlineChatMultitaskingDisconnectAndShutdown(t *testing.T) {
	s := testServer(t)
	bot, input, _ := inlineFixture(t, s)
	bot.EnableMultitasking = true
	var err error
	bot, err = s.updateBot(bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	setRemotePassword(t, s)
	cookie := remoteLogin(t, s)
	var requests atomic.Int32
	input.Generation = attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Working\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	finished := make(chan struct{}, 2)
	go func() { inlineHTTP(s, bot, input, cookie, ctx); finished <- struct{}{} }()
	go func() { inlineHTTP(s, bot, input, cookie, context.Background()); finished <- struct{}{} }()
	waitInline(t, func() bool { return requests.Load() == 2 })
	disconnect()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not release inline execution")
	}
	s.mu.Lock()
	remaining := len(s.botReservations[bot.ID])
	s.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("reservations after disconnect: %d", remaining)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not release inline execution")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("inline handler did not exit on shutdown")
	}
}
