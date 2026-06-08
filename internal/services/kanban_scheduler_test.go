package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

func TestKanbanSchedulerRespectsDependencies(t *testing.T) {
	root := t.TempDir()
	var service *SystemService
	var workspaceID string
	dependentRequest := make(chan llm.ChatRequest, 1)
	service, workspaceID = newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		title := cardTitleFromRequest(t, request)
		if title == "Dependent" {
			board, err := service.LoadKanbanBoard(workspaceID)
			if err != nil {
				t.Fatalf("load board during dependent request: %v", err)
			}
			if len(board.Done) != 1 || board.Done[0].ID != "card-1" {
				t.Fatalf("dependent started before prerequisite was done: %#v", board)
			}
			select {
			case dependentRequest <- request:
			default:
			}
		}
		writeSSE(t, w,
			fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":"Completed %s."}}]}`, title),
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Prerequisite", Description: "First", AcceptanceCriteria: []string{"First"}, Lane: KanbanLaneReady},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Dependent", Description: "Second", AcceptanceCriteria: []string{"Second"}, Dependencies: []string{"card-1"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 2); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 2
	})
	if len(board.Blocked) != 0 || len(board.Ready) != 0 || len(board.InProgress) != 0 {
		t.Fatalf("expected all cards done, got %#v", board)
	}

	var captured llm.ChatRequest
	select {
	case captured = <-dependentRequest:
	case <-time.After(time.Second):
		t.Fatal("dependent card request was not captured")
	}
	requestData, err := json.Marshal(captured.Messages)
	if err != nil {
		t.Fatalf("marshal dependent request: %v", err)
	}
	for _, expected := range []string{
		"Completed dependency outputs:",
		"card-1 (Prerequisite):",
		"Completed Prerequisite.",
	} {
		if !strings.Contains(string(requestData), expected) {
			t.Fatalf("expected dependent request to include %q, got %s", expected, requestData)
		}
	}
}

func TestKanbanSchedulerSuccessfulCardMovesToDone(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"Checking the card."}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"Implemented and verified."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Finish task", Description: "Do it", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})
	done := board.Done[0]
	if done.ID != "card-1" || done.Status != KanbanLaneDone {
		t.Fatalf("expected card done, got %#v", done)
	}
	if !transcriptContains(done.ProgressTranscript, "Checking the card.") || !transcriptContains(done.ProgressTranscript, "Implemented and verified.") {
		t.Fatalf("expected thinking and final message in transcript, got %#v", done.ProgressTranscript)
	}
}

func TestKanbanSchedulerHandlesInlineReasoningToolCall(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	inlineToolCall := `<tool_call> <function=filesystem_list> <parameter=path> . </parameter> </function> </tool_call>`
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, "Checking files.\n"+inlineToolCall),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
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
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Inspect workspace", Description: "List files", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})
	if requestCount.Load() != 2 {
		t.Fatalf("expected tool result follow-up request, got %d requests", requestCount.Load())
	}
	transcript := board.Done[0].ProgressTranscript
	if !transcriptContains(transcript, "Checking files.") || !transcriptContains(transcript, "Found README.md.") || !transcriptContains(transcript, `"tool":"filesystem_list"`) {
		t.Fatalf("expected clean thinking, tool call, and final result in transcript, got %#v", transcript)
	}
	if transcriptContains(transcript, "tool_call>") {
		t.Fatalf("inline tool markup leaked into transcript: %#v", transcript)
	}
}

func TestKanbanSchedulerReadImageToolSendsImageContentPart(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ui.png"), tinyPNGBytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var requestCount atomic.Int32
	var secondRequest llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_image","type":"function","function":{"name":"filesystem_read_image","arguments":%q}}]}}]}`, `{"path":"ui.png"}`),
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			)
		case 2:
			if err := json.NewDecoder(r.Body).Decode(&secondRequest); err != nil {
				t.Fatalf("decode second request: %v", err)
			}
			writeSSE(t, w,
				`{"choices":[{"index":0,"delta":{"content":"Image reviewed."}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			)
		default:
			t.Fatalf("unexpected extra request")
		}
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Inspect screenshot", Description: "Look at ui.png", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})

	if requestCount.Load() != 2 {
		t.Fatalf("expected image tool follow-up request, got %d requests", requestCount.Load())
	}
	if !transcriptContains(board.Done[0].ProgressTranscript, `"contentType":"image_url"`) {
		t.Fatalf("expected image tool metadata in transcript, got %#v", board.Done[0].ProgressTranscript)
	}
	var imageMessage *llm.Message
	for i := range secondRequest.Messages {
		message := &secondRequest.Messages[i]
		if message.Role == llm.RoleUser && len(message.ContentParts) == 2 && message.ContentParts[1].ImageURL != nil {
			imageMessage = message
		}
		if message.Role == llm.RoleTool && strings.Contains(message.Content, "data:image") {
			t.Fatalf("expected tool message to omit image data URL, got %q", message.Content)
		}
	}
	if imageMessage == nil || imageMessage.ContentParts[1].Type != "image_url" || !strings.HasPrefix(imageMessage.ContentParts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("expected image content-parts message, got %#v", secondRequest.Messages)
	}
}

func TestKanbanSchedulerRetriesWhenThinkingStreamLoops(t *testing.T) {
	root := t.TempDir()
	loopPhrase := "checking the workspace now "
	retryRequest := make(chan llm.ChatRequest, 1)
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			writeSSE(t, w,
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, loopPhrase),
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, loopPhrase),
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, loopPhrase),
				fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning_content":%q}}]}`, loopPhrase),
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
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Finish task", Description: "Do it", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})
	if requestCount.Load() != 2 {
		t.Fatalf("expected one retry request, got %d", requestCount.Load())
	}
	done := board.Done[0]
	if !transcriptContains(done.ProgressTranscript, "Detected repeated thinking") || !transcriptContains(done.ProgressTranscript, "Recovered without repeating.") {
		t.Fatalf("expected retry status and recovered content in transcript, got %#v", done.ProgressTranscript)
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
	if !strings.Contains(requestText, "thinking started repeating itself") || !strings.Contains(requestText, "Continue or retry") {
		t.Fatalf("expected thinking retry guidance in request, got %s", requestText)
	}
}

func TestKanbanSchedulerBlocksCardOnTokenLimitFinishReason(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Started implementation"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
		)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Finish task", Description: "Do it", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Blocked) == 1
	})
	blocked := board.Blocked[0]
	if blocked.ID != "card-1" || blocked.Status != KanbanLaneBlocked {
		t.Fatalf("expected card blocked, got %#v", blocked)
	}
	if !transcriptContains(blocked.ProgressTranscript, "Started implementation") || !transcriptContains(blocked.ProgressTranscript, "token limit") {
		t.Fatalf("expected partial output and token-limit error in transcript, got %#v", blocked.ProgressTranscript)
	}
}

func TestKanbanAgentIncludesWorkspaceInstructions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Kanban agents must follow the workspace playbook."), 0o600); err != nil {
		t.Fatal(err)
	}
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Implemented with instructions."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Follow instructions", Description: "Use AGENTS.md", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})

	if len(captured.Messages) == 0 || captured.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected system message first, got %#v", captured.Messages)
	}
	if !strings.Contains(captured.Messages[0].Content, "Kanban agents must follow the workspace playbook.") {
		t.Fatalf("expected AGENTS.md content in system prompt, got %q", captured.Messages[0].Content)
	}
	if !strings.Contains(captured.Messages[0].Content, "Write the final message as a concise handoff summary for dependent cards") {
		t.Fatalf("expected dependency handoff summary instruction in system prompt, got %q", captured.Messages[0].Content)
	}
	assertSystemPromptOperatingContext(t, captured.Messages[0].Content, root)
}

func TestKanbanCardProgressStreamsOnlyWhileDetailIsOpen(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Inspectable", Description: "Watch it", AcceptanceCriteria: []string{"Progress"}, Lane: KanbanLaneReady},
	})

	events := newKanbanEventRecorder(service)
	service.appendKanbanProgress(workspaceID, "card-1", KanbanProgressEntry{
		Type:    "message",
		Title:   "Buffered",
		Content: "Buffered while closed.",
	})
	if got := events.countType("card_progress"); got != 0 {
		t.Fatalf("expected no progress event before detail opens, got %d", got)
	}

	board, err := service.OpenKanbanCardDetail(workspaceID, "card-1")
	if err != nil {
		t.Fatalf("open card detail: %v", err)
	}
	if len(board.Ready) != 1 || !transcriptContains(board.Ready[0].ProgressTranscript, "Buffered while closed.") {
		t.Fatalf("expected opened detail to include buffered progress, got %#v", board)
	}

	service.appendKanbanProgress(workspaceID, "card-1", KanbanProgressEntry{
		Type:    "message",
		Title:   "Live",
		Content: "Visible while open.",
	})
	if !events.waitFor(func(event KanbanEvent) bool {
		return event.Type == "card_progress" && event.CardID == "card-1" && event.Entry != nil && event.Entry.Content == "Visible while open."
	}) {
		t.Fatalf("expected live progress event while card detail is open, got %#v", events.snapshot())
	}

	if _, err := service.CloseKanbanCardDetail(workspaceID, "card-1"); err != nil {
		t.Fatalf("close card detail: %v", err)
	}
	service.appendKanbanProgress(workspaceID, "card-1", KanbanProgressEntry{
		Type:    "message",
		Title:   "Buffered",
		Content: "Buffered after close.",
	})
	if got := events.countType("card_progress"); got != 1 {
		t.Fatalf("expected progress stream to stop after close, got %d events: %#v", got, events.snapshot())
	}
}

func TestClosingCardDetailStopsProgressEventsButAgentContinues(t *testing.T) {
	root := t.TempDir()
	requestSeen := make(chan struct{})
	writeFirst := make(chan struct{})
	writeFinal := make(chan struct{})
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		close(requestSeen)
		w.Header().Set("Content-Type", "text/event-stream")
		<-writeFirst
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"started\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-writeFinal
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":" finished"}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Keep running", Description: "Continue after close", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})
	events := newKanbanEventRecorder(service)

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("agent request did not start")
	}
	if _, err := service.OpenKanbanCardDetail(workspaceID, "card-1"); err != nil {
		t.Fatalf("open card detail: %v", err)
	}
	close(writeFirst)
	if !events.waitFor(func(event KanbanEvent) bool {
		return event.Type == "card_progress" && event.CardID == "card-1" && event.Entry != nil && event.Entry.Content == "started"
	}) {
		t.Fatalf("expected live progress before closing detail, got %#v", events.snapshot())
	}

	if _, err := service.CloseKanbanCardDetail(workspaceID, "card-1"); err != nil {
		t.Fatalf("close card detail: %v", err)
	}
	close(writeFinal)
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})
	if !transcriptContains(board.Done[0].ProgressTranscript, " finished") {
		t.Fatalf("expected closed card to keep buffering progress, got %#v", board.Done[0].ProgressTranscript)
	}
	if events.any(func(event KanbanEvent) bool {
		return event.Type == "card_progress" && event.Entry != nil && strings.Contains(event.Entry.Content, "finished")
	}) {
		t.Fatalf("expected no final progress event after closing detail, got %#v", events.snapshot())
	}

	reopened, err := service.OpenKanbanCardDetail(workspaceID, "card-1")
	if err != nil {
		t.Fatalf("reopen card detail: %v", err)
	}
	if len(reopened.Done) != 1 || !transcriptContains(reopened.Done[0].ProgressTranscript, " finished") {
		t.Fatalf("expected reopened detail to show prior progress, got %#v", reopened)
	}
}

func TestKanbanSchedulerAgentErrorMovesCardToBlocked(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		http.Error(w, "model failed", http.StatusInternalServerError)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Fail task", Description: "Fail", AcceptanceCriteria: []string{"Blocked"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Blocked) == 1
	})
	blocked := board.Blocked[0]
	if !transcriptContains(blocked.ProgressTranscript, "model failed") {
		t.Fatalf("expected visible block reason, got %#v", blocked.ProgressTranscript)
	}
}

func TestKanbanSchedulerRunsIndependentCardsAtConfiguredConcurrency(t *testing.T) {
	root := t.TempDir()
	var active atomic.Int32
	var maxActive atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(120 * time.Millisecond)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Done."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
		active.Add(-1)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "One", Description: "One", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Two", Description: "Two", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
		{ID: "card-3", WorkspaceID: workspaceID, Title: "Three", Description: "Three", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 2); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 3
	})
	if got := maxActive.Load(); got != 2 {
		t.Fatalf("expected max concurrency of 2, got %d", got)
	}
}

func TestKanbanSchedulerShutdownCancelsActiveAgents(t *testing.T) {
	root := t.TempDir()
	requestSeen := make(chan struct{})
	requestCanceled := make(chan struct{})
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		close(requestSeen)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"started\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(requestCanceled)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Long task", Description: "Wait", AcceptanceCriteria: []string{"Canceled"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("agent request did not start")
	}

	service.Shutdown()

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe agent cancellation")
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Blocked) == 1
	})
	if !transcriptContains(board.Blocked[0].ProgressTranscript, agentCancellationText) {
		t.Fatalf("expected cancellation reason in transcript, got %#v", board.Blocked[0].ProgressTranscript)
	}
}

func requestedCardTitle(t *testing.T, r *http.Request) string {
	t.Helper()
	var request llm.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return cardTitleFromRequest(t, request)
}

func cardTitleFromRequest(t *testing.T, request llm.ChatRequest) string {
	t.Helper()
	for _, message := range request.Messages {
		if message.Role != llm.RoleUser {
			continue
		}
		for _, line := range strings.Split(message.Content, "\n") {
			if title, ok := strings.CutPrefix(line, "Title: "); ok {
				return strings.TrimSpace(title)
			}
		}
	}
	t.Fatalf("card title not found in request: %#v", request.Messages)
	return ""
}

func waitForKanbanBoard(t *testing.T, service *SystemService, workspaceID string, done func(KanbanBoard) bool) KanbanBoard {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		board, err := service.LoadKanbanBoard(workspaceID)
		if err != nil {
			t.Fatalf("load board: %v", err)
		}
		if done(board) {
			return board
		}
		time.Sleep(10 * time.Millisecond)
	}
	board, _ := service.LoadKanbanBoard(workspaceID)
	t.Fatalf("timed out waiting for kanban board, got %#v", board)
	return KanbanBoard{}
}

func transcriptContains(transcript []KanbanProgressEntry, content string) bool {
	for _, entry := range transcript {
		if strings.Contains(entry.Content, content) {
			return true
		}
	}
	return false
}

type kanbanEventRecorder struct {
	mu     sync.Mutex
	events []KanbanEvent
}

func newKanbanEventRecorder(service *SystemService) *kanbanEventRecorder {
	recorder := &kanbanEventRecorder{}
	service.kanbanEventSink = recorder.record
	return recorder
}

func (r *kanbanEventRecorder) record(event KanbanEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *kanbanEventRecorder) snapshot() []KanbanEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]KanbanEvent(nil), r.events...)
}

func (r *kanbanEventRecorder) countType(eventType string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, event := range r.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func (r *kanbanEventRecorder) any(match func(KanbanEvent) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if match(event) {
			return true
		}
	}
	return false
}

func (r *kanbanEventRecorder) waitFor(match func(KanbanEvent) bool) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if r.any(match) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestKanbanBlockedCardCanReceiveUserMessageAndReturnToReady(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Blocked", Description: "Needs input", AcceptanceCriteria: []string{"Ready"}, Lane: KanbanLaneBlocked},
	})

	board, err := service.AddKanbanCardMessage(workspaceID, "card-1", "Try a smaller implementation.")
	if err != nil {
		t.Fatalf("add card message: %v", err)
	}
	if len(board.Ready) != 1 || board.Ready[0].ID != "card-1" {
		t.Fatalf("expected card to return to ready, got %#v", board)
	}
	if !transcriptContains(board.Ready[0].ProgressTranscript, "Try a smaller implementation.") {
		t.Fatalf("expected user message in transcript, got %#v", board.Ready[0].ProgressTranscript)
	}
}

func TestKanbanBlockedCardUserMessageIsIncludedInNextRun(t *testing.T) {
	root := t.TempDir()
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Retried with guidance."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{
			ID:                 "card-1",
			WorkspaceID:        workspaceID,
			Title:              "Retry me",
			Description:        "Needs user guidance",
			AcceptanceCriteria: []string{"Done"},
			Lane:               KanbanLaneBlocked,
			ProgressTranscript: []KanbanProgressEntry{{
				Type:    "error",
				Title:   "Agent error",
				Content: "Previous attempt failed.",
				Status:  KanbanLaneBlocked,
			}},
		},
	})

	if _, err := service.AddKanbanCardMessage(workspaceID, "card-1", "Use a smaller implementation."); err != nil {
		t.Fatalf("add user message: %v", err)
	}
	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})

	requestData, err := json.Marshal(captured.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requestData), "Use a smaller implementation.") {
		t.Fatalf("expected retry prompt to include user message, got %s", requestData)
	}
}

func TestKanbanStopCardBlocksActiveAgent(t *testing.T) {
	root := t.TempDir()
	requestSeen := make(chan struct{})
	requestCanceled := make(chan struct{})
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		close(requestSeen)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"started\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(requestCanceled)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Stop me", Description: "Wait", AcceptanceCriteria: []string{"Stopped"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("agent request did not start")
	}
	if _, err := service.StopKanbanCard(workspaceID, "card-1"); err != nil {
		t.Fatalf("stop card: %v", err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe card cancellation")
	}
	board := waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Blocked) == 1
	})
	if !transcriptContains(board.Blocked[0].ProgressTranscript, "User stopped") {
		t.Fatalf("expected stop reason in transcript, got %#v", board.Blocked[0].ProgressTranscript)
	}
}

func TestKanbanSchedulerStateIsRuntimeOnlyAcrossRestart(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service, workspaceID := newDecompositionTestServiceWithStore(t, root, storePath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		writeSSE(t, w,
			`{"choices":[{"index":0,"delta":{"content":"Runtime result."}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Runtime result", Description: "Do not save", AcceptanceCriteria: []string{"Runtime"}, Lane: KanbanLaneReady},
	})

	if _, err := service.StartKanbanExecution(workspaceID, 1); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	waitForKanbanBoard(t, service, workspaceID, func(board KanbanBoard) bool {
		return len(board.Done) == 1
	})

	reloaded := NewSystemServiceWithStorePath(storePath)
	board, err := reloaded.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load reloaded board: %v", err)
	}
	if len(board.Ready) != 0 || len(board.InProgress) != 0 || len(board.Blocked) != 0 || len(board.Done) != 0 {
		t.Fatalf("expected scheduler card state to be runtime-only after reload, got %#v", board)
	}
}
