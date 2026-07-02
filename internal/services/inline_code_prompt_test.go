package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestSystemServiceSubmitInlineCodePromptIncludesCursorContextAndTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Inline prompts must follow workspace rules."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var captured llm.ChatRequest
	var events []InlineCodePromptEvent
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(
			t,
			w,
			`{"choices":[{"index":0,"delta":{"content":"Use a small "}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"helper here."},"finish_reason":"stop"}]}`,
		)
	}))
	service.inlineCodeEventSink = func(event InlineCodePromptEvent) {
		events = append(events, event)
	}
	filePath := labeledTestPath(t, service, workspaceID, "src/main.go")

	response, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{
		RequestID:        "inline-test-1",
		FilePath:         filePath,
		Prompt:           "What should this do?",
		CursorToken:      "main",
		CursorLineText:   "func main() {}",
		FocusSubstring:   "func main() {}",
		ContextSubstring: "package main\nfunc main() {}\n",
		SelectedText:     "func main() {}",
	})
	if err != nil {
		t.Fatalf("submit inline prompt: %v", err)
	}
	if response.Content != "Use a small helper here." {
		t.Fatalf("unexpected response: %#v", response)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("expected system and user messages, got %#v", captured.Messages)
	}
	if !strings.Contains(captured.Messages[0].Content, "Inline prompts must follow workspace rules.") {
		t.Fatalf("expected AGENTS.md content in system prompt, got %q", captured.Messages[0].Content)
	}
	if !strings.Contains(captured.Messages[0].Content, "aroundLine") {
		t.Fatalf("expected inline system prompt to mention targeted line reads, got %q", captured.Messages[0].Content)
	}
	if !strings.Contains(captured.Messages[0].Content, "mentions @path") {
		t.Fatalf("expected inline system prompt to explain file references, got %q", captured.Messages[0].Content)
	}
	assertSystemPromptOperatingContext(t, captured.Messages[0].Content, root)
	userPrompt := captured.Messages[1].Content
	for _, expected := range []string{
		"File: " + filePath,
		"Cursor target.",
		"Token: main",
		"Source text:",
		"Focused cursor substring:",
		"Context substring:",
		"What should this do?",
		"package main",
		"Selected text. If present, this is the primary target:",
		"func main() {}",
	} {
		if !strings.Contains(userPrompt, expected) {
			t.Fatalf("expected user prompt to contain %q, got %q", expected, userPrompt)
		}
	}
	for _, unexpected := range []string{
		"Cursor: line",
		"Snippet lines:",
	} {
		if strings.Contains(userPrompt, unexpected) {
			t.Fatalf("expected user prompt not to contain %q, got %q", unexpected, userPrompt)
		}
	}
	if !captured.Stream {
		t.Fatalf("expected streaming inline request, got %#v", captured)
	}
	if len(captured.Tools) == 0 || captured.ToolChoice != "auto" {
		t.Fatalf("expected tools with auto choice, got %#v", captured)
	}
	if len(events) < 3 || events[0].Type != "token" || events[0].Content != "Use a small " || events[0].RequestID != "inline-test-1" {
		t.Fatalf("expected streamed token events, got %#v", events)
	}
	if events[len(events)-1].Type != "complete" || events[len(events)-1].Content != "Use a small helper here." {
		t.Fatalf("expected final complete event, got %#v", events)
	}
}

func TestSystemServiceSubmitInlineCodePromptExecutesEditToolAndReturnsAffectedPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var requestCount atomic.Int32
	var events []InlineCodePromptEvent
	var notesPath string
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		count := requestCount.Add(1)
		var captured llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch count {
		case 1:
			args := fmt.Sprintf(`{"path":%q,"oldText":"before\n","newText":"after\n"}`, notesPath)
			writeSSE(
				t,
				w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_edit_text","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, args),
			)
		case 2:
			if len(captured.Messages) < 4 || captured.Messages[len(captured.Messages)-1].Role != llm.RoleTool {
				t.Fatalf("expected tool result in follow-up request, got %#v", captured.Messages)
			}
			writeSSE(t, w,
				kanbanToolCallPayload(t, "call_skill", "workspace_skill_record", map[string]any{
					"action": "skip",
					"reason": "Routine inline edit.",
				}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 3:
			writeSSE(t, w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		default:
			t.Fatalf("unexpected request %d", count)
		}
	}))
	service.inlineCodeEventSink = func(event InlineCodePromptEvent) {
		events = append(events, event)
	}
	notesPath = labeledTestPath(t, service, workspaceID, "notes.txt")

	response, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{
		FilePath:         notesPath,
		Prompt:           "Change before to after.",
		CursorToken:      "before",
		CursorLineText:   "before",
		FocusSubstring:   "before\n",
		ContextSubstring: "before\n",
	})
	if err != nil {
		t.Fatalf("submit inline prompt: %v", err)
	}
	if response.Content != "" {
		t.Fatalf("expected empty final content, got %#v", response)
	}
	if strings.Join(response.AffectedPaths, ",") != notesPath {
		t.Fatalf("expected affected path, got %#v", response.AffectedPaths)
	}
	if len(response.ToolCalls) != 2 || response.ToolCalls[0].Name != "filesystem_edit_text" || response.ToolCalls[0].Status != "complete" ||
		response.ToolCalls[1].Name != "workspace_skill_record" || response.ToolCalls[1].Status != "complete" {
		t.Fatalf("unexpected tool calls: %#v", response.ToolCalls)
	}
	if !hasInlineToolEvent(events, "filesystem_edit_text", "complete") {
		t.Fatalf("expected completed tool call event, got %#v", events)
	}
	data, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after\n" {
		t.Fatalf("expected file edit, got %q", data)
	}
}

func TestSystemServiceSubmitInlineCodePromptAllowsMoreThanEightToolIterations(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var requestCount atomic.Int32
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		count := requestCount.Add(1)
		var captured llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if count <= 9 {
			writeSSE(
				t,
				w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_%d","type":"function","function":{"name":"filesystem_list","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`, count),
			)
			return
		}
		if count != 10 {
			t.Fatalf("unexpected request %d", count)
		}
		if len(captured.Messages) < 20 || captured.Messages[len(captured.Messages)-1].Role != llm.RoleTool {
			t.Fatalf("expected repeated tool results in follow-up request, got %#v", captured.Messages)
		}
		writeSSE(t, w, `{"choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":"stop"}]}`)
	}))
	notesPath := labeledTestPath(t, service, workspaceID, "notes.txt")

	response, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{
		FilePath:         notesPath,
		Prompt:           "Inspect until ready.",
		CursorToken:      "hello",
		CursorLineText:   "hello",
		FocusSubstring:   "hello\n",
		ContextSubstring: "hello\n",
	})
	if err != nil {
		t.Fatalf("submit inline prompt: %v", err)
	}
	if response.Content != "Done." {
		t.Fatalf("unexpected response: %#v", response)
	}
	if requestCount.Load() != 10 {
		t.Fatalf("expected ten model requests, got %d", requestCount.Load())
	}
}

func TestSystemServiceSubmitInlineCodePromptCompactsAcrossToolIterations(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("inline context ", 700)), 0o600); err != nil {
		t.Fatal(err)
	}

	var streamRequests atomic.Int32
	var summaryRequests atomic.Int32
	var events []InlineCodePromptEvent
	var filePath string
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !request.Stream {
			assertCompleteRequest(t, r)
			summaryRequests.Add(1)
			writeChatResponse(t, w, "## Goal and Constraints\n- Complete the inline request.\n## Current State\n- File inspected.\n## Completed Checklist\n- [x] Read prior output.\n## Remaining Checklist\n- [ ] Answer.\n## Decisions and Rejected Approaches\n- None.\n## Relevant Files and Commands\n- large.txt\n## Findings, Errors, and Verification\n- None.\n## Immediate Next Action\n- Answer.")
			return
		}
		assertChatStreamRequest(t, r)
		switch streamRequests.Add(1) {
		case 1:
			writeSSE(t, w,
				kanbanToolCallPayload(t, "inline_read_1", "filesystem_read_text", map[string]any{"path": filePath}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			writeSSE(t, w,
				kanbanToolCallPayload(t, "inline_read_2", "filesystem_read_text", map[string]any{"path": filePath}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 3:
			if !requestContainsContent(request, contextCheckpointStart) {
				t.Fatalf("expected compacted inline context, got %#v", request.Messages)
			}
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Inline completed after compaction."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected stream request %d", streamRequests.Load())
		}
	}))
	service.inlineCodeEventSink = func(event InlineCodePromptEvent) {
		events = append(events, event)
	}
	filePath = labeledTestPath(t, service, workspaceID, "large.txt")
	state := service.LoadState()
	settings := state.Settings
	settings.ContextLength = 4096
	settings.MaxTokens = 512
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save compact context settings: %v", err)
	}

	response, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{
		RequestID:        "inline-compact",
		FilePath:         filePath,
		Prompt:           "Inspect this file twice and answer.",
		CursorToken:      "inline",
		CursorLineText:   "inline context",
		FocusSubstring:   "inline context",
		ContextSubstring: "inline context",
	})
	if err != nil {
		t.Fatalf("submit inline prompt: %v", err)
	}
	if response.Content != "Inline completed after compaction." {
		t.Fatalf("unexpected response: %#v", response)
	}
	if streamRequests.Load() != 3 || summaryRequests.Load() == 0 {
		t.Fatalf("expected tool iterations, compaction, and completion; streams=%d summaries=%d", streamRequests.Load(), summaryRequests.Load())
	}
	hasCompacting := false
	hasCompacted := false
	for _, event := range events {
		hasCompacting = hasCompacting || event.Type == "compacting"
		hasCompacted = hasCompacted || event.Type == "compacted"
	}
	if !hasCompacting || !hasCompacted {
		t.Fatalf("expected inline compaction events, got %#v", events)
	}
}

func TestSystemServiceSubmitInlineCodePromptRecoversOversizedToolResult(t *testing.T) {
	root := t.TempDir()
	const resultMarker = "INLINE_OVERSIZED_TOOL_RESULT"
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(resultMarker), 0o600); err != nil {
		t.Fatal(err)
	}

	var requestCount atomic.Int32
	var recovered llm.ChatRequest
	var filePath string
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				kanbanToolCallPayload(t, "inline_large", "filesystem_read_text", map[string]any{"path": filePath}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			if !requestContainsContent(request, resultMarker) {
				t.Fatalf("expected original tool result, got %#v", request.Messages)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"type":"context_length_exceeded","message":"maximum context length reached"}}`)
		case 3:
			recovered = request
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Retried with focused context."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected request %d", requestCount.Load())
		}
	}))
	filePath = labeledTestPath(t, service, workspaceID, "large.txt")

	response, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{
		FilePath:         filePath,
		Prompt:           "Inspect the file.",
		FocusSubstring:   "placeholder",
		ContextSubstring: "placeholder",
	})
	if err != nil {
		t.Fatalf("submit inline prompt: %v", err)
	}
	if response.Content != "Retried with focused context." {
		t.Fatalf("unexpected response: %#v", response)
	}
	if !requestContainsContent(recovered, toolResultContextErrorCode) ||
		requestContainsContent(recovered, resultMarker) {
		t.Fatalf("expected focused-query recovery request, got %#v", recovered.Messages)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Status != "error" ||
		!strings.Contains(response.ToolCalls[0].Result, toolResultContextErrorCode) {
		t.Fatalf("expected visible oversized-tool error, got %#v", response.ToolCalls)
	}
}

func TestSystemServiceSubmitInlineCodePromptStreamsInlineToolCallWithoutLeakingMarkup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inlineToolCall := `<tool_call><function=filesystem_list><parameter=path>.</parameter></function></tool_call>`
	var requestCount atomic.Int32
	var events []InlineCodePromptEvent
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		count := requestCount.Add(1)
		var captured llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch count {
		case 1:
			writeSSE(
				t,
				w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, "Checking files.\n"+inlineToolCall),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			if len(captured.Messages) < 4 || captured.Messages[len(captured.Messages)-1].Role != llm.RoleTool {
				t.Fatalf("expected inline tool result in follow-up request, got %#v", captured.Messages)
			}
			writeSSE(t, w, `{"choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":"stop"}]}`)
		default:
			t.Fatalf("unexpected request %d", count)
		}
	}))
	service.inlineCodeEventSink = func(event InlineCodePromptEvent) {
		events = append(events, event)
	}
	notesPath := labeledTestPath(t, service, workspaceID, "notes.txt")

	response, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{
		FilePath:         notesPath,
		Prompt:           "Inspect.",
		CursorToken:      "hello",
		CursorLineText:   "hello",
		FocusSubstring:   "hello\n",
		ContextSubstring: "hello\n",
	})
	if err != nil {
		t.Fatalf("submit inline prompt: %v", err)
	}
	if strings.Contains(response.Content, "<tool_call>") {
		t.Fatalf("expected inline tool markup to be removed from final content, got %q", response.Content)
	}
	for _, event := range events {
		if strings.Contains(event.Content, "<tool_call>") {
			t.Fatalf("expected inline tool markup not to stream, got %#v", events)
		}
	}
	if !hasInlineToolEvent(events, "filesystem_list", "complete") {
		t.Fatalf("expected completed inline tool event, got %#v", events)
	}
}

func TestSystemServiceSubmitInlineCodePromptValidationAndStreamErrors(t *testing.T) {
	service, workspaceID, root := newWorkspaceFilesTestService(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	mainPath := labeledTestPath(t, service, workspaceID, "main.go")
	if _, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{FilePath: mainPath}); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("expected prompt validation error, got %v", err)
	}
	if _, err := service.SubmitInlineCodePrompt("missing", InlineCodePromptRequest{FilePath: "main.go", Prompt: "help"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing workspace error, got %v", err)
	}
	traversalPath := workspaceRootLabel(t, service, workspaceID) + "/../main.go"
	if _, err := service.SubmitInlineCodePrompt(workspaceID, InlineCodePromptRequest{FilePath: traversalPath, Prompt: "help"}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal error, got %v", err)
	}

	noCompleteService, noCompleteWorkspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w, `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`)
	}))
	noCompletePath := labeledTestPath(t, noCompleteService, noCompleteWorkspaceID, "main.go")
	if _, err := noCompleteService.SubmitInlineCodePrompt(noCompleteWorkspaceID, InlineCodePromptRequest{FilePath: noCompletePath, Prompt: "help"}); err == nil || !strings.Contains(err.Error(), "ended before completion") {
		t.Fatalf("expected incomplete stream error, got %v", err)
	}
}

func hasInlineToolEvent(events []InlineCodePromptEvent, name string, status string) bool {
	for _, event := range events {
		if event.Type == "tool_call" && event.ToolCall != nil && event.ToolCall.Name == name && event.ToolCall.Status == status {
			return true
		}
	}
	return false
}
