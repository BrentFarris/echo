package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

func TestSystemServiceAppInfo(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	info := service.AppInfo()

	if info.Name != "Echo" {
		t.Fatalf("expected Echo, got %q", info.Name)
	}
	if info.Phase != "release-readiness" {
		t.Fatalf("expected release-readiness phase, got %q", info.Phase)
	}
	if info.AccentHex != "#8f1d2c" {
		t.Fatalf("expected dark red accent, got %q", info.AccentHex)
	}
}

func TestSystemServiceReturnsEmptyCollectionsForUI(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	state := service.LoadState()
	if state.Workspaces == nil {
		t.Fatal("expected empty workspace list to be a non-nil slice")
	}
	if state.KanbanCards == nil {
		t.Fatal("expected empty kanban card list to be a non-nil slice")
	}

	if err := os.WriteFile(storePath, []byte(`{"workspaces":null,"kanbanCards":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state = NewSystemServiceWithStorePath(storePath).LoadState()
	if state.Workspaces == nil || state.KanbanCards == nil {
		t.Fatalf("expected persisted null collections to normalize to empty slices, got %#v", state)
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read normalized state: %v", err)
	}
	if !strings.Contains(string(data), `"kanbanCards": []`) {
		t.Fatalf("expected normalized kanban cards collection, got %s", data)
	}

	workspacePath := filepath.Join(root, "project")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	state, err = service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	session, err := service.LoadChatSession(state.ActiveWorkspaceID)
	if err != nil {
		t.Fatalf("load chat session: %v", err)
	}
	if session.Messages == nil {
		t.Fatal("expected empty chat messages to be a non-nil slice")
	}

	board, err := service.LoadKanbanBoard(state.ActiveWorkspaceID)
	if err != nil {
		t.Fatalf("load kanban board: %v", err)
	}
	if board.Ready == nil || board.InProgress == nil || board.Blocked == nil || board.Done == nil {
		t.Fatalf("expected empty board lanes to be non-nil slices, got %#v", board)
	}
}

func TestSystemServiceChatSendStreamsAssistantMessage(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Hello **there**"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	session, err := service.SendChatMessage(workspaceID, "Plan this")
	if err != nil {
		t.Fatalf("send chat: %v", err)
	}
	if !session.Busy {
		t.Fatal("expected chat to be busy after send")
	}
	if len(session.Messages) != 2 || session.Messages[0].Role != llm.RoleUser || session.Messages[1].Role != llm.RoleAssistant {
		t.Fatalf("unexpected starting messages: %#v", session.Messages)
	}

	session = waitForChatIdle(t, service, workspaceID)
	if got := session.Messages[1].Content; got != "Hello **there**." {
		t.Fatalf("expected streamed assistant content, got %q", got)
	}
	if session.Messages[1].Status != "complete" {
		t.Fatalf("expected complete assistant message, got %#v", session.Messages[1])
	}
}

func TestSystemServiceChatIncludesWorkspaceInstructions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Always run the Echo workspace checks."), 0o600); err != nil {
		t.Fatal(err)
	}
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Noted."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	if _, err := service.SendChatMessage(workspaceID, "What should I do?"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	if len(captured.Messages) == 0 || captured.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected system message first, got %#v", captured.Messages)
	}
	if !strings.Contains(captured.Messages[0].Content, "Always run the Echo workspace checks.") {
		t.Fatalf("expected AGENTS.md content in system prompt, got %q", captured.Messages[0].Content)
	}
	assertSystemPromptOperatingContext(t, captured.Messages[0].Content, root)
}

func TestSystemServiceChatThinkingCorrectionStaysOutOfVisibleTranscript(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Done."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	settings := service.LoadState().Settings
	settings.ThinkingCorrection = true
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	if _, err := service.SendChatMessage(workspaceID, "Inspect this"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if len(captured.Messages) < 2 || !strings.Contains(captured.Messages[1].Content, llm.ThinkingCorrectionText) {
		t.Fatalf("expected thinking correction in LLM request, got %#v", captured.Messages)
	}
	if len(session.Messages) < 1 || strings.Contains(session.Messages[0].Content, llm.ThinkingCorrectionText) {
		t.Fatalf("expected visible user message to omit thinking correction, got %#v", session.Messages)
	}
}

func TestSystemServiceChatPlanModeUsesReadOnlyPlanningRequest(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Plan only."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Inspect and plan this", true); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	if len(captured.Messages) == 0 || captured.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected system message first, got %#v", captured.Messages)
	}
	assertPlanModeChatRequest(t, captured)
}

func TestSystemServiceChatNonPlanModeUsesFullToolRequest(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Ready to work."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Do this", false); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	names := chatRequestToolNames(captured)
	for _, expected := range []string{"filesystem_create_text", "filesystem_delete_file", "filesystem_edit_text", "filesystem_list", "filesystem_read_image", "filesystem_read_text", "filesystem_read_video", "filesystem_search_text", "filesystem_stat", "shell_command", "workspace_context"} {
		if !names[expected] {
			t.Fatalf("expected non-plan mode to include %s, got %#v", expected, names)
		}
	}
}

func TestSystemServiceRetryChatMessageUsesPlanMode(t *testing.T) {
	root := t.TempDir()
	retryRequest := make(chan llm.ChatRequest, 1)
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Initial response."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		case 2:
			var captured llm.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode retry request: %v", err)
			}
			retryRequest <- captured
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Plan-mode retry."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Inspect this", false); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if len(session.Messages) < 2 {
		t.Fatalf("expected assistant message, got %#v", session.Messages)
	}
	if _, err := service.RetryChatMessage(workspaceID, session.Messages[1].ID, true); err != nil {
		t.Fatalf("retry chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	var captured llm.ChatRequest
	select {
	case captured = <-retryRequest:
	case <-time.After(time.Second):
		t.Fatal("retry request was not captured")
	}
	assertPlanModeChatRequest(t, captured)
}

func TestSystemServiceEditChatMessageUsesPlanMode(t *testing.T) {
	root := t.TempDir()
	editRequest := make(chan llm.ChatRequest, 1)
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Initial response."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		case 2:
			var captured llm.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode edit request: %v", err)
			}
			editRequest <- captured
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Plan-mode edit."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Inspect this", false); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if len(session.Messages) < 1 {
		t.Fatalf("expected user message, got %#v", session.Messages)
	}
	if _, err := service.EditChatMessage(workspaceID, session.Messages[0].ID, "Inspect this carefully", true); err != nil {
		t.Fatalf("edit chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	var captured llm.ChatRequest
	select {
	case captured = <-editRequest:
	case <-time.After(time.Second):
		t.Fatal("edit request was not captured")
	}
	assertPlanModeChatRequest(t, captured)
	if len(captured.Messages) < 2 || captured.Messages[1].Content != "Inspect this carefully" {
		t.Fatalf("expected edited user message in request, got %#v", captured.Messages)
	}
}

func TestSystemServiceEditAssistantMessageUpdatesVisiblePlanAndHistory(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	var captured llm.ChatRequest
	service, workspaceID := newDecompositionTestServiceWithStore(t, root, storePath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode decomposition request: %v", err)
		}
		writeChatResponse(t, w, `{"cards":[{"id":"phase-1","title":"Build revised plan","description":"Implement the revised assistant plan.","acceptanceCriteria":["Revised behavior exists"],"dependencies":[]}]}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Plan the feature.", Status: "complete"},
		{
			ID:        "msg-2",
			Role:      llm.RoleAssistant,
			Content:   "Original plan text.",
			Reasoning: "Original hidden reasoning.",
			ToolCalls: []ChatToolActivity{{ID: "call-1", Name: "filesystem_list", Status: "complete"}},
			Status:    "complete",
		},
		{ID: "msg-3", Role: llm.RoleUser, Content: "Keep the change focused.", Status: "complete"},
		{ID: "msg-4", Role: llm.RoleAssistant, Content: "Follow-up plan detail.", Status: "complete"},
	}, []llm.Message{
		{Role: llm.RoleUser, Content: "Plan the feature."},
		{Role: llm.RoleAssistant, Content: "Inspecting first.", ToolCalls: []llm.ToolCall{{ID: "call-1"}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: "Original tool result."},
		{Role: llm.RoleAssistant, Content: "Original plan text."},
		{Role: llm.RoleUser, Content: "Keep the change focused."},
		{Role: llm.RoleAssistant, Content: "Follow-up plan detail."},
	})

	const revisedPlan = "Revised plan: implement only the focused behavior."
	session, err := service.EditChatMessage(workspaceID, "msg-2", revisedPlan, false)
	if err != nil {
		t.Fatalf("edit assistant message: %v", err)
	}
	if session.Busy || len(session.Messages) != 4 || session.Messages[1].ID != "msg-2" {
		t.Fatalf("expected an in-place idle edit, got %#v", session)
	}
	if session.Messages[1].Content != revisedPlan {
		t.Fatalf("expected revised visible content, got %q", session.Messages[1].Content)
	}

	service.chatMu.Lock()
	history := cloneLLMMessages(service.chatSessions[workspaceID].History)
	service.chatMu.Unlock()
	if len(history) != 4 {
		t.Fatalf("expected edited assistant turn to replace tool history, got %#v", history)
	}
	if history[1].Role != llm.RoleAssistant || history[1].Content != revisedPlan {
		t.Fatalf("expected revised assistant history, got %#v", history)
	}
	if history[2].Role != llm.RoleUser || history[2].Content != "Keep the change focused." {
		t.Fatalf("expected later history to be preserved, got %#v", history)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	persisted, err := reloaded.LoadChatSession(workspaceID)
	if err != nil {
		t.Fatalf("load persisted chat: %v", err)
	}
	if len(persisted.Messages) != 4 || persisted.Messages[1].Content != revisedPlan {
		t.Fatalf("expected revised plan to persist, got %#v", persisted.Messages)
	}

	if _, err := service.ExecutePlan(workspaceID); err != nil {
		t.Fatalf("execute revised plan: %v", err)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("expected decomposition request, got %#v", captured.Messages)
	}
	visiblePrompt := captured.Messages[1].Content
	if !strings.Contains(visiblePrompt, revisedPlan) {
		t.Fatalf("expected decomposition to use revised plan, got %q", visiblePrompt)
	}
	for _, stale := range []string{"Original plan text.", "Original hidden reasoning.", "Original tool result."} {
		if strings.Contains(visiblePrompt, stale) {
			t.Fatalf("decomposition request included stale assistant state %q: %q", stale, visiblePrompt)
		}
	}
}

func TestSystemServiceEditAssistantMessageRequiresCompleteStatus(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("editing an assistant message should not call the model")
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Plan the feature.", Status: "complete"},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Partial plan.", Status: "streaming"},
	}, []llm.Message{{Role: llm.RoleUser, Content: "Plan the feature."}})

	_, err := service.EditChatMessage(workspaceID, "msg-2", "Revised plan.", false)
	if err == nil || !strings.Contains(err.Error(), "complete assistant") {
		t.Fatalf("expected complete-assistant validation error, got %v", err)
	}
}

func TestSystemServicePruneChatMessagesFocusesPlanAndHistory(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	var captured llm.ChatRequest
	service, workspaceID := newDecompositionTestServiceWithStore(t, root, storePath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode decomposition request: %v", err)
		}
		writeChatResponse(t, w, `{"cards":[{"id":"phase-1","title":"Implement focused work","description":"Use only the focused plan.","acceptanceCriteria":["Focused behavior exists"],"dependencies":[]}]}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Broad request that should be pruned.", Status: "complete"},
		{
			ID:        "msg-2",
			Role:      llm.RoleAssistant,
			Content:   "Noisy plan that should be pruned.",
			ToolCalls: []ChatToolActivity{{ID: "call-1", Name: "filesystem_list", Status: "complete"}},
			Status:    "complete",
		},
		{ID: "msg-3", Role: llm.RoleUser, Content: "Keep only the focused change.", Status: "complete"},
		{ID: "msg-4", Role: llm.RoleAssistant, Content: "Focused approved plan.", Status: "complete"},
	}, []llm.Message{
		{Role: llm.RoleUser, Content: "Broad request that should be pruned."},
		{Role: llm.RoleAssistant, Content: "Inspecting noisy context.", ToolCalls: []llm.ToolCall{{ID: "call-1"}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: "Hidden noisy tool result."},
		{Role: llm.RoleAssistant, Content: "Noisy plan that should be pruned."},
		{Role: llm.RoleUser, Content: "Keep only the focused change."},
		{Role: llm.RoleAssistant, Content: "Focused approved plan."},
	})

	if _, err := service.PruneChatMessage(workspaceID, "msg-2"); err != nil {
		t.Fatalf("prune assistant message: %v", err)
	}
	session, err := service.PruneChatMessage(workspaceID, "msg-1")
	if err != nil {
		t.Fatalf("prune user message: %v", err)
	}
	if len(session.Messages) != 2 || session.Messages[0].ID != "msg-3" || session.Messages[1].ID != "msg-4" {
		t.Fatalf("expected only focused messages to remain, got %#v", session.Messages)
	}

	service.chatMu.Lock()
	history := cloneLLMMessages(service.chatSessions[workspaceID].History)
	service.chatMu.Unlock()
	if len(history) != 2 || history[0].Content != "Keep only the focused change." || history[1].Content != "Focused approved plan." {
		t.Fatalf("expected retained history to match visible focused chat, got %#v", history)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	persisted, err := reloaded.LoadChatSession(workspaceID)
	if err != nil {
		t.Fatalf("load persisted pruned chat: %v", err)
	}
	if len(persisted.Messages) != 2 {
		t.Fatalf("expected pruned chat to persist, got %#v", persisted.Messages)
	}

	if _, err := service.ExecutePlan(workspaceID); err != nil {
		t.Fatalf("execute focused plan: %v", err)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("expected decomposition request, got %#v", captured.Messages)
	}
	visiblePrompt := captured.Messages[1].Content
	for _, expected := range []string{"Keep only the focused change.", "Focused approved plan."} {
		if !strings.Contains(visiblePrompt, expected) {
			t.Fatalf("expected focused prompt to include %q, got %q", expected, visiblePrompt)
		}
	}
	for _, pruned := range []string{
		"Broad request that should be pruned.",
		"Noisy plan that should be pruned.",
		"Inspecting noisy context.",
		"Hidden noisy tool result.",
	} {
		if strings.Contains(visiblePrompt, pruned) {
			t.Fatalf("decomposition prompt included pruned context %q: %q", pruned, visiblePrompt)
		}
	}
}

func TestVisibleChatHistoryPreservesRemainingImages(t *testing.T) {
	const imageDataURL = "data:image/png;base64,aW1hZ2U="
	history := visibleChatHistory([]ChatMessage{
		{
			Role:    llm.RoleUser,
			Content: "Use this remaining screenshot.",
			Images:  []ChatImageAttachment{{DataURL: imageDataURL}},
			Status:  "complete",
		},
		{Role: llm.RoleAssistant, Content: "Incomplete response.", Status: "canceled"},
		{Role: llm.RoleAssistant, Content: "Complete response.", Status: "complete"},
	})

	if len(history) != 2 {
		t.Fatalf("expected user and complete assistant history, got %#v", history)
	}
	if len(history[0].ContentParts) != 2 || history[0].ContentParts[1].ImageURL == nil || history[0].ContentParts[1].ImageURL.URL != imageDataURL {
		t.Fatalf("expected remaining image content to be preserved, got %#v", history[0].ContentParts)
	}
	if history[1].Content != "Complete response." {
		t.Fatalf("expected only complete assistant content, got %#v", history)
	}
}

func TestSystemServiceChatSendsPastedImageAsContentPart(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Looks good."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	if _, err := service.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{
		Content:  "Review this screenshot.",
		PlanMode: true,
		Images: []ChatImageInput{{
			Name:      "screen.png",
			MediaType: "image/png",
			DataURL:   tinyPNGDataURL(),
			Bytes:     int64(len(tinyPNGBytes())),
		}},
	}); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if len(session.Messages) < 1 || len(session.Messages[0].Images) != 1 {
		t.Fatalf("expected user message image metadata, got %#v", session.Messages)
	}
	if session.Messages[0].Images[0].Name != "screen.png" || !strings.HasPrefix(session.Messages[0].Images[0].DataURL, "data:image/png;base64,") {
		t.Fatalf("unexpected user image metadata: %#v", session.Messages[0].Images[0])
	}
	if len(captured.Messages) < 2 {
		t.Fatalf("expected system and user messages, got %#v", captured.Messages)
	}
	user := captured.Messages[1]
	if len(user.ContentParts) != 2 {
		t.Fatalf("expected text plus image content parts, got %#v", user)
	}
	if user.ContentParts[0].Type != "text" || !strings.Contains(user.ContentParts[0].Text, "Attached media:") {
		t.Fatalf("expected attached media labels in text part, got %#v", user.ContentParts[0])
	}
	if strings.Contains(user.ContentParts[0].Text, "data:image") {
		t.Fatalf("expected text part to omit base64 image data, got %q", user.ContentParts[0].Text)
	}
	if user.ContentParts[1].Type != "image_url" || user.ContentParts[1].ImageURL == nil || !strings.HasPrefix(user.ContentParts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("expected image_url data URL part, got %#v", user.ContentParts[1])
	}
	if user.ContentParts[1].ImageURL.Detail != "" {
		t.Fatalf("expected image detail to be omitted, got %#v", user.ContentParts[1].ImageURL)
	}
}

func TestSystemServiceChatSendsWorkspaceImageMentionAsContentPart(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ui.png"), tinyPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Reviewed."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	imagePath := labeledTestPath(t, service, workspaceID, "ui.png")

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Review @"+imagePath, true); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if len(session.Messages[0].Images) != 1 || session.Messages[0].Images[0].Path != imagePath {
		t.Fatalf("expected workspace image metadata, got %#v", session.Messages[0].Images)
	}
	user := captured.Messages[1]
	if len(user.ContentParts) != 2 || user.ContentParts[1].ImageURL == nil {
		t.Fatalf("expected workspace image content part, got %#v", user)
	}
	if !strings.Contains(user.ContentParts[0].Text, imagePath) {
		t.Fatalf("expected text part to name workspace image, got %q", user.ContentParts[0].Text)
	}
}

func TestSystemServiceChatReadImageToolSendsImageContentPart(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ui.png"), tinyPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var requestCount atomic.Int32
	var secondRequest llm.ChatRequest
	var imagePath string
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_image","type":"function","function":{"name":"filesystem_read_image","arguments":%q}}]}}]}`, fmt.Sprintf(`{"path":%q,"detail":"high"}`, imagePath)),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			if err := json.NewDecoder(r.Body).Decode(&secondRequest); err != nil {
				t.Fatalf("decode second request: %v", err)
			}
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"The screenshot shows the UI."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))
	imagePath = labeledTestPath(t, service, workspaceID, "ui.png")

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Inspect ui.png visually.", true); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if requestCount.Load() != 2 {
		t.Fatalf("expected image tool follow-up request, got %d requests", requestCount.Load())
	}
	if len(session.Messages[1].ToolCalls) != 1 || !strings.Contains(session.Messages[1].ToolCalls[0].Result, `"contentType":"image_url"`) {
		t.Fatalf("expected visible image tool result metadata, got %#v", session.Messages[1].ToolCalls)
	}
	if strings.Contains(session.Messages[1].ToolCalls[0].Result, "data:image") {
		t.Fatalf("expected visible tool result to omit image data URL, got %q", session.Messages[1].ToolCalls[0].Result)
	}

	var toolMessage *llm.Message
	var imageMessage *llm.Message
	for i := range secondRequest.Messages {
		message := &secondRequest.Messages[i]
		if message.Role == llm.RoleTool && message.ToolCallID == "call_image" {
			toolMessage = message
		}
		if message.Role == llm.RoleUser && len(message.ContentParts) == 2 && message.ContentParts[1].ImageURL != nil {
			imageMessage = message
		}
	}
	if toolMessage == nil || !strings.Contains(toolMessage.Content, `"contentType":"image_url"`) {
		t.Fatalf("expected compact tool result message, got %#v", secondRequest.Messages)
	}
	if strings.Contains(toolMessage.Content, "data:image") {
		t.Fatalf("expected tool message to omit image data URL, got %q", toolMessage.Content)
	}
	if imageMessage == nil {
		t.Fatalf("expected image content-parts user message, got %#v", secondRequest.Messages)
	}
	if imageMessage.ContentParts[0].Type != "text" || !strings.Contains(imageMessage.ContentParts[0].Text, "filesystem_read_image") {
		t.Fatalf("expected image message text to name the tool, got %#v", imageMessage.ContentParts[0])
	}
	imagePart := imageMessage.ContentParts[1]
	if imagePart.Type != "image_url" || imagePart.ImageURL == nil || !strings.HasPrefix(imagePart.ImageURL.URL, "data:image/png;base64,") || imagePart.ImageURL.Detail != "high" {
		t.Fatalf("expected image_url data URL with detail, got %#v", imagePart)
	}
}

func TestSystemServiceChatRejectsUnsupportedPastedImage(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("chat request should not be sent for an unsupported image")
	}))

	_, err := service.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{
		Content: "Review this.",
		Images: []ChatImageInput{{
			Name:    "vector.svg",
			DataURL: "data:image/svg+xml;base64,PHN2Zy8+",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported image format") {
		t.Fatalf("expected unsupported image error, got %v", err)
	}
}

func TestSystemServiceChatRejectsOversizedWorkspaceImage(t *testing.T) {
	root := t.TempDir()
	data := append(tinyPNGBytes(), make([]byte, maxChatImageBytes+1)...)
	if err := os.WriteFile(filepath.Join(root, "huge.png"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("chat request should not be sent for an oversized image")
	}))
	imagePath := labeledTestPath(t, service, workspaceID, "huge.png")

	_, err := service.SendChatMessage(workspaceID, "Review @"+imagePath)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("expected oversized image error, got %v", err)
	}
}

func TestSystemServiceChatRejectsWorkspaceImageTraversal(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("chat request should not be sent for path traversal")
	}))
	traversalPath := workspaceRootLabel(t, service, workspaceID) + "/../outside.png"

	_, err := service.SendChatMessage(workspaceID, "Review @"+traversalPath)
	if err == nil || !strings.Contains(err.Error(), "escapes the workspace") {
		t.Fatalf("expected traversal error, got %v", err)
	}
}

func TestSystemServiceChatPreservesImageContentInRuntimeHistory(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	var secondRequest llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		count := requestCount.Add(1)
		var captured llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if count == 2 {
			secondRequest = captured
		}
		writeSSE(t, w,
			fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":"Response %d."}}]}`, count),
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	if _, err := service.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{
		Content: "Review this image.",
		Images: []ChatImageInput{{
			Name:    "first.png",
			DataURL: tinyPNGDataURL(),
		}},
	}); err != nil {
		t.Fatalf("send first chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)
	if _, err := service.SendChatMessage(workspaceID, "Now compare it with the plan."); err != nil {
		t.Fatalf("send second chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	if len(secondRequest.Messages) < 4 {
		t.Fatalf("expected second request to include prior history, got %#v", secondRequest.Messages)
	}
	priorUser := secondRequest.Messages[1]
	if len(priorUser.ContentParts) != 2 || priorUser.ContentParts[1].ImageURL == nil {
		t.Fatalf("expected prior image content parts in runtime history, got %#v", priorUser)
	}
}

func TestSystemServiceChatPlanModeBlocksInlineMutatingToolCall(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	var secondRequest llm.ChatRequest
	inlineToolCall := `<tool_call> <function=filesystem_create_text> <parameter=path> blocked.txt </parameter> <parameter=content> should not exist </parameter> </function> </tool_call>`
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, inlineToolCall),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		case 2:
			if err := json.NewDecoder(r.Body).Decode(&secondRequest); err != nil {
				t.Fatalf("decode second request: %v", err)
			}
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"I cannot make changes in plan mode."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))

	if _, err := service.SendChatMessageWithPlanMode(workspaceID, "Create a file", true); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if requestCount.Load() != 2 {
		t.Fatalf("expected denied tool call to be returned to model, got %d requests", requestCount.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected mutating tool call not to create blocked.txt, stat error: %v", err)
	}
	requestData, err := json.Marshal(secondRequest.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requestData), "tool_not_allowed") {
		t.Fatalf("expected denied tool result in second request, got %s", requestData)
	}
	assistant := session.Messages[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Status != "error" {
		t.Fatalf("expected blocked tool call to be shown as an error, got %#v", assistant.ToolCalls)
	}
}

func TestSystemServiceChatShowsReasoningAndToolActivity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	var listPath string
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			args := fmt.Sprintf(`{"path":%q}`, listPath)
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"reasoning_content":"Checking files."}}]}`,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_list","arguments":%q}}]}}]}`, args),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Found README.md."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))
	listPath = workspaceRootLabel(t, service, workspaceID)

	if _, err := service.SendChatMessage(workspaceID, "Inspect the workspace"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	assistant := session.Messages[1]
	if assistant.Reasoning != "Checking files." {
		t.Fatalf("expected reasoning to stream, got %q", assistant.Reasoning)
	}
	if assistant.Content != "Found README.md." {
		t.Fatalf("expected final assistant content, got %q", assistant.Content)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", assistant.ToolCalls)
	}
	toolCall := assistant.ToolCalls[0]
	if toolCall.Name != "filesystem_list" || toolCall.Status != "complete" {
		t.Fatalf("unexpected tool activity: %#v", toolCall)
	}
	if !strings.Contains(toolCall.Result, "README.md") {
		t.Fatalf("expected tool result to include file listing, got %q", toolCall.Result)
	}
}

func TestSystemServiceChatRecoversWhenToolResultExceedsContext(t *testing.T) {
	root := t.TempDir()
	const resultMarker = "UNBOUNDED_RESULT_MARKER"
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(resultMarker), 0o600); err != nil {
		t.Fatal(err)
	}

	var requestCount atomic.Int32
	var recoveredRequest llm.ChatRequest
	var filePath string
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				kanbanToolCallPayload(t, "call_large", "filesystem_read_text", map[string]any{"path": filePath}),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			if !requestContainsContent(request, resultMarker) {
				t.Fatalf("expected original tool result before context rejection, got %#v", request.Messages)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"code":400,"message":"request (174435 tokens) exceeds the available context size (147456 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":174435,"n_ctx":147456}}`)
		case 3:
			recoveredRequest = request
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Recovered with a narrower request."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))
	filePath = labeledTestPath(t, service, workspaceID, "large.txt")

	if _, err := service.SendChatMessage(workspaceID, "Inspect the large file"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if requestCount.Load() != 3 {
		t.Fatalf("expected context rejection to be retried, got %d requests", requestCount.Load())
	}
	if !requestContainsContent(recoveredRequest, toolResultContextErrorCode) ||
		!requestContainsContent(recoveredRequest, "Narrow the request") {
		t.Fatalf("expected bounded tool error in recovery request, got %#v", recoveredRequest.Messages)
	}
	if requestContainsContent(recoveredRequest, resultMarker) {
		t.Fatalf("expected oversized result to be removed, got %#v", recoveredRequest.Messages)
	}
	assistant := session.Messages[1]
	if assistant.Status != "complete" || assistant.Content != "Recovered with a narrower request." {
		t.Fatalf("expected completed recovered response, got %#v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Status != "error" ||
		!strings.Contains(assistant.ToolCalls[0].Result, toolResultContextErrorCode) {
		t.Fatalf("expected visible tool activity to show the context error, got %#v", assistant.ToolCalls)
	}
	historyData, err := json.Marshal(service.chatHistory(workspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(historyData), resultMarker) || !strings.Contains(string(historyData), toolResultContextErrorCode) {
		t.Fatalf("expected persisted runtime history to keep only the bounded result, got %s", historyData)
	}
}

func TestSystemServiceChatRepairsMalformedToolArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	var secondRequest llm.ChatRequest
	malformedArgs := `{"path":"."`
	repairedArgs := `{"path":"."}`
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"filesystem_list","arguments":%q}}]}}]}`, malformedArgs),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			if err := json.NewDecoder(r.Body).Decode(&secondRequest); err != nil {
				t.Fatalf("decode second request: %v", err)
			}
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Found README.md."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))

	if _, err := service.SendChatMessage(workspaceID, "Inspect the workspace"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if requestCount.Load() != 2 {
		t.Fatalf("expected tool result follow-up request, got %d requests", requestCount.Load())
	}
	assistant := session.Messages[1]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", assistant.ToolCalls)
	}
	toolCall := assistant.ToolCalls[0]
	if toolCall.Status != "complete" || toolCall.Arguments != repairedArgs {
		t.Fatalf("expected repaired complete tool activity, got %#v", toolCall)
	}

	var assistantHistory *llm.Message
	for i := range secondRequest.Messages {
		message := &secondRequest.Messages[i]
		if message.Role == llm.RoleAssistant && len(message.ToolCalls) == 1 {
			assistantHistory = message
			break
		}
	}
	if assistantHistory == nil {
		t.Fatalf("expected repaired assistant tool call in follow-up request, got %#v", secondRequest.Messages)
	}
	if got := assistantHistory.ToolCalls[0].Function.Arguments; got != repairedArgs {
		t.Fatalf("expected repaired arguments in follow-up request, got %q", got)
	}
}

func TestSystemServiceChatHandlesInlineReasoningToolCall(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	inlineToolCall := `<tool_call> <function=filesystem_list> <parameter=path> . </parameter> </function> </tool_call>`
	var requestCount atomic.Int32
	var secondRequest llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, "Checking files.\n"+inlineToolCall),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		case 2:
			if err := json.NewDecoder(r.Body).Decode(&secondRequest); err != nil {
				t.Fatalf("decode second request: %v", err)
			}
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Found README.md."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))

	if _, err := service.SendChatMessage(workspaceID, "Inspect the workspace"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if requestCount.Load() != 2 {
		t.Fatalf("expected tool result follow-up request, got %d requests", requestCount.Load())
	}
	assistant := session.Messages[1]
	if !strings.Contains(assistant.Reasoning, "Checking files.") || strings.Contains(assistant.Reasoning, "tool_call") {
		t.Fatalf("expected clean reasoning without inline tool markup, got %q", assistant.Reasoning)
	}
	if assistant.Content != "Found README.md." {
		t.Fatalf("expected final assistant content, got %q", assistant.Content)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != "filesystem_list" || assistant.ToolCalls[0].Status != "complete" {
		t.Fatalf("expected completed inline tool activity, got %#v", assistant.ToolCalls)
	}

	var assistantToolID string
	var toolMessageID string
	for _, message := range secondRequest.Messages {
		if message.Role == llm.RoleAssistant && len(message.ToolCalls) == 1 {
			assistantToolID = message.ToolCalls[0].ID
		}
		if message.Role == llm.RoleTool {
			toolMessageID = message.ToolCallID
		}
	}
	if assistantToolID == "" || toolMessageID != assistantToolID {
		t.Fatalf("expected matching assistant/tool call ids, got assistant=%q tool=%q in %#v", assistantToolID, toolMessageID, secondRequest.Messages)
	}
}

func TestSystemServiceChatRetriesWhenStreamLoops(t *testing.T) {
	root := t.TempDir()
	loopPhrase := "checking the workspace now "
	retryRequest := make(chan llm.ChatRequest, 1)
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, loopPhrase),
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, loopPhrase),
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, loopPhrase),
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, loopPhrase),
			)
		case 2:
			var captured llm.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode retry request: %v", err)
			}
			retryRequest <- captured
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Recovered without repeating."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))

	if _, err := service.SendChatMessage(workspaceID, "Plan this"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if requestCount.Load() != 2 {
		t.Fatalf("expected one retry request, got %d", requestCount.Load())
	}
	assistant := session.Messages[1]
	if assistant.Status != "complete" || !strings.Contains(assistant.Content, "Recovered without repeating.") {
		t.Fatalf("expected recovered assistant message, got %#v", assistant)
	}

	var captured llm.ChatRequest
	select {
	case captured = <-retryRequest:
	case <-time.After(time.Second):
		t.Fatal("retry request was not captured")
	}
	requestData, err := json.Marshal(captured.Messages)
	if err != nil {
		t.Fatal(err)
	}
	requestText := string(requestData)
	if !strings.Contains(requestText, "started repeating itself") || !strings.Contains(requestText, "already sent to the user") {
		t.Fatalf("expected retry guidance in request, got %s", requestText)
	}
}

func TestSystemServiceChatReportsTokenLimitFinishReason(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Partial plan:"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		)
	}))

	if _, err := service.SendChatMessage(workspaceID, "Plan this"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)
	if len(session.Messages) != 2 {
		t.Fatalf("expected user and assistant messages, got %#v", session.Messages)
	}
	assistant := session.Messages[1]
	if assistant.Content != "Partial plan:" {
		t.Fatalf("expected partial content to remain visible, got %q", assistant.Content)
	}
	if assistant.Status != "error" {
		t.Fatalf("expected token-limit finish to mark message as error, got %#v", assistant)
	}
	if !strings.Contains(assistant.Error, "token limit") {
		t.Fatalf("expected token-limit error, got %q", assistant.Error)
	}
}

func TestSystemServiceStopChatStreamCancelsAndReturnsIdle(t *testing.T) {
	root := t.TempDir()
	requestCanceled := make(chan struct{})
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(requestCanceled)
	}))

	if _, err := service.SendChatMessage(workspaceID, "Start"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	waitForChatContent(t, service, workspaceID, "partial")
	if _, err := service.StopChatStream(workspaceID); err != nil {
		t.Fatalf("stop chat: %v", err)
	}

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe cancellation")
	}
	session := waitForChatIdle(t, service, workspaceID)
	if session.Messages[1].Status != "canceled" {
		t.Fatalf("expected canceled assistant message, got %#v", session.Messages[1])
	}
}

func TestSystemServiceChatBadEndpointShowsRecoverableError(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := server.URL + "/v1"
	server.Close()

	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	settings := state.Settings
	settings.Endpoint = endpoint
	settings.Model = "test-model"
	settings.TimeoutSeconds = 1
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	if _, err := service.SendChatMessage(state.ActiveWorkspaceID, "Plan while offline"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, state.ActiveWorkspaceID)
	if len(session.Messages) != 2 {
		t.Fatalf("expected user and assistant messages, got %#v", session.Messages)
	}
	assistant := session.Messages[1]
	if assistant.Status != "error" {
		t.Fatalf("expected assistant error status, got %#v", assistant)
	}
	if !strings.Contains(assistant.Error, "Could not reach the LLM endpoint") {
		t.Fatalf("expected recoverable endpoint error, got %q", assistant.Error)
	}
}

func TestSystemServiceClearChatLeavesWorkspaceStateIntact(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Done"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	if _, err := service.SendChatMessage(workspaceID, "Hello"); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)

	session, err := service.ClearChat(workspaceID)
	if err != nil {
		t.Fatalf("clear chat: %v", err)
	}
	if len(session.Messages) != 0 || session.Busy {
		t.Fatalf("expected empty idle chat, got %#v", session)
	}

	state := service.LoadState()
	if len(state.Workspaces) != 1 || state.Workspaces[0].ID != workspaceID {
		t.Fatalf("expected workspace state to remain, got %#v", state.Workspaces)
	}
}

func TestSystemServiceExecutePlanExcludesHiddenChatState(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeChatResponse(t, w, `{"cards":[{"id":"phase-1","title":"Build visible work","description":"Use only visible plan text.","acceptanceCriteria":["Ready card exists"],"dependencies":[]}]}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Please plan the visible work.", Status: "complete"},
		{
			ID:        "msg-2",
			Role:      llm.RoleAssistant,
			Content:   "Visible approved plan: build the visible work card.",
			Reasoning: "hidden thinking must stay out",
			ToolCalls: []ChatToolActivity{{
				ID:     "call-1",
				Name:   "filesystem_list",
				Status: "complete",
				Result: "hidden tool result must stay out",
			}},
			Status: "complete",
		},
	}, []llm.Message{
		{Role: llm.RoleUser, Content: "Please plan the visible work."},
		{Role: llm.RoleAssistant, Content: "Visible approved plan", ToolCalls: []llm.ToolCall{{ID: "call-1"}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: "hidden tool result must stay out"},
	})

	if _, err := service.ExecutePlan(workspaceID); err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("expected system plus one combined user message, got %#v", captured.Messages)
	}
	if captured.Messages[0].Role != llm.RoleSystem || captured.Messages[1].Role != llm.RoleUser {
		t.Fatalf("expected decomposition to use system plus user prompt, got %#v", captured.Messages)
	}
	visiblePrompt := captured.Messages[1].Content
	for _, expected := range []string{
		"--- USER MESSAGE 1 ---",
		"Please plan the visible work.",
		"--- ASSISTANT MESSAGE 1 ---",
		"Visible approved plan: build the visible work card.",
	} {
		if !strings.Contains(visiblePrompt, expected) {
			t.Fatalf("expected visible transcript prompt to contain %q, got %q", expected, visiblePrompt)
		}
	}
	requestData, err := json.Marshal(captured.Messages)
	if err != nil {
		t.Fatal(err)
	}
	requestText := string(requestData)
	for _, hidden := range []string{"hidden thinking", "hidden tool result", "tool_call_id", "tool_calls", `"role":"assistant"`} {
		if strings.Contains(requestText, hidden) {
			t.Fatalf("decomposition request leaked %q in %s", hidden, requestText)
		}
	}
	if len(captured.Tools) != 0 || captured.ToolChoice != nil || captured.Stream {
		t.Fatalf("expected decomposition to avoid tools and streaming, got %#v", captured)
	}
}

func TestSystemServiceExecutePlanUsesCodingCentricPrompt(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeChatResponse(t, w, `{"cards":[{"id":"phase-1","title":"Update branch switching","description":"Change the Git branch switching behavior.","acceptanceCriteria":["Branch switching handles dirty worktrees correctly"],"dependencies":[]}]}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Fix branch switching.", Status: "complete"},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Plan: inspect the Git service, update branch switching behavior, then verify it.", Status: "complete"},
	}, nil)

	if _, err := service.ExecutePlan(workspaceID); err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("expected system plus user prompt, got %#v", captured.Messages)
	}
	systemPrompt := captured.Messages[0].Content
	for _, expected := range []string{
		"approved coding plans",
		"isolated programming work",
		"Do not create cards for opening, navigating, reading, inspecting, or finding files",
		"Do not create setup, planning, context-gathering, review, summary, build, test, or verify-only cards",
		"Echo automatically runs detected verification after each card",
		"Acceptance criteria should describe the desired code/product outcome, not process steps",
	} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("expected coding-centric decomposition prompt to include %q, got %q", expected, systemPrompt)
		}
	}
	if strings.Contains(systemPrompt, "testable slices") {
		t.Fatalf("expected prompt to avoid generic test-slice language, got %q", systemPrompt)
	}
	userPrompt := captured.Messages[1].Content
	if !strings.Contains(userPrompt, "coding Kanban cards") || !strings.Contains(userPrompt, "Return only the requested JSON") {
		t.Fatalf("expected concise coding user prompt, got %q", userPrompt)
	}
}

func TestSystemServiceExecutePlanIncludesImageLabelsWithoutImageData(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeChatResponse(t, w, `{"cards":[{"id":"phase-1","title":"Review screenshot","description":"Use the image-backed user request labels.","acceptanceCriteria":["Screenshot review is represented"],"dependencies":[]}]}`)
	}))
	imageDataURL := tinyPNGDataURL()
	visibleContent := "Review the visual issue.\n\nAttached media:\n- Image 1: screenshot.png (screens/screenshot.png), image/png, 12 B"
	seedChatPlan(service, workspaceID, []ChatMessage{
		{
			ID:      "msg-1",
			Role:    llm.RoleUser,
			Content: visibleContent,
			Images: []ChatImageAttachment{{
				ID:        "img-1",
				Source:    "workspace",
				Name:      "screenshot.png",
				Path:      "screens/screenshot.png",
				MediaType: "image/png",
				Bytes:     int64(len(tinyPNGBytes())),
				DataURL:   imageDataURL,
			}},
			Status: "complete",
		},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Approved plan: review and fix the visual issue.", Status: "complete"},
	}, []llm.Message{
		{
			Role:    llm.RoleUser,
			Content: visibleContent,
			ContentParts: []llm.MessageContentPart{
				llm.TextContentPart(visibleContent),
				llm.ImageURLContentPart(imageDataURL),
			},
		},
	})

	if _, err := service.ExecutePlan(workspaceID); err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if len(captured.Messages) != 2 {
		t.Fatalf("expected system plus combined visible prompt, got %#v", captured.Messages)
	}
	visiblePrompt := captured.Messages[1].Content
	if !strings.Contains(visiblePrompt, "Attached media:") || !strings.Contains(visiblePrompt, "screenshot.png") {
		t.Fatalf("expected visible media labels in decomposition prompt, got %q", visiblePrompt)
	}
	if strings.Contains(visiblePrompt, "data:image") || strings.Contains(visiblePrompt, "base64") {
		t.Fatalf("expected decomposition prompt to omit image data, got %q", visiblePrompt)
	}
}

func TestSystemServiceExecutePlanCreatesReadyCardsWithValidDependencies(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service, workspaceID := newDecompositionTestServiceWithStore(t, root, storePath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		writeChatResponse(t, w, `{"cards":[{"id":"phase-1","title":"Foundation","description":"Prepare the base slice.","acceptanceCriteria":["Foundation test passes"],"dependencies":[]},{"id":"phase-2","title":"Feature","description":"Build on the base slice.","acceptanceCriteria":["Feature is visible"],"dependencies":["phase-1"]}]}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Break this approved plan into cards.", Status: "complete"},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Approved plan: first foundation, then feature.", Status: "complete"},
	}, nil)

	board, err := service.ExecutePlan(workspaceID)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if len(board.Ready) != 2 || len(board.InProgress) != 0 || len(board.Blocked) != 0 || len(board.Done) != 0 {
		t.Fatalf("expected two ready cards only, got %#v", board)
	}
	if board.Ready[0].ID == "" || board.Ready[1].ID == "" || board.Ready[0].ID == board.Ready[1].ID {
		t.Fatalf("expected unique card ids, got %#v", board.Ready)
	}
	if got := board.Ready[1].Dependencies; len(got) != 1 || got[0] != board.Ready[0].ID {
		t.Fatalf("expected dependency to map to first card id, got %#v", got)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	reloadedBoard, err := reloaded.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load reloaded board: %v", err)
	}
	if len(reloadedBoard.Ready) != 2 {
		t.Fatalf("expected ready cards to persist after reload, got %#v", reloadedBoard)
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !strings.Contains(string(data), "kanbanCards") {
		t.Fatalf("expected state file to include kanban cards, got %s", data)
	}
}

func TestSystemServiceRestoresLatestChatSessionAcrossRestart(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(root)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	service.chatMu.Lock()
	service.chatSessions[workspaceID] = &chatSessionState{
		WorkspaceID: workspaceID,
		Messages: []ChatMessage{
			{ID: "msg-40", Role: llm.RoleUser, Content: "Keep this request", Status: "complete"},
			{ID: "msg-41", Role: llm.RoleAssistant, Content: "Keep this answer", Status: "complete"},
		},
		History: []llm.Message{
			{Role: llm.RoleUser, Content: "Keep this request"},
			{Role: llm.RoleAssistant, Content: "Keep this answer"},
		},
	}
	service.chatMu.Unlock()
	if err := service.persistChatSession(workspaceID); err != nil {
		t.Fatalf("persist chat: %v", err)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	session, err := reloaded.LoadChatSession(workspaceID)
	if err != nil {
		t.Fatalf("load restored chat: %v", err)
	}
	if len(session.Messages) != 2 || session.Messages[1].Content != "Keep this answer" || session.Busy || session.StreamID != "" {
		t.Fatalf("expected latest chat session to restore idle, got %#v", session)
	}
	if next := reloaded.nextChatID("msg"); next != "msg-42" {
		t.Fatalf("expected restored IDs to remain unique, got %q", next)
	}

	if _, err := reloaded.ClearChat(workspaceID); err != nil {
		t.Fatalf("clear restored chat: %v", err)
	}
	cleared := NewSystemServiceWithStorePath(storePath)
	session, err = cleared.LoadChatSession(workspaceID)
	if err != nil {
		t.Fatalf("load cleared chat: %v", err)
	}
	if len(session.Messages) != 0 {
		t.Fatalf("expected cleared chat snapshot to persist, got %#v", session.Messages)
	}
}

func TestSystemServiceExecutePlanAcceptsJSONWithSurroundingText(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		writeChatResponse(t, w, "Let's convert that plan into cards.\n\n```json\n{\"cards\":[{\"id\":\"phase-1\",\"title\":\"Apply neon theme\",\"description\":\"Update the editor theme colors to use neon values.\",\"acceptanceCriteria\":[\"Editor theme uses neon colors\"],\"dependencies\":[]}]}\n```")
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Break this approved plan into cards.", Status: "complete"},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Approved plan: update the editor theme colors to neon.", Status: "complete"},
	}, nil)

	board, err := service.ExecutePlan(workspaceID)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if len(board.Ready) != 1 {
		t.Fatalf("expected one ready card, got %#v", board)
	}
	if board.Ready[0].Title != "Apply neon theme" {
		t.Fatalf("expected extracted JSON card title, got %#v", board.Ready[0])
	}
}

func TestSystemServiceExecutePlanSkipsNonJSONBracePreamble(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		writeChatResponse(t, w, "Thinking note: {based on the visible plan}\n\n{\"cards\":[{\"id\":\"phase-1\",\"title\":\"Apply neon theme\",\"description\":\"Update the editor theme colors to use neon values.\",\"acceptanceCriteria\":[\"Editor theme uses neon colors\"],\"dependencies\":[]}]}")
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Break this approved plan into cards.", Status: "complete"},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Approved plan: update the editor theme colors to neon.", Status: "complete"},
	}, nil)

	board, err := service.ExecutePlan(workspaceID)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	if len(board.Ready) != 1 || board.Ready[0].Title != "Apply neon theme" {
		t.Fatalf("expected parser to skip prose braces and find card JSON, got %#v", board.Ready)
	}
}

func TestSystemServiceExecutePlanRejectsInvalidOutputWithoutPartialCards(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCompleteRequest(t, r)
		writeChatResponse(t, w, `{"cards":[{"id":"phase-1","title":"Foundation","description":"Prepare the base slice.","acceptanceCriteria":["Done"],"dependencies":["missing-phase"]}]}`)
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Break this approved plan into cards.", Status: "complete"},
		{ID: "msg-2", Role: llm.RoleAssistant, Content: "Approved plan: create one card.", Status: "complete"},
	}, nil)

	if _, err := service.ExecutePlan(workspaceID); err == nil {
		t.Fatal("expected invalid dependency to be rejected")
	}
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load board: %v", err)
	}
	if len(board.Ready) != 0 {
		t.Fatalf("expected no partial cards, got %#v", board.Ready)
	}
}

func TestSystemServiceExecutePlanRequiresVisibleAssistantPlan(t *testing.T) {
	root := t.TempDir()
	called := false
	service, workspaceID := newDecompositionTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatal("decomposition endpoint should not be called without a visible plan")
	}))
	seedChatPlan(service, workspaceID, []ChatMessage{
		{ID: "msg-1", Role: llm.RoleUser, Content: "Please make cards.", Status: "complete"},
	}, nil)

	_, err := service.ExecutePlan(workspaceID)
	if err == nil {
		t.Fatal("expected insufficient plan error")
	}
	if called {
		t.Fatal("unexpected decomposition request")
	}
	if !strings.Contains(err.Error(), "visible plan") {
		t.Fatalf("expected helpful visible plan error, got %q", err.Error())
	}
	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load board: %v", err)
	}
	if len(board.Ready) != 0 {
		t.Fatalf("expected no cards, got %#v", board.Ready)
	}
}

func newChatTestService(t *testing.T, workspacePath string, handler http.Handler) (*SystemService, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	settings := state.Settings
	settings.Endpoint = server.URL + "/v1"
	settings.Model = "test-model"
	settings.TimeoutSeconds = 10
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return service, state.ActiveWorkspaceID
}

func newDecompositionTestService(t *testing.T, workspacePath string, handler http.Handler) (*SystemService, string) {
	t.Helper()
	return newDecompositionTestServiceWithStore(t, workspacePath, filepath.Join(t.TempDir(), "state.json"), handler)
}

func newDecompositionTestServiceWithStore(t *testing.T, workspacePath string, storePath string, handler http.Handler) (*SystemService, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	settings := state.Settings
	settings.Endpoint = server.URL + "/v1"
	settings.Model = "test-model"
	settings.TimeoutSeconds = 10
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	return service, state.ActiveWorkspaceID
}

func seedChatPlan(service *SystemService, workspaceID string, messages []ChatMessage, history []llm.Message) {
	service.chatMu.Lock()
	defer service.chatMu.Unlock()
	service.chatSessions[workspaceID] = &chatSessionState{
		WorkspaceID: workspaceID,
		Messages:    messages,
		History:     history,
	}
}

func assertChatStreamRequest(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Fatalf("expected POST, got %s", r.Method)
	}
	if r.URL.Path != "/v1/chat/completions" {
		t.Fatalf("expected chat completions path, got %s", r.URL.Path)
	}
	if r.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("expected event-stream accept header, got %q", r.Header.Get("Accept"))
	}
}

func assertCompleteRequest(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Fatalf("expected POST, got %s", r.Method)
	}
	if r.URL.Path != "/v1/chat/completions" {
		t.Fatalf("expected chat completions path, got %s", r.URL.Path)
	}
	if r.Header.Get("Accept") != "application/json" {
		t.Fatalf("expected JSON accept header, got %q", r.Header.Get("Accept"))
	}
}

func writeChatResponse(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	_ = json.NewEncoder(w).Encode(llm.ChatResponse{
		Choices: []llm.ChatChoice{{
			Index:   0,
			Message: llm.Message{Role: llm.RoleAssistant, Content: content},
		}},
	})
}

func writeSSE(t *testing.T, w http.ResponseWriter, payloads ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, payload := range payloads {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			t.Fatalf("write stream: %v", err)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func waitForChatIdle(t *testing.T, service *SystemService, workspaceID string) ChatSession {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, err := service.LoadChatSession(workspaceID)
		if err != nil {
			t.Fatalf("load chat: %v", err)
		}
		if !session.Busy {
			return session
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for chat to become idle")
	return ChatSession{}
}

func waitForChatContent(t *testing.T, service *SystemService, workspaceID string, content string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, err := service.LoadChatSession(workspaceID)
		if err != nil {
			t.Fatalf("load chat: %v", err)
		}
		if len(session.Messages) > 1 && strings.Contains(session.Messages[1].Content, content) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for chat content %q", content)
}

func chatRequestToolNames(request llm.ChatRequest) map[string]bool {
	names := make(map[string]bool, len(request.Tools))
	for _, tool := range request.Tools {
		names[tool.Function.Name] = true
	}
	return names
}

func assertPlanModeChatRequest(t *testing.T, request llm.ChatRequest) {
	t.Helper()
	if len(request.Messages) == 0 || request.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected system message first, got %#v", request.Messages)
	}
	prompt := request.Messages[0].Content
	for _, expected := range []string{
		"planning changes only",
		"do not make workspace changes",
		"available read-only tools",
		"workspace_context",
		"aroundLine",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected plan-mode prompt to include %q, got %q", expected, prompt)
		}
	}

	names := chatRequestToolNames(request)
	for _, expected := range []string{"filesystem_list", "filesystem_read_image", "filesystem_read_video", "filesystem_read_text", "filesystem_search_text", "filesystem_stat", "workspace_context"} {
		if !names[expected] {
			t.Fatalf("expected plan mode to include read-only tool %s, got %#v", expected, names)
		}
	}
	for _, denied := range []string{"filesystem_create_text", "filesystem_delete_file", "filesystem_edit_text", "shell_command"} {
		if names[denied] {
			t.Fatalf("expected plan mode to exclude mutating tool %s, got %#v", denied, names)
		}
	}
	if request.ToolChoice != "auto" {
		t.Fatalf("expected auto tool choice, got %#v", request.ToolChoice)
	}
}

func tinyPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
}

func tinyPNGDataURL() string {
	return chatImageDataURL("image/png", tinyPNGBytes())
}

func workspaceRootLabel(t *testing.T, service *SystemService, workspaceID string) string {
	t.Helper()
	state := service.LoadState()
	for _, workspace := range state.Workspaces {
		if workspace.ID != workspaceID {
			continue
		}
		if len(workspace.Folders) == 0 {
			t.Fatalf("workspace %s has no folders", workspaceID)
		}
		return workspace.Folders[0].Label
	}
	t.Fatalf("workspace %s not found", workspaceID)
	return ""
}

func labeledTestPath(t *testing.T, service *SystemService, workspaceID string, path string) string {
	t.Helper()
	return workspaceRootLabel(t, service, workspaceID) + "/" + strings.TrimLeft(strings.ReplaceAll(path, "\\", "/"), "/")
}

func assertSystemPromptOperatingContext(t *testing.T, content string, workspaceRoot string) {
	t.Helper()

	for _, expected := range []string{
		"Operating context:",
		"- Operating system: " + runtime.GOOS,
		"- Default shell: ",
		"- Shell command guidance: ",
		"- OS user: ",
		"- Workspace folders:",
		workspaceRoot + " [available, AGENTS.md enabled]",
		"- Path convention: tool paths must be labeled workspace paths",
		"Start every concrete file or directory path with one of the listed workspace folder labels",
		"Example: use " + normalizeWorkspaceFolderLabel(filepath.Base(workspaceRoot)) + "/frontend/src/main.ts, not frontend/src/main.ts",
		"- Current time: ",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected system prompt to include %q, got %q", expected, content)
		}
	}

	username := lineValue(content, "- OS user: ")
	if username == "" {
		t.Fatalf("expected system prompt to include a non-empty OS user, got %q", content)
	}
	if shell := lineValue(content, "- Default shell: "); shell == "" {
		t.Fatalf("expected system prompt to include default shell, got %q", content)
	}
	if guidance := lineValue(content, "- Shell command guidance: "); guidance == "" {
		t.Fatalf("expected system prompt to include shell command guidance, got %q", content)
	} else if runtime.GOOS == "windows" {
		for _, expected := range []string{"PowerShell-native commands", "not cmd.exe", "avoid CMD syntax", "$env:VAR"} {
			if !strings.Contains(guidance, expected) {
				t.Fatalf("expected Windows shell guidance to include %q, got %q", expected, guidance)
			}
		}
	}

	currentTime := lineValue(content, "- Current time: ")
	if currentTime == "" {
		t.Fatalf("expected system prompt to include current time, got %q", content)
	}
	if _, err := time.Parse(time.RFC3339, currentTime); err != nil {
		t.Fatalf("expected current time to be RFC3339, got %q: %v", currentTime, err)
	}
}

func lineValue(content string, prefix string) string {
	for _, line := range strings.Split(content, "\n") {
		if value, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func TestSystemServiceAddWorkspaceCreatesAndSelectsFolder(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "project")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}

	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}

	if len(state.Workspaces) != 1 {
		t.Fatalf("expected one workspace, got %d", len(state.Workspaces))
	}
	workspace := state.Workspaces[0]
	if workspace.ID == "" {
		t.Fatal("expected workspace id")
	}
	if workspace.DisplayName != "project" {
		t.Fatalf("expected display name from folder, got %q", workspace.DisplayName)
	}
	if len(workspace.Folders) != 1 {
		t.Fatalf("expected one workspace folder, got %d", len(workspace.Folders))
	}
	if workspace.Folders[0].Path != filepath.Clean(workspacePath) {
		t.Fatalf("expected folder path %q, got %q", filepath.Clean(workspacePath), workspace.Folders[0].Path)
	}
	if workspace.Folders[0].Label != "project" {
		t.Fatalf("expected folder label project, got %q", workspace.Folders[0].Label)
	}
	if !workspace.Folders[0].UseAgents {
		t.Fatal("expected AGENTS.md usage to default on")
	}
	if state.ActiveWorkspaceID != workspace.ID {
		t.Fatalf("expected active workspace id %q, got %q", workspace.ID, state.ActiveWorkspaceID)
	}
	if !workspace.Active {
		t.Fatal("expected added workspace to be active")
	}
	if workspace.Missing {
		t.Fatalf("expected workspace to exist, got error %q", workspace.Error)
	}
}

func TestSystemServiceDuplicateWorkspaceSelectsExisting(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	firstState, err := service.AddWorkspace(first)
	if err != nil {
		t.Fatalf("add first workspace: %v", err)
	}
	firstID := firstState.ActiveWorkspaceID
	if _, err := service.AddWorkspace(second); err != nil {
		t.Fatalf("add second workspace: %v", err)
	}

	state, err := service.AddWorkspace(filepath.Join(first, "."))
	if err != nil {
		t.Fatalf("add duplicate workspace: %v", err)
	}
	if len(state.Workspaces) != 2 {
		t.Fatalf("expected duplicate to keep two workspaces, got %d", len(state.Workspaces))
	}
	if state.ActiveWorkspaceID != firstID {
		t.Fatalf("expected duplicate to select existing workspace %q, got %q", firstID, state.ActiveWorkspaceID)
	}
	for _, workspace := range state.Workspaces {
		if workspace.ID == firstID && !workspace.Active {
			t.Fatal("expected duplicate workspace to be active")
		}
	}
}

func TestSystemServiceWorkspaceListPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	if _, err := service.AddWorkspace(first); err != nil {
		t.Fatalf("add first workspace: %v", err)
	}
	state, err := service.AddWorkspace(second)
	if err != nil {
		t.Fatalf("add second workspace: %v", err)
	}
	activeID := state.ActiveWorkspaceID

	reloaded := NewSystemServiceWithStorePath(storePath).LoadState()
	if len(reloaded.Workspaces) != 2 {
		t.Fatalf("expected two persisted workspaces, got %d", len(reloaded.Workspaces))
	}
	if reloaded.ActiveWorkspaceID != activeID {
		t.Fatalf("expected active workspace %q after restart, got %q", activeID, reloaded.ActiveWorkspaceID)
	}
	for _, workspace := range reloaded.Workspaces {
		if workspace.ID == activeID && !workspace.Active {
			t.Fatal("expected active workspace flag after restart")
		}
	}
}

func TestSystemServiceMissingWorkspaceShowsRecoverableState(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "project")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}

	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	if err := os.RemoveAll(workspacePath); err != nil {
		t.Fatal(err)
	}
	missing := NewSystemServiceWithStorePath(storePath).LoadState()
	if len(missing.Workspaces) != 1 {
		t.Fatalf("expected one workspace, got %d", len(missing.Workspaces))
	}
	if missing.Workspaces[0].ID != workspaceID {
		t.Fatalf("expected workspace %q, got %q", workspaceID, missing.Workspaces[0].ID)
	}
	if !missing.Workspaces[0].Missing {
		t.Fatal("expected deleted folder to be marked missing")
	}
	if missing.Workspaces[0].Error == "" {
		t.Fatal("expected recoverable workspace error")
	}
	if len(missing.Workspaces[0].Folders) != 1 || !missing.Workspaces[0].Folders[0].Missing {
		t.Fatalf("expected deleted folder to be marked missing, got %#v", missing.Workspaces[0].Folders)
	}

	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	recovered := NewSystemServiceWithStorePath(storePath).LoadState()
	if recovered.Workspaces[0].Missing {
		t.Fatalf("expected restored folder to recover, got %q", recovered.Workspaces[0].Error)
	}
	if recovered.Workspaces[0].Folders[0].Missing {
		t.Fatalf("expected restored folder status to recover, got %q", recovered.Workspaces[0].Folders[0].Error)
	}
}

func TestSystemServiceWorkspaceFoldersCanBeRemovedAndAgentsToggled(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "project")
	second := filepath.Join(root, "project-copy")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(first)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	state, err = service.AddWorkspaceFolder(workspaceID, second)
	if err != nil {
		t.Fatalf("add workspace folder: %v", err)
	}
	workspace := state.Workspaces[0]
	if len(workspace.Folders) != 2 {
		t.Fatalf("expected two folders, got %#v", workspace.Folders)
	}
	if workspace.Folders[0].Label == workspace.Folders[1].Label {
		t.Fatalf("expected unique labels, got %#v", workspace.Folders)
	}

	state, err = service.SetWorkspaceFolderUseAgents(workspaceID, workspace.Folders[0].ID, false)
	if err != nil {
		t.Fatalf("toggle agents: %v", err)
	}
	if state.Workspaces[0].Folders[0].UseAgents {
		t.Fatal("expected AGENTS.md usage to be disabled")
	}

	state, err = service.RemoveWorkspaceFolder(workspaceID, workspace.Folders[0].ID)
	if err != nil {
		t.Fatalf("remove first folder: %v", err)
	}
	state, err = service.RemoveWorkspaceFolder(workspaceID, state.Workspaces[0].Folders[0].ID)
	if err != nil {
		t.Fatalf("remove last folder: %v", err)
	}
	if len(state.Workspaces[0].Folders) != 0 {
		t.Fatalf("expected blank workspace, got %#v", state.Workspaces[0].Folders)
	}
	if state.Workspaces[0].Missing {
		t.Fatalf("expected blank workspace to remain available, got %q", state.Workspaces[0].Error)
	}
	if _, _, err := service.workspaceAndSettings(workspaceID); err != nil {
		t.Fatalf("expected blank workspace to be available: %v", err)
	}
}

func TestSystemServiceDefaultsAndSettingsPersistence(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")
	service := NewSystemServiceWithStorePath(storePath)

	state := service.LoadState()
	if state.Settings.Endpoint != llm.DefaultEndpoint {
		t.Fatalf("expected default endpoint, got %q", state.Settings.Endpoint)
	}
	if state.Settings.Model != llm.DefaultModel {
		t.Fatalf("expected default model, got %q", state.Settings.Model)
	}
	if state.Settings.MinP != 0 {
		t.Fatalf("expected default min-p 0, got %v", state.Settings.MinP)
	}
	if state.Settings.PresencePenalty != 1.5 {
		t.Fatalf("expected default presence penalty 1.5, got %v", state.Settings.PresencePenalty)
	}
	if state.Settings.RepetitionPenalty != llm.DefaultSettings().RepetitionPenalty {
		t.Fatalf("expected default repetition penalty %v, got %v", llm.DefaultSettings().RepetitionPenalty, state.Settings.RepetitionPenalty)
	}
	if state.Settings.SearxngURL != llm.DefaultSearxngURL {
		t.Fatalf("expected default SearXNG URL, got %q", state.Settings.SearxngURL)
	}
	if state.Settings.DisableNotificationSounds {
		t.Fatal("expected notification sounds to be enabled by default")
	}
	if state.Settings.ThinkingCorrection {
		t.Fatal("expected thinking correction to be disabled by default")
	}
	if state.Settings.ThinkingTokenBudget != -1 {
		t.Fatalf("expected default thinking token budget -1, got %d", state.Settings.ThinkingTokenBudget)
	}
	if len(state.Settings.Theme.Light) != 0 || len(state.Settings.Theme.Dark) != 0 {
		t.Fatal("expected theme overrides to be empty by default")
	}

	settings := state.Settings
	settings.Endpoint = "https://example.test/v1"
	settings.Model = "test-model"
	settings.SearxngURL = "https://search.example.test/"
	settings.Temperature = 0.2
	settings.TopK = 16
	settings.TopP = 0.8
	settings.MinP = 0.1
	settings.ContextLength = 8192
	settings.MaxTokens = 1024
	settings.PresencePenalty = 1.25
	settings.RepetitionPenalty = 1.05
	settings.ThinkingTokenBudget = 0
	settings.ThinkingCorrection = true
	settings.DisableNotificationSounds = true
	settings.Theme.Light = map[string]string{
		"accent":      "#123ABC",
		"futureToken": "#abc",
	}
	settings.Theme.Dark = map[string]string{
		"surface": "#112233",
	}

	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	reloaded := NewSystemServiceWithStorePath(storePath).LoadState()
	if reloaded.Settings.Endpoint != settings.Endpoint {
		t.Fatalf("expected persisted endpoint, got %q", reloaded.Settings.Endpoint)
	}
	if reloaded.Settings.Model != settings.Model {
		t.Fatalf("expected persisted model, got %q", reloaded.Settings.Model)
	}
	if reloaded.Settings.SearxngURL != settings.SearxngURL {
		t.Fatalf("expected persisted SearXNG URL, got %q", reloaded.Settings.SearxngURL)
	}
	if reloaded.Settings.MaxTokens != settings.MaxTokens {
		t.Fatalf("expected persisted max tokens, got %d", reloaded.Settings.MaxTokens)
	}
	if reloaded.Settings.MinP != settings.MinP {
		t.Fatalf("expected persisted min-p, got %v", reloaded.Settings.MinP)
	}
	if reloaded.Settings.PresencePenalty != settings.PresencePenalty {
		t.Fatalf("expected persisted presence penalty, got %v", reloaded.Settings.PresencePenalty)
	}
	if reloaded.Settings.RepetitionPenalty != settings.RepetitionPenalty {
		t.Fatalf("expected persisted repetition penalty, got %v", reloaded.Settings.RepetitionPenalty)
	}
	if !reloaded.Settings.DisableNotificationSounds {
		t.Fatal("expected persisted disabled notification sounds setting")
	}
	if !reloaded.Settings.ThinkingCorrection {
		t.Fatal("expected persisted thinking correction setting")
	}
	if reloaded.Settings.ThinkingTokenBudget != settings.ThinkingTokenBudget {
		t.Fatalf("expected persisted thinking token budget, got %d", reloaded.Settings.ThinkingTokenBudget)
	}
	if reloaded.Settings.Theme.Light["accent"] != "#123abc" {
		t.Fatalf("expected normalized light accent override, got %q", reloaded.Settings.Theme.Light["accent"])
	}
	if reloaded.Settings.Theme.Light["futureToken"] != "#aabbcc" {
		t.Fatalf("expected normalized future token override, got %q", reloaded.Settings.Theme.Light["futureToken"])
	}
	if reloaded.Settings.Theme.Dark["surface"] != "#112233" {
		t.Fatalf("expected persisted dark surface override, got %q", reloaded.Settings.Theme.Dark["surface"])
	}
}

func TestSystemServiceMigratesLegacyDisabledThinking(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(storePath, []byte(`{"settings":{"enableThinking":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	state := NewSystemServiceWithStorePath(storePath).LoadState()
	if state.Settings.ThinkingTokenBudget != 0 {
		t.Fatalf("expected legacy disabled thinking to migrate to budget 0, got %d", state.Settings.ThinkingTokenBudget)
	}

	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read migrated state: %v", err)
	}
	if strings.Contains(string(data), "enableThinking") {
		t.Fatalf("expected legacy enableThinking key to be removed after migration, got %s", data)
	}
	if !strings.Contains(string(data), `"thinkingTokenBudget": 0`) {
		t.Fatalf("expected migrated state to persist thinking token budget 0, got %s", data)
	}
}

func TestSystemServiceMigratesOnlyDisabledLegacyWebAccessDefaultPort(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    int
	}{
		{name: "disabled default migrates", enabled: false, want: defaultWebAccessPort},
		{name: "enabled explicit port is preserved", enabled: true, want: legacyWebAccessPort},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "state.json")
			payload := fmt.Sprintf(`{"webAccess":{"enabled":%t,"bindHost":"0.0.0.0","port":%d,"accessToken":"token"}}`, test.enabled, legacyWebAccessPort)
			if err := os.WriteFile(storePath, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}

			state := NewSystemServiceWithStorePath(storePath).LoadState()
			if state.WebAccess.Port != test.want {
				t.Fatalf("expected web access port %d, got %d", test.want, state.WebAccess.Port)
			}

			data, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatalf("read migrated state: %v", err)
			}
			var persisted AppState
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatalf("decode persisted state: %v", err)
			}
			if persisted.WebAccess.Port != test.want {
				t.Fatalf("expected persisted web access port %d, got %d in %s", test.want, persisted.WebAccess.Port, data)
			}
		})
	}
}

func TestSystemServiceRejectsInvalidSettings(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	settings := service.LoadState().Settings

	settings.Endpoint = ""
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected empty endpoint to be rejected")
	}

	settings.Endpoint = "notaurl"
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected invalid endpoint to be rejected")
	}

	settings.Endpoint = llm.DefaultEndpoint
	settings.Model = ""
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected empty model to be rejected")
	}

	settings.Model = llm.DefaultModel
	settings.SearxngURL = "notaurl"
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected invalid SearXNG URL to be rejected")
	}

	settings.SearxngURL = llm.DefaultSearxngURL
	settings.MinP = -0.1
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected invalid min-p to be rejected")
	}

	settings.MinP = 0
	settings.RepetitionPenalty = -0.1
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected invalid repetition penalty to be rejected")
	}

	settings.RepetitionPenalty = llm.DefaultSettings().RepetitionPenalty
	settings.ThinkingTokenBudget = -2
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected invalid thinking token budget to be rejected")
	}

	settings.ThinkingTokenBudget = -1
	settings.Theme.Light = map[string]string{"accent": "red"}
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected invalid theme color to be rejected")
	}

	settings.Theme.Light = map[string]string{"": "#123456"}
	if _, err := service.SaveSettings(settings); err == nil {
		t.Fatal("expected invalid theme token to be rejected")
	}
}

func TestSystemServiceDeleteWorkspaceUpdatesActiveState(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	service := NewSystemServiceWithStorePath(filepath.Join(root, "state.json"))
	state, err := service.AddWorkspace(first)
	if err != nil {
		t.Fatalf("add first workspace: %v", err)
	}
	firstID := state.ActiveWorkspaceID
	state, err = service.AddWorkspace(second)
	if err != nil {
		t.Fatalf("add second workspace: %v", err)
	}
	secondID := state.ActiveWorkspaceID
	if firstID == secondID {
		t.Fatal("expected unique workspace ids")
	}

	state, err = service.DeleteWorkspace(secondID)
	if err != nil {
		t.Fatalf("delete active workspace: %v", err)
	}
	if len(state.Workspaces) != 1 {
		t.Fatalf("expected one workspace, got %d", len(state.Workspaces))
	}
	if state.ActiveWorkspaceID != firstID {
		t.Fatalf("expected active workspace to fall back to first, got %q", state.ActiveWorkspaceID)
	}

	state, err = service.DeleteWorkspace(firstID)
	if err != nil {
		t.Fatalf("delete final workspace: %v", err)
	}
	if len(state.Workspaces) != 0 {
		t.Fatalf("expected no workspaces, got %d", len(state.Workspaces))
	}
	if state.ActiveWorkspaceID != "" {
		t.Fatalf("expected no active workspace, got %q", state.ActiveWorkspaceID)
	}
}

func TestSystemServiceReorderWorkspacesPersistsOrder(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	third := filepath.Join(root, "third")
	for _, path := range []string{first, second, third} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := service.AddWorkspace(path); err != nil {
			t.Fatalf("add workspace: %v", err)
		}
	}

	state := service.LoadState()
	ids := []string{
		state.Workspaces[2].ID,
		state.Workspaces[0].ID,
		state.Workspaces[1].ID,
	}
	state, err := service.ReorderWorkspaces(ids)
	if err != nil {
		t.Fatalf("reorder workspaces: %v", err)
	}
	for i, id := range ids {
		if state.Workspaces[i].ID != id {
			t.Fatalf("expected workspace %d to be %q, got %q", i, id, state.Workspaces[i].ID)
		}
	}
	if state.ActiveWorkspaceID != ids[0] {
		t.Fatalf("expected active workspace to remain %q, got %q", ids[0], state.ActiveWorkspaceID)
	}

	reloaded := NewSystemServiceWithStorePath(storePath).LoadState()
	for i, id := range ids {
		if reloaded.Workspaces[i].ID != id {
			t.Fatalf("expected persisted workspace %d to be %q, got %q", i, id, reloaded.Workspaces[i].ID)
		}
	}
	if _, err := service.ReorderWorkspaces([]string{ids[0], ids[0], ids[1]}); err == nil {
		t.Fatal("expected duplicate workspace id to fail")
	}
	if _, err := service.ReorderWorkspaces([]string{ids[0], ids[1]}); err == nil {
		t.Fatal("expected incomplete workspace order to fail")
	}
}

func TestSystemServiceSetWorkspaceLetterPersists(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	state, err = service.SetWorkspaceLetter(workspaceID, "  zed ")
	if err != nil {
		t.Fatalf("set workspace letter: %v", err)
	}
	if got := state.Workspaces[0].Letter; got != "ZED" {
		t.Fatalf("expected normalized letter ZED, got %q", got)
	}

	reloaded := NewSystemServiceWithStorePath(storePath).LoadState()
	if got := reloaded.Workspaces[0].Letter; got != "ZED" {
		t.Fatalf("expected persisted letter ZED, got %q", got)
	}

	state, err = service.SetWorkspaceLetter(workspaceID, " ")
	if err != nil {
		t.Fatalf("clear workspace letter: %v", err)
	}
	if got := state.Workspaces[0].Letter; got != "" {
		t.Fatalf("expected cleared letter, got %q", got)
	}
}

func TestSystemServiceWorkspaceDefaultPlanModePersists(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	if !state.Workspaces[0].DefaultPlanMode {
		t.Fatal("expected new workspace to default plan mode on")
	}

	state, err = service.SetWorkspaceDefaultPlanMode(workspaceID, false)
	if err != nil {
		t.Fatalf("set default plan mode: %v", err)
	}
	if state.Workspaces[0].DefaultPlanMode {
		t.Fatal("expected default plan mode to be disabled")
	}

	reloaded := NewSystemServiceWithStorePath(storePath).LoadState()
	if reloaded.Workspaces[0].DefaultPlanMode {
		t.Fatal("expected disabled default plan mode to persist")
	}
}

func TestSystemServiceLegacyWorkspaceDefaultPlanModeDefaultsOn(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "state.json")
	payload := fmt.Sprintf(`{"activeWorkspaceId":"workspace-1","workspaces":[{"id":"workspace-1","folders":[{"id":"folder-1","label":"workspace","path":%q,"useAgents":true}],"displayName":"workspace"}]}`, workspacePath)
	if err := os.WriteFile(storePath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	state := NewSystemServiceWithStorePath(storePath).LoadState()
	if !state.Workspaces[0].DefaultPlanMode {
		t.Fatal("expected legacy workspace to default plan mode on")
	}
}

func TestSystemServiceChatHistoryUpToLocked(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	workspacePath := t.TempDir()
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	history := []llm.Message{
		{Role: llm.RoleUser, Content: "hello"},
		{Role: llm.RoleAssistant, Content: "hi there"},
		{Role: llm.RoleUser, Content: "second question"},
		{Role: llm.RoleAssistant, Content: "second answer"},
	}
	seedChatPlan(service, workspaceID, nil, history)

	tests := []struct {
		name    string
		index   int
		wantLen int
		wantNil bool
	}{
		{"zero returns nil", 0, 0, true},
		{"one returns first message", 1, 1, false},
		{"two returns first two", 2, 2, false},
		{"full length returns all", 4, 4, false},
		{"excess clamps to full", 10, 4, false},
		{"negative clamps to zero", -1, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := service.chatHistoryUpToLocked(workspaceID, tc.index)
			if tc.wantNil && result != nil {
				t.Fatalf("expected nil, got %d messages", len(result))
			}
			if !tc.wantNil && result == nil {
				t.Fatalf("expected non-nil, got nil")
			}
			if len(result) != tc.wantLen {
				t.Fatalf("expected %d messages, got %d", tc.wantLen, len(result))
			}
			// Verify returned slice is a copy (mutating it doesn't affect internal state).
			if len(result) > 0 {
				result[0] = llm.Message{Role: llm.RoleUser, Content: "mutated"}
			}
			internal := service.chatHistory(workspaceID)
			if len(internal) > 0 && internal[0].Content == "mutated" {
				t.Fatal("history was mutated by caller")
			}
		})
	}

	// Non-existent workspace returns nil.
	result := service.chatHistoryUpToLocked("nonexistent", 2)
	if result != nil {
		t.Fatalf("expected nil for nonexistent workspace, got %d messages", len(result))
	}
}

func tinyMP4Bytes() []byte {
	return []byte{0, 0, 0, 8, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
}

func tinyWebMBytes() []byte {
	return []byte{0x1a, 0x45, 0xdf, 0xa3, 0x00, 0x00, 0x00}
}

func tinyMOVBytes() []byte {
	return []byte{0, 0, 0, 8, 'm', 'o', 'o', 'v'}
}

func tinyMP4DataURL() string {
	return chatMediaDataURL("video/mp4", tinyMP4Bytes())
}

func tinyWebMDataURL() string {
	return chatMediaDataURL("video/webm", tinyWebMBytes())
}

func TestDetectChatVideoMediaType(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		wantType  string
		wantError bool
	}{
		{"mp4 ftyp header", tinyMP4Bytes(), "video/mp4", false},
		{"webm EBML header", tinyWebMBytes(), "video/webm", false},
		{"mov moov header", tinyMOVBytes(), "video/quicktime", false},
		{"empty data", []byte{}, "", true},
		{"unsupported format", []byte{0x00, 0x01, 0x02}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectChatVideoMediaType(tc.data)
			if tc.wantError {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantType {
				t.Fatalf("expected %q, got %q", tc.wantType, got)
			}
		})
	}
}

func TestSupportedChatVideoMediaType(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		want      bool
	}{
		{"mp4", "video/mp4", true},
		{"webm", "video/webm", true},
		{"quicktime", "video/quicktime", true},
		{"MP4 uppercase", "VIDEO/MP4", true},
		{"image/png", "image/png", false},
		{"unknown", "video/avi", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := supportedChatVideoMediaType(tc.mediaType)
			if got != tc.want {
				t.Fatalf("expected %v for %q, got %v", tc.want, tc.mediaType, got)
			}
		})
	}
}

func TestChatVideoExtension(t *testing.T) {
	tests := []struct {
		mediaType string
		want      string
	}{
		{"video/mp4", ".mp4"},
		{"video/webm", ".webm"},
		{"video/quicktime", ".mov"},
		{"image/png", ""},
	}
	for _, tc := range tests {
		t.Run(tc.mediaType, func(t *testing.T) {
			got := chatVideoExtension(tc.mediaType)
			if got != tc.want {
				t.Fatalf("expected %q for %s, got %q", tc.want, tc.mediaType, got)
			}
		})
	}
}

func TestChatVideoPathKind(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"clip.mp4", "supported"},
		{"video.webm", "supported"},
		{"recording.mov", "supported"},
		{"photo.png", ""},
		{"readme.txt", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := chatVideoPathKind(tc.path)
			if got != tc.want {
				t.Fatalf("expected %q for %s, got %q", tc.want, tc.path, got)
			}
		})
	}
}

func TestValidateChatVideos(t *testing.T) {
	// Within limits
	small := []ChatVideoAttachment{{ID: "v1", MediaType: "video/mp4", Bytes: 1024}}
	if err := validateChatVideos(small); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}

	// Too many videos
	many := make([]ChatVideoAttachment, maxChatVideoAttachments+1)
	for i := range many {
		many[i] = ChatVideoAttachment{ID: fmt.Sprintf("v%d", i), MediaType: "video/mp4", Bytes: 1024}
	}
	if err := validateChatVideos(many); err == nil {
		t.Fatal("expected too-many error")
	}

	// Video exceeds size limit
	huge := []ChatVideoAttachment{{ID: "v1", Name: "huge.mp4", MediaType: "video/mp4", Bytes: maxChatVideoBytes + 1}}
	if err := validateChatVideos(huge); err == nil {
		t.Fatal("expected size error")
	}

	// Unsupported format
	bad := []ChatVideoAttachment{{ID: "v1", MediaType: "video/avi"}}
	if err := validateChatVideos(bad); err == nil {
		t.Fatal("expected unsupported format error")
	}
}

func TestParseChatVideoDataURL(t *testing.T) {
	// Valid MP4 data URL
	dataURL := tinyMP4DataURL()
	mediaType, data, err := parseChatVideoDataURL(dataURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mediaType != "video/mp4" {
		t.Fatalf("expected video/mp4, got %q", mediaType)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data")
	}

	// Invalid: not a data URL
	_, _, err = parseChatVideoDataURL("http://example.com/video.mp4")
	if err == nil {
		t.Fatal("expected error for non-data URL")
	}

	// Invalid: unsupported format
	_, _, err = parseChatVideoDataURL(tinyPNGDataURL())
	if err == nil {
		t.Fatal("expected error for image data URL")
	}
}

func TestSystemServiceChatSendsPastedVideoAsContentPart(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Video reviewed."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	videoDataURL := tinyMP4DataURL()
	if _, err := service.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{
		Content: "Review this video.",
		Videos:  []ChatVideoInput{{Name: "clip.mp4", DataURL: videoDataURL}},
	}); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if len(session.Messages) < 1 || len(session.Messages[0].Videos) != 1 {
		t.Fatalf("expected user message video metadata, got %#v", session.Messages)
	}
	if session.Messages[0].Videos[0].Name != "clip.mp4" || !strings.HasPrefix(session.Messages[0].Videos[0].DataURL, "data:video/mp4;base64,") {
		t.Fatalf("unexpected user video metadata: %#v", session.Messages[0].Videos[0])
	}
	if len(captured.Messages) < 2 {
		t.Fatalf("expected system and user messages, got %#v", captured.Messages)
	}
	user := captured.Messages[1]
	// text + video_url = 2 parts (video sent as content part with data URL)
	if len(user.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts (text, video_url), got %d: %#v", len(user.ContentParts), user.ContentParts)
	}
	if user.ContentParts[0].Type != "text" || !strings.Contains(user.ContentParts[0].Text, "Attached media:") {
		t.Fatalf("expected attached media labels in text part, got %#v", user.ContentParts[0])
	}
	if user.ContentParts[1].Type != "video_url" {
		t.Fatalf("expected second part to be video_url, got %q", user.ContentParts[1].Type)
	}
	if user.ContentParts[1].VideoURL == nil || !strings.HasPrefix(user.ContentParts[1].VideoURL.URL, "data:video/mp4;base64,") {
		t.Fatalf("expected video data URL in video_url part, got %#v", user.ContentParts[1])
	}
}

func TestSystemServiceChatSendsMixedImageAndVideoAttachments(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Both reviewed."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))

	if _, err := service.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{
		Content: "Review both.",
		Images:  []ChatImageInput{{Name: "screen.png", DataURL: tinyPNGDataURL()}},
		Videos:  []ChatVideoInput{{Name: "clip.mp4", DataURL: tinyMP4DataURL()}},
	}); err != nil {
		t.Fatalf("send chat: %v", err)
	}
	session := waitForChatIdle(t, service, workspaceID)

	if len(session.Messages[0].Images) != 1 || len(session.Messages[0].Videos) != 1 {
		t.Fatalf("expected one image and one video, got images=%d videos=%d", len(session.Messages[0].Images), len(session.Messages[0].Videos))
	}
	user := captured.Messages[1]
	// text + image_url + video_url = 3 parts
	if len(user.ContentParts) != 3 {
		t.Fatalf("expected 3 content parts (text, image, video), got %d: %#v", len(user.ContentParts), user.ContentParts)
	}
	if user.ContentParts[1].Type != "image_url" {
		t.Fatalf("expected second part to be image_url, got %q", user.ContentParts[1].Type)
	}
	if user.ContentParts[2].Type != "video_url" {
		t.Fatalf("expected third part to be video_url, got %q", user.ContentParts[2].Type)
	}
}

func TestSystemServiceChatRejectsUnsupportedVideo(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("chat request should not be sent for an unsupported video")
	}))

	_, err := service.SendChatMessageWithAttachments(workspaceID, ChatMessageRequest{
		Content: "Review this.",
		Videos:  []ChatVideoInput{{Name: "clip.avi", DataURL: "data:video/avi;base64,dGVzdA=="}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported video format") {
		t.Fatalf("expected unsupported video error, got %v", err)
	}
}

func TestSystemServiceChatRejectsOversizedWorkspaceVideo(t *testing.T) {
	root := t.TempDir()
	data := append(tinyMP4Bytes(), make([]byte, maxChatVideoBytes+1)...)
	if err := os.WriteFile(filepath.Join(root, "huge.mp4"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("chat request should not be sent for an oversized video")
	}))
	videoPath := labeledTestPath(t, service, workspaceID, "huge.mp4")

	_, err := service.SendChatMessage(workspaceID, "Review @"+videoPath)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("expected oversized video error, got %v", err)
	}
}

func TestChatMediaTextContent(t *testing.T) {
	// Only images
	text := chatMediaTextContent("hello", []ChatImageAttachment{{Name: "pic.png", MediaType: "image/png", Bytes: 100}}, nil)
	if !strings.Contains(text, "Attached media:") || !strings.Contains(text, "pic.png") {
		t.Fatalf("expected image in media text, got %q", text)
	}

	// Only videos
	text = chatMediaTextContent("", nil, []ChatVideoAttachment{{Name: "clip.mp4", MediaType: "video/mp4", Bytes: 200}})
	if !strings.Contains(text, "Please review the attached video(s)") {
		t.Fatalf("expected video default prompt, got %q", text)
	}

	// Both
	text = chatMediaTextContent("check these",
		[]ChatImageAttachment{{Name: "pic.png", MediaType: "image/png", Bytes: 100}},
		[]ChatVideoAttachment{{Name: "clip.mp4", MediaType: "video/mp4", Bytes: 200}})
	if !strings.Contains(text, "pic.png") || !strings.Contains(text, "clip.mp4") {
		t.Fatalf("expected both media in text, got %q", text)
	}

	// No media returns content as-is
	text = chatMediaTextContent("hello", nil, nil)
	if text != "hello" {
		t.Fatalf("expected unchanged content, got %q", text)
	}
}

func TestChatMediaContentParts(t *testing.T) {
	images := []ChatImageAttachment{{Name: "pic.png", MediaType: "image/png", Bytes: 100, DataURL: tinyPNGDataURL()}}
	videos := []ChatVideoAttachment{{Name: "clip.mp4", MediaType: "video/mp4", Bytes: 200, DataURL: tinyMP4DataURL()}}

	// Only images
	parts := chatMediaContentParts("hi", images, nil)
	if len(parts) != 2 || parts[1].Type != "image_url" {
		t.Fatalf("expected image part, got %#v", parts)
	}

	// Only videos — video_url part is sent as content part
	parts = chatMediaContentParts("hi", nil, videos)
	if len(parts) != 2 || parts[1].Type != "video_url" {
		t.Fatalf("expected text + video_url parts, got %#v", parts)
	}
	if parts[1].VideoURL == nil || !strings.HasPrefix(parts[1].VideoURL.URL, "data:video/mp4;base64,") {
		t.Fatalf("expected video data URL, got %#v", parts[1])
	}

	// Both — image_url and video_url as content parts
	parts = chatMediaContentParts("hi", images, videos)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts (text, image, video), got %d", len(parts))
	}
	if parts[1].Type != "image_url" {
		t.Fatalf("expected second part to be image_url, got %q", parts[1].Type)
	}
	if parts[2].Type != "video_url" {
		t.Fatalf("expected third part to be video_url, got %q", parts[2].Type)
	}

	// None
	parts = chatMediaContentParts("hi", nil, nil)
	if parts != nil {
		t.Fatalf("expected nil, got %#v", parts)
	}
}

func TestLLMVideoURLContentPart(t *testing.T) {
	part := llm.VideoURLContentPart("data:video/mp4;base64,abc123")
	if part.Type != "video_url" {
		t.Fatalf("expected type video_url, got %q", part.Type)
	}
	if part.VideoURL == nil || part.VideoURL.URL != "data:video/mp4;base64,abc123" {
		t.Fatalf("expected correct VideoURL, got %#v", part)
	}
	if part.ImageURL != nil {
		t.Fatal("expected ImageURL to be nil")
	}
}

func TestCloneMessagesPreservesVideoURL(t *testing.T) {
	// Verify VideoURL is properly structured and serializable via NewChatRequest
	// which internally calls cloneMessages.
	messages := []llm.Message{
		{
			Role: llm.RoleUser,
			ContentParts: []llm.MessageContentPart{
				llm.TextContentPart("hello"),
				llm.VideoURLContentPart("data:video/mp4;base64,test"),
			},
		},
	}
	settings := llm.Settings{Model: "test-model", Endpoint: "http://localhost"}
	req, err := llm.NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].ContentParts) != 2 {
		t.Fatalf("expected preserved structure, got %#v", req.Messages)
	}
	part := req.Messages[0].ContentParts[1]
	if part.Type != "video_url" || part.VideoURL == nil || part.VideoURL.URL != "data:video/mp4;base64,test" {
		t.Fatalf("expected preserved VideoURL, got %#v", part)
	}
}
