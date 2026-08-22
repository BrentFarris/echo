package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

// TestPlanQuestionsPauseBroadcastsGlobally verifies that when a plan-mode
// question set begins awaiting input, a global plan_questions_awaiting message
// is broadcast to every connected client (regardless of the surface/tab they
// are viewing), so a client elsewhere can notify the user.
func TestPlanQuestionsPauseBroadcastsGlobally(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "plan-questions-broadcast")
	server.llm = &planQuestionsStreamer{}

	url := startWebSocketTestServer(t, server)
	chatClient := dialSharedClient(t, url)
	// Deliberately leave the observer unsubscribed: the broadcast must reach
	// unsubscribed clients, like the chat_completed notification it mirrors.
	observer := dialSharedClient(t, url)

	subscribeChat(t, chatClient, workspace.ID)
	if err := chatClient.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "plan-broadcast",
		"message": "Plan this feature", "agentModeId": "plan",
	}); err != nil {
		t.Fatal(err)
	}

	awaiting := readUntilMessageType(t, observer, "plan_questions_awaiting")
	if awaiting["workspaceId"] != workspace.ID || awaiting["workspaceName"] != workspace.Name {
		t.Fatalf("unexpected workspace on global broadcast: %#v", awaiting)
	}
	if awaiting["surface"] != "chat" || awaiting["chatId"] == "" || awaiting["turnId"] == "" || awaiting["callId"] == "" {
		t.Fatalf("expected target metadata on global broadcast: %#v", awaiting)
	}
	questions, ok := awaiting["questions"].([]any)
	if !ok || len(questions) != 2 {
		t.Fatalf("expected questions on global broadcast: %#v", awaiting)
	}
	first, ok := questions[0].(map[string]any)
	if !ok || first["question"] == "" {
		t.Fatalf("expected question text on global broadcast: %#v", awaiting)
	}
}

type planQuestionsStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (f *planQuestionsStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	callCount := len(f.requests)
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 4)
	if callCount == 1 {
		events <- llm.StreamEvent{
			Type: llm.EventToolCall,
			ToolCall: &llm.ToolCallDelta{
				Index: 0, ID: "call-questions", Type: "function",
				Function: llm.FunctionCallDelta{
					Name:      tools.AskUserQuestionsToolName,
					Arguments: `{"questions":[{"id":"scope","question":"Which scope?","options":["Core","Extended"]},{"id":"language","question":"Which language?"}]}`,
				},
			},
		}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Final plan using the answers."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "plan-questions", Events: events}
}

func (f *planQuestionsStreamer) capturedRequests() []llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.ChatRequest(nil), f.requests...)
}

func TestPlanQuestionsPauseSubmitAndResumeChat(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "plan-questions")
	streamer := &planQuestionsStreamer{}
	server.llm = streamer

	url := startWebSocketTestServer(t, server)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "requestId": "plan-request",
		"message": "Plan this feature", "agentModeId": "plan",
	}); err != nil {
		t.Fatal(err)
	}

	toolCall := readUntilSessionEvent(t, conn, "tool_call")
	if toolCall["tool"] != tools.AskUserQuestionsToolName || toolCall["status"] != "awaiting_input" {
		t.Fatalf("unexpected question event: %#v", toolCall)
	}
	questionSet, ok := toolCall["planQuestions"].(map[string]any)
	if !ok || questionSet["questionSetId"] != "call-questions" {
		t.Fatalf("question set missing from event: %#v", toolCall)
	}

	parent, err := server.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	chatID := parent.activeChatID
	parent.mu.Unlock()
	if err := conn.WriteJSON(map[string]any{
		"type": "plan_questions_submit", "workspaceId": workspace.ID, "chatId": chatID,
		"questionSetId": "call-questions", "requestId": "answer-request",
		"answers": []sessions.PlanAnswer{
			{QuestionID: "scope", OptionIndex: 1},
			{QuestionID: "language", OptionIndex: -1, Text: "Go"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	resolved := readUntilSessionEvent(t, conn, "plan_questions_resolved")
	if resolved["skipped"] != false {
		t.Fatalf("unexpected resolved event: %#v", resolved)
	}
	result := readUntilSessionEvent(t, conn, "tool_result")
	if result["success"] != true {
		t.Fatalf("question tool failed: %#v", result)
	}
	finished := readUntilSessionEvent(t, conn, "turn_finished")
	if finished["status"] != "done" {
		t.Fatalf("chat did not resume: %#v", finished)
	}

	requests := streamer.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("expected two model requests, got %d", len(requests))
	}
	foundSchema := false
	for _, schema := range requests[0].Tools {
		foundSchema = foundSchema || schema.Function.Name == tools.AskUserQuestionsToolName
	}
	if !foundSchema {
		t.Fatal("Plan-mode request did not expose ask_user_questions")
	}
	foundAnswer := false
	for _, message := range requests[1].Messages {
		if message.Role == llm.RoleTool && strings.Contains(message.Content, `"scope"`) && strings.Contains(message.Content, `"Go"`) {
			foundAnswer = true
		}
	}
	if !foundAnswer {
		t.Fatalf("answers were not returned to the model: %#v", requests[1].Messages)
	}

	transcript := loadActiveTabTranscript(t, workspace)
	if len(transcript.Turns) != 1 || len(transcript.Turns[0].AssistantTurns) == 0 {
		t.Fatalf("question activity was not persisted: %#v", transcript.Turns)
	}
	activity := transcript.Turns[0].AssistantTurns[0].Tools[0]
	if activity.Name != tools.AskUserQuestionsToolName || activity.Status != "complete" || !activity.Success || activity.PlanQuestions == nil || len(activity.Answers) != 2 {
		t.Fatalf("unexpected persisted question activity: %#v", activity)
	}
}

func TestValidatePlanAnswers(t *testing.T) {
	set := &sessions.PlanQuestionSet{
		QuestionSetID: "call", Questions: []sessions.PlanQuestion{
			{ID: "scope", Question: "Scope?", Options: []string{"Core", "Extended"}},
			{ID: "language", Question: "Language?"},
		},
	}
	if _, err := validatePlanAnswers(set, nil, true); err != nil {
		t.Fatalf("skip should be valid: %v", err)
	}
	if _, err := validatePlanAnswers(set, []sessions.PlanAnswer{{QuestionID: "scope", OptionIndex: 0}, {QuestionID: "language", OptionIndex: -1, Text: "Go"}}, false); err != nil {
		t.Fatalf("valid answers failed: %v", err)
	}
	invalid := [][]sessions.PlanAnswer{
		{{QuestionID: "scope", OptionIndex: 0}},
		{{QuestionID: "missing", OptionIndex: 0}, {QuestionID: "language", Text: "Go"}},
		{{QuestionID: "scope", OptionIndex: 5}, {QuestionID: "language", Text: "Go"}},
		{{QuestionID: "scope", OptionIndex: 0}, {QuestionID: "scope", OptionIndex: 1}},
	}
	for i, answers := range invalid {
		if _, err := validatePlanAnswers(set, answers, false); err == nil {
			t.Fatalf("case %d: expected validation failure", i)
		}
	}
}
