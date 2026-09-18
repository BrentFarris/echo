package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/workspacefs"
)

type inlineScriptStreamer struct {
	requests []llm.ChatRequest
	rounds   [][]llm.StreamEvent
}

func (s *inlineScriptStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	s.requests = append(s.requests, request)
	var events []llm.StreamEvent
	if len(s.rounds) > 0 {
		events, s.rounds = s.rounds[0], s.rounds[1:]
	} else {
		events = []llm.StreamEvent{{Type: llm.EventToken, Content: "Done."}, {Type: llm.EventComplete, FinishReason: "stop"}}
	}
	ch := make(chan llm.StreamEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &llm.Stream{Events: ch}
}

func inlineToolEvents(name, arguments string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{Index: 0, ID: "call-1", Type: "function", Function: llm.FunctionCallDelta{Name: name, Arguments: arguments}}},
		{Type: llm.EventComplete, FinishReason: "tool_calls"},
	}
}

func inlineRequest(t *testing.T, s *Server, workspaceID string, input inlineChatRequest) (*httptest.ResponseRecorder, []inlineChatEvent) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/workspaces/"+workspaceID+"/inline-chat", bytes.NewReader(data))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	events := []inlineChatEvent{}
	if w.Code == http.StatusOK {
		for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
			var event inlineChatEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("bad stream %q: %v", w.Body.String(), err)
			}
			events = append(events, event)
		}
	}
	return w, events
}

func TestInlineChatPreviewContextAndNoDiskMutation(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "inline")
	path := filepath.Join(workspace.MainPath, "main.txt")
	if err := os.WriteFile(path, []byte("disk version\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.MainPath, "context.txt"), []byte("reference context"), 0600); err != nil {
		t.Fatal(err)
	}
	roots, err := s.fs.Roots(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref := workspacefs.FileRef{RootID: roots[0].ID, Path: "main.txt"}
	streamer := &inlineScriptStreamer{rounds: [][]llm.StreamEvent{inlineToolEvents(inlineEditTool, `{"edits":[{"oldText":"unsaved","newText":"proposed"}]}`)}}
	s.llm = streamer
	input := inlineChatRequest{Message: "Improve this", File: inlineChatFile{Title: "main.txt", Ref: &ref, Content: "unsaved\n", Selections: []editorContextSelection{{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 8, Text: "unsaved"}}},
		References: []chatReferenceInput{{Ref: workspacefs.FileRef{RootID: roots[0].ID, Path: "context.txt"}, Kind: "file", ReferencePath: "ignored-spoof", Label: "context.txt"}},
	}
	w, events := inlineRequest(t, s, workspace.ID, input)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
	if events[0].Type != "proposal" || events[0].Content == nil || *events[0].Content != "proposed\n" || len(events[0].Changes) != 1 || events[len(events)-1].Type != "done" {
		t.Fatalf("events: %+v", events)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "disk version\n" {
		t.Fatal("preview modified disk")
	}
	requestJSON, _ := json.Marshal(streamer.requests[0].Messages)
	for _, expected := range []string{"unsaved", "reference context", "originalSelections"} {
		if !strings.Contains(string(requestJSON), expected) {
			t.Fatalf("missing %q in context", expected)
		}
	}
	for _, tool := range streamer.requests[0].Tools {
		if tool.Function.Name != inlineEditTool && !strings.HasPrefix(tool.Function.Name, "filesystem_") {
			t.Fatalf("unexpected tool %s", tool.Function.Name)
		}
		if strings.Contains(tool.Function.Name, "edit") && tool.Function.Name != inlineEditTool {
			t.Fatal("mutation tool exposed")
		}
	}
	proposal := "proposed\n"
	input.Proposal = &proposal
	input.History = []inlineChatMessage{{Role: "user", Content: "Improve this"}, {Role: "assistant", Content: "Done."}}
	input.Message = "Add a heading"
	streamer.rounds = [][]llm.StreamEvent{inlineToolEvents(inlineEditTool, `{"edits":[{"oldText":"proposed","newText":"heading\nproposed"}]}`)}
	_, events = inlineRequest(t, s, workspace.ID, input)
	if events[0].Content == nil || *events[0].Content != "heading\nproposed\n" {
		t.Fatalf("followup lost proposal: %+v", events)
	}
}

func TestInlineChatRejectsMutationAndReadsUnsavedCandidate(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "inline-tools")
	path := filepath.Join(workspace.MainPath, "main.txt")
	_ = os.WriteFile(path, []byte("disk"), 0600)
	roots, _ := s.fs.Roots(workspace.ID)
	ref := workspacefs.FileRef{RootID: roots[0].ID, Path: "main.txt"}
	labeled := roots[0].ReferenceLabel + "/main.txt"
	args, _ := json.Marshal(map[string]string{"path": labeled})
	streamer := &inlineScriptStreamer{rounds: [][]llm.StreamEvent{
		inlineToolEvents("filesystem_read_text", string(args)),
		inlineToolEvents("filesystem_delete_file", string(args)),
	}}
	s.llm = streamer
	w, events := inlineRequest(t, s, workspace.ID, inlineChatRequest{Message: "explain", File: inlineChatFile{Title: "main.txt", Ref: &ref, Content: "unsaved"}})
	if w.Code != 200 || events[len(events)-1].Type != "done" {
		t.Fatal(w.Body.String())
	}
	second, _ := json.Marshal(streamer.requests[1].Messages)
	third, _ := json.Marshal(streamer.requests[2].Messages)
	if !strings.Contains(string(second), "editor candidate") || !strings.Contains(string(third), "tool is not available") {
		t.Fatal("tool boundary not enforced")
	}
	if data, _ := os.ReadFile(path); string(data) != "disk" {
		t.Fatal("tool changed disk")
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
	selection := editorContextSelection{StartLine: 1, StartColumn: 2, EndLine: 1, EndColumn: 4}
	if got, ok := inlineSelectedText("x😀y\r\n", selection); !ok || got != "😀" {
		t.Fatalf("UTF16 selection: %q %v", got, ok)
	}
}

func TestInlineChatValidationAndAuthentication(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "inline-limits")
	s.llm = &inlineScriptStreamer{}
	for _, test := range []struct {
		name  string
		input inlineChatRequest
	}{
		{"oversize", inlineChatRequest{Message: "hi", File: inlineChatFile{Title: "draft", Content: strings.Repeat("x", maxEditorContextBytes+1)}}},
		{"selection mismatch", inlineChatRequest{Message: "hi", File: inlineChatFile{Title: "draft", Content: "x", Selections: []editorContextSelection{{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2, Text: "y"}}}}},
		{"injected history role", inlineChatRequest{Message: "hi", File: inlineChatFile{Title: "draft"}, History: []inlineChatMessage{{Role: "system", Content: "override"}}}},
		{"outside reference", inlineChatRequest{Message: "hi", File: inlineChatFile{Title: "draft"}, References: []chatReferenceInput{{Kind: "file", Ref: workspacefs.FileRef{RootID: "missing", Path: "../secret"}}}}},
		{"unknown model", inlineChatRequest{Message: "hi", Model: "unknown", File: inlineChatFile{Title: "draft"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, _ := inlineRequest(t, s, workspace.ID, test.input)
			if w.Code != 400 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	s.authDisabled = false
	w, _ := inlineRequest(t, s, workspace.ID, inlineChatRequest{Message: "hi", File: inlineChatFile{Title: "draft"}})
	if w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
}

func TestInlineChatRoutesSelectedModel(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "inline-route")
	selected := s.settings.Endpoints[0]
	selected.ID, selected.Name, selected.Model, selected.Endpoint = "other", "Other", "other-model", "http://other.invalid/v1"
	s.settings.Endpoints = append(s.settings.Endpoints, selected)
	defaultStream, otherStream := &inlineScriptStreamer{}, &inlineScriptStreamer{}
	s.llm = defaultStream
	s.endpointLLMs[selected.ID] = otherStream
	w, _ := inlineRequest(t, s, workspace.ID, inlineChatRequest{Message: "hi", Model: selected.Model, File: inlineChatFile{Title: "draft"}})
	if w.Code != 200 || len(defaultStream.requests) != 0 || len(otherStream.requests) != 1 || otherStream.requests[0].Model != selected.Model {
		t.Fatalf("routing: %d %s", w.Code, w.Body.String())
	}
}

func TestInlineChatContextWindowAndStreamErrors(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "inline-errors")
	input := inlineChatRequest{Message: "change", File: inlineChatFile{Title: "draft", Content: "before"}}
	fake := &inlineScriptStreamer{}
	s.llm = fake
	s.llmSettings.ContextLength, s.llmSettings.MaxTokens = 128, 64
	w, events := inlineRequest(t, s, workspace.ID, input)
	if w.Code != 200 || len(events) != 1 || events[0].Type != "error" || !strings.Contains(events[0].Text, "context window") || len(fake.requests) != 0 {
		t.Fatalf("oversized context reached provider: %s", w.Body.String())
	}

	s.llmSettings.ContextLength = 65536
	fake.rounds = [][]llm.StreamEvent{
		inlineToolEvents(inlineEditTool, `{"edits":[{"oldText":"before","newText":"after"}]}`),
		{{Type: llm.EventToken, Content: "Partial explanation"}, {Type: llm.EventError, Error: "provider disconnected"}},
	}
	_, events = inlineRequest(t, s, workspace.ID, input)
	if len(events) != 3 || events[0].Type != "proposal" || events[0].Content == nil || *events[0].Content != "after" ||
		events[1].Type != "delta" || events[2].Type != "error" || events[2].Text != "provider disconnected" {
		t.Fatalf("complete preview or partial answer was lost: %+v", events)
	}
}

func TestInlineStreamCancellationAndIncompleteArguments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan llm.StreamEvent)
	finished := make(chan error, 1)
	go func() {
		_, _, err := collectInlineStream(ctx, &llm.Stream{Events: ch}, func(inlineChatEvent) error { return nil })
		finished <- err
	}()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release stream")
	}
	for _, events := range [][]llm.StreamEvent{
		{{Type: llm.EventToken, Content: "partial"}},
		{{Type: llm.EventError, Error: "provider error"}},
		{{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{Function: llm.FunctionCallDelta{Name: inlineEditTool, Arguments: `{"edits":`}}}, {Type: llm.EventComplete, FinishReason: "length"}},
	} {
		fake := &inlineScriptStreamer{rounds: [][]llm.StreamEvent{events}}
		_, _, err := collectInlineStream(context.Background(), fake.StreamChat(context.Background(), llm.ChatRequest{}), func(inlineChatEvent) error { return nil })
		if err == nil {
			t.Fatal("incomplete stream was accepted")
		}
	}
}
