package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

func chatPlanQuestionSSE(args string) (string, string) {
	return fmt.Sprintf(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_q1","type":"function","function":{"name":"ask_user_questions","arguments":%q}}]}}]}`, args),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
}

func disableResearchAgents(t *testing.T, service *SystemService) {
	t.Helper()
	settings := service.LoadState().Settings
	settings.ResearchAgentConcurrency = 0
	if _, err := service.SaveSettings(settings); err != nil {
		t.Fatalf("disable research agents: %v", err)
	}
}

func waitForPlanQuestionWait(t *testing.T, service *SystemService) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		service.chatMu.Lock()
		var id string
		for key := range service.planQuestionWaits {
			id = key
			break
		}
		service.chatMu.Unlock()
		if id != "" {
			return id
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("plan question wait was never registered")
	return ""
}

func hasRoleToolMessage(t *testing.T, request llm.ChatRequest, contains ...string) bool {
	t.Helper()
	for _, message := range request.Messages {
		if message.Role != llm.RoleTool {
			continue
		}
		all := true
		for _, expected := range contains {
			if !strings.Contains(message.Content, expected) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func TestValidatePlanAnswers(t *testing.T) {
	set := &PlanQuestionSet{
		QuestionSetID: "call_1",
		Questions: []PlanQuestion{
			{ID: "scope", Question: "Scope?", Options: []string{"Core", "Extended"}},
			{ID: "lang", Question: "Language?"},
		},
	}
	if err := validatePlanAnswers(set, nil, true); err != nil {
		t.Fatalf("expected skip to pass, got %v", err)
	}
	if err := validatePlanAnswers(set, []PlanAnswer{{QuestionID: "scope", OptionIndex: 0}, {QuestionID: "lang", Text: "Go"}}, false); err != nil {
		t.Fatalf("expected valid answers to pass, got %v", err)
	}
	if err := validatePlanAnswers(set, []PlanAnswer{{QuestionID: "scope", OptionIndex: 0}}, false); err == nil {
		t.Fatalf("expected missing answer to fail")
	}
	if err := validatePlanAnswers(set, []PlanAnswer{{QuestionID: "nope", OptionIndex: 0}, {QuestionID: "lang", Text: "Go"}}, false); err == nil {
		t.Fatalf("expected unknown question id to fail")
	}
	if err := validatePlanAnswers(set, []PlanAnswer{{QuestionID: "scope", OptionIndex: 5}, {QuestionID: "lang", Text: "Go"}}, false); err == nil {
		t.Fatalf("expected out-of-range option index to fail")
	}
	if err := validatePlanAnswers(set, []PlanAnswer{{QuestionID: "scope", OptionIndex: 0}, {QuestionID: "scope", OptionIndex: 1}}, false); err == nil {
		t.Fatalf("expected duplicate answers to fail")
	}
}

func TestPlanQuestionsSubmitFinalizes(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			args := `{"questions":[{"id":"scope","question":"Implement in Go or Python?","options":["Go","Python"]}]}`
			call, done := chatPlanQuestionSSE(args)
			writeSSE(t, w, call, done)
		case 2:
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request 2: %v", err)
			}
			content, done := chatVerificationContentSSE("Final plan: implement the feature in Go.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected chat request %d", requestCount.Load())
		}
	}))
	disableResearchAgents(t, service)

	session, err := service.SendChatMessageWithPlanMode(workspaceID, "Plan out this feature", true)
	if err != nil {
		t.Fatalf("send plan message: %v", err)
	}
	qsetID := waitForPlanQuestionWait(t, service)

	updated, err := service.SubmitPlanAnswers(workspaceID, session.ChatID, qsetID, []PlanAnswer{{QuestionID: "scope", OptionIndex: 0}})
	if err != nil {
		t.Fatalf("submit answers: %v", err)
	}
	if updated.ChatID != session.ChatID {
		t.Fatalf("expected same chat id, got %q", updated.ChatID)
	}

	final := waitForChatIdle(t, service, workspaceID)
	last := final.Messages[len(final.Messages)-1]
	if last.Status != "complete" {
		t.Fatalf("expected complete status, got %q", last.Status)
	}
	if !strings.Contains(last.Content, "implement the feature in Go") {
		t.Fatalf("unexpected final plan %q", last.Content)
	}
	if !hasRoleToolMessage(t, captured, `"scope"`, `"optionIndex":0`) {
		t.Fatalf("expected tool result with answers in request 2: %#v", captured.Messages)
	}
	if !toolRequestSchemas(t, captured) {
		t.Fatalf("expected ask_user_questions to remain exposed in later plan requests")
	}

	var planActivity *ChatToolActivity
	for i := range last.ToolCalls {
		if last.ToolCalls[i].Name == tools.AskUserQuestionsToolName {
			planActivity = &last.ToolCalls[i]
			break
		}
	}
	if planActivity == nil {
		t.Fatalf("expected an ask_user_questions activity on the final message")
	}
	if planActivity.Status != "complete" {
		t.Fatalf("expected activity status complete, got %q", planActivity.Status)
	}
	if planActivity.PlanQuestions == nil || len(planActivity.PlanQuestions.Questions) != 1 {
		t.Fatalf("expected planQuestions preserved on the completed activity")
	}
	if planActivity.Result == "" || !strings.Contains(planActivity.Result, `"optionIndex":0`) {
		t.Fatalf("expected answered tool result, got %q", planActivity.Result)
	}
}

func TestPlanQuestionsSkipFinalizes(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	var captured llm.ChatRequest
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			args := `{"questions":[{"id":"aim","question":"Primary goal?","options":["Speed","Portability"]}]}`
			call, done := chatPlanQuestionSSE(args)
			writeSSE(t, w, call, done)
		case 2:
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request 2: %v", err)
			}
			content, done := chatVerificationContentSSE("Final plan with best judgment.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected chat request %d", requestCount.Load())
		}
	}))
	disableResearchAgents(t, service)

	session, err := service.SendChatMessageWithPlanMode(workspaceID, "Plan it with your best judgment", true)
	if err != nil {
		t.Fatalf("send plan message: %v", err)
	}
	qsetID := waitForPlanQuestionWait(t, service)

	if _, err := service.SkipPlanQuestions(workspaceID, session.ChatID, qsetID); err != nil {
		t.Fatalf("skip questions: %v", err)
	}

	final := waitForChatIdle(t, service, workspaceID)
	last := final.Messages[len(final.Messages)-1]
	if last.Status != "complete" {
		t.Fatalf("expected complete status, got %q", last.Status)
	}
	if !strings.Contains(last.Content, "best judgment") {
		t.Fatalf("unexpected final plan %q", last.Content)
	}
	if !hasRoleToolMessage(t, captured, `"skipped":true`) {
		t.Fatalf("expected skipped tool result in request 2: %#v", captured.Messages)
	}
}

func TestPlanQuestionsDuplicateSubmitRejected(t *testing.T) {
	root := t.TempDir()
	var requestCount atomic.Int32
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		switch requestCount.Add(1) {
		case 1:
			args := `{"questions":[{"id":"q","question":"Q?","options":["A","B"]}]}`
			call, done := chatPlanQuestionSSE(args)
			writeSSE(t, w, call, done)
		case 2:
			content, done := chatVerificationContentSSE("Final plan.")
			writeSSE(t, w, content, done)
		default:
			t.Fatalf("unexpected chat request %d", requestCount.Load())
		}
	}))
	disableResearchAgents(t, service)

	session, err := service.SendChatMessageWithPlanMode(workspaceID, "Plan something", true)
	if err != nil {
		t.Fatalf("send plan message: %v", err)
	}
	qsetID := waitForPlanQuestionWait(t, service)

	if _, err := service.SubmitPlanAnswers(workspaceID, session.ChatID, qsetID, []PlanAnswer{{QuestionID: "q", OptionIndex: 0}}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	waitForChatIdle(t, service, workspaceID)
	if _, err := service.SubmitPlanAnswers(workspaceID, session.ChatID, qsetID, []PlanAnswer{{QuestionID: "q", OptionIndex: 1}}); err == nil {
		t.Fatalf("expected duplicate submit to be rejected")
	}
}

func TestPlanQuestionsStopCancelsWait(t *testing.T) {
	root := t.TempDir()
	service, workspaceID := newChatTestService(t, root, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertChatStreamRequest(t, r)
		args := `{"questions":[{"id":"q","question":"Q?"}]}`
		call, done := chatPlanQuestionSSE(args)
		writeSSE(t, w, call, done)
	}))
	disableResearchAgents(t, service)

	session, err := service.SendChatMessageWithPlanMode(workspaceID, "Plan something", true)
	if err != nil {
		t.Fatalf("send plan message: %v", err)
	}
	qsetID := waitForPlanQuestionWait(t, service)

	if _, err := service.StopChatStreamForTab(workspaceID, session.ChatID); err != nil {
		t.Fatalf("stop chat: %v", err)
	}
	final := waitForChatIdle(t, service, workspaceID)
	last := final.Messages[len(final.Messages)-1]
	if last.Status != "canceled" {
		t.Fatalf("expected canceled status, got %q", last.Status)
	}
	service.chatMu.Lock()
	_, stillWaiting := service.planQuestionWaits[qsetID]
	service.chatMu.Unlock()
	if stillWaiting {
		t.Fatalf("expected plan question wait to be cleaned up after cancel")
	}
}

func TestRestoreDowngradesStaleAwaitingInput(t *testing.T) {
	service := NewSystemServiceWithStorePath(filepath.Join(t.TempDir(), "state.json"))
	persisted := persistedChatSession{
		ChatID: "chat-1",
		Messages: []ChatMessage{{
			ID:     "msg-1",
			Role:   "assistant",
			Status: "streaming",
			ToolCalls: []ChatToolActivity{{
				ID:     "call-1",
				Name:   tools.AskUserQuestionsToolName,
				Status: "awaiting_input",
			}},
		}},
	}
	service.chatMu.Lock()
	session, changed := service.restorePersistedChatSessionLocked("ws-1", persisted)
	service.chatMu.Unlock()
	if !changed {
		t.Fatalf("expected restore to mark changes")
	}
	if len(session.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(session.Messages))
	}
	message := session.Messages[0]
	if message.Status != "canceled" {
		t.Fatalf("expected streaming message downgraded to canceled, got %q", message.Status)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(message.ToolCalls))
	}
	activity := message.ToolCalls[0]
	if activity.Status != "complete" {
		t.Fatalf("expected awaiting_input downgraded to complete, got %q", activity.Status)
	}
	if activity.Result == "" {
		t.Fatalf("expected non-empty downgrade result note")
	}
}

// toolRequestSchemas is a helper used to keep the captured schema assertion
// explicit; returns whether the request exposed ask_user_questions.
func toolRequestSchemas(t *testing.T, request llm.ChatRequest) bool {
	t.Helper()
	for _, tool := range request.Tools {
		if tool.Function.Name == tools.AskUserQuestionsToolName {
			return true
		}
	}
	return false
}
