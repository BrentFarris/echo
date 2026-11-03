package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func assertQuestionState(t *testing.T, s *Server, want ...domain.PendingQuestion) {
	t.Helper()
	mux := http.NewServeMux()
	s.routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/state", nil))
	var state struct {
		Questions []domain.PendingQuestion `json:"questions"`
	}
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Questions == nil || !slices.Equal(state.Questions, want) {
		t.Fatalf("pending questions: got %+v, want %+v", state.Questions, want)
	}
}

func TestParseAskUserQuestionsArgs(t *testing.T) {
	if _, e := ParseAskUserQuestionsArgs(nil); e == nil {
		t.Fatal("empty arguments accepted")
	}
	if _, e := ParseAskUserQuestionsArgs(json.RawMessage(`{}`)); e == nil {
		t.Fatal("missing questions accepted")
	}
	if _, e := ParseAskUserQuestionsArgs(json.RawMessage(`{"questions":[{"id":"a","question":"A"},{"id":"a","question":"B"}]}`)); e == nil {
		t.Fatal("duplicate ids accepted")
	}
	if _, e := ParseAskUserQuestionsArgs(json.RawMessage(`{"questions":[{"id":"a","question":"A","options":["1","2","3","4"]}]}`)); e == nil {
		t.Fatal("too many options accepted")
	}
	if _, e := ParseAskUserQuestionsArgs(json.RawMessage(`{"questions":[{"id":"a","question":"A","options":["ok"]}]}`)); e != nil {
		t.Fatal(e)
	}
}

func TestValidatePlanAnswers(t *testing.T) {
	set := []PlanQuestion{{ID: "scope", Question: "Which scope?", Options: []string{"Core", "Extended"}}}
	if _, e := ValidatePlanAnswers(set, nil, true); e != nil {
		t.Fatal(e)
	}
	if _, e := ValidatePlanAnswers(set, nil, false); e == nil {
		t.Fatal("empty answers accepted without skip")
	}
	if _, e := ValidatePlanAnswers(set, []PlanAnswer{{QuestionID: "missing", OptionIndex: 0}}, false); e == nil {
		t.Fatal("unknown question accepted")
	}
	if _, e := ValidatePlanAnswers(set, []PlanAnswer{{QuestionID: "scope", OptionIndex: 5}}, false); e == nil {
		t.Fatal("out-of-range option accepted")
	}
	normalized, e := ValidatePlanAnswers(set, []PlanAnswer{{QuestionID: "scope", OptionIndex: -1, Text: "  Go  "}}, false)
	if e != nil || len(normalized) != 1 || normalized[0].Text != "Go" || normalized[0].OptionIndex != -1 {
		t.Fatal(normalized, e)
	}
	normalized, e = ValidatePlanAnswers(set, []PlanAnswer{{QuestionID: "scope", OptionIndex: 1}}, false)
	if e != nil || normalized[0].OptionIndex != 1 {
		t.Fatal(normalized, e)
	}
}

func questionPending(t *testing.T, s *Server, chatID, callID string) domain.Message {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ms, e := s.messages(chatID)
		if e != nil {
			t.Fatal(e)
		}
		for _, m := range ms {
			if m.Role == "tool" && m.CallID == callID {
				if m.Status == "running" {
					s.questionsMu.Lock()
					_, pending := s.pendingQuestions[m.ID]
					s.questionsMu.Unlock()
					if pending {
						return m
					}
					continue
				}
				if m.Status != "" {
					t.Fatalf("question finished without answers: %s", m.Status)
				}
			}
		}
		if time.Now().After(deadline) {
			runs, _ := s.Store.ChatRuns(chatID)
			t.Fatalf("question never became pending: messages=%+v questions=%+v runs=%+v", ms, s.pendingQuestionState(), runs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAskUserQuestionsBlocksAndResumes(t *testing.T) {
	s := testServer(t)
	asked := false
	var toolResult string
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		for _, m := range input.Messages {
			if m.Role == "tool" && m.CallID == "q1" {
				toolResult = m.Content
			}
		}
		if !asked {
			asked = true
			args := `{"questions":[{"id":"scope","question":"Which scope?","options":["Core","Extended"]},{"id":"lang","question":"Which language?"}]}`
			stream(w, "", []domain.ToolCall{{ID: "q1", Type: "function", Function: domain.FunctionCall{Name: AskUserQuestionsToolName, Arguments: args}}})
			return
		}
		stream(w, "Planned with Extended and Go.", nil)
	})
	c := chat(t, s, g)
	d, e := s.send(c.ID, SendRequest{Content: "Plan it", Generation: g})
	if e != nil {
		t.Fatal(e)
	}
	tool := questionPending(t, s, c.ID, "q1")
	pending := domain.PendingQuestion{ChatID: c.ID, MessageID: tool.ID, BotID: tool.BotID}
	assertQuestionState(t, s, pending)
	if !strings.Contains(tool.Content, "Which scope?") {
		t.Fatal("pending tool message does not carry the question set", tool.Content)
	}
	// Invalid answers are rejected without waking the run.
	if e := s.resolveChatQuestions(c.ID, tool.ID, []PlanAnswer{{QuestionID: "scope", OptionIndex: 5}}, false); e == nil {
		t.Fatal("out-of-range option accepted")
	}
	if e := s.resolveChatQuestions(c.ID, tool.ID, []PlanAnswer{{QuestionID: "scope", OptionIndex: 1}}, false); e == nil {
		t.Fatal("partial answers accepted")
	}
	assertQuestionState(t, s, pending)
	if e := s.resolveChatQuestions(c.ID, tool.ID, []PlanAnswer{{QuestionID: "scope", OptionIndex: 1}, {QuestionID: "lang", OptionIndex: -1, Text: "Go"}}, false); e != nil {
		t.Fatal(e)
	}
	assertQuestionState(t, s)
	waitDispatch(t, d)
	if !strings.Contains(toolResult, "Extended") || !strings.Contains(toolResult, "Go") {
		t.Fatal("model did not receive the answers", toolResult)
	}
	if e := s.resolveChatQuestions(c.ID, tool.ID, nil, true); e == nil {
		t.Fatal("second resolution accepted")
	}
	ms, e := s.messages(c.ID)
	if e != nil {
		t.Fatal(e)
	}
	var final domain.Message
	for _, m := range ms {
		if m.Role == "assistant" && m.Content != "" {
			final = m
		}
	}
	if final.Content != "Planned with Extended and Go." {
		t.Fatal("run did not resume after the answers", ms)
	}
}

func TestAskUserQuestionsSkip(t *testing.T) {
	s := testServer(t)
	asked := false
	var toolResult string
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		for _, m := range input.Messages {
			if m.Role == "tool" && m.CallID == "q1" {
				toolResult = m.Content
			}
		}
		if !asked {
			asked = true
			args := `{"questions":[{"id":"scope","question":"Which scope?","options":["Core"]}]}`
			stream(w, "", []domain.ToolCall{{ID: "q1", Type: "function", Function: domain.FunctionCall{Name: AskUserQuestionsToolName, Arguments: args}}})
			return
		}
		stream(w, "Proceeding with best judgment.", nil)
	})
	c := chat(t, s, g)
	d, e := s.send(c.ID, SendRequest{Content: "Plan it", Generation: g})
	if e != nil {
		t.Fatal(e)
	}
	tool := questionPending(t, s, c.ID, "q1")
	if e := s.resolveChatQuestions(c.ID, tool.ID, nil, true); e != nil {
		t.Fatal(e)
	}
	assertQuestionState(t, s)
	waitDispatch(t, d)
	var parsed struct {
		Skipped   bool           `json:"skipped"`
		Answers   []PlanAnswer   `json:"answers"`
		Questions []PlanQuestion `json:"questions"`
	}
	if e := json.Unmarshal([]byte(toolResult), &parsed); e != nil {
		t.Fatal(toolResult, e)
	}
	if !parsed.Skipped || parsed.Answers != nil || len(parsed.Questions) != 1 {
		t.Fatal("skip result malformed", toolResult)
	}
}

func TestAskUserQuestionsCancel(t *testing.T) {
	s := testServer(t)
	asked := false
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		if !asked {
			asked = true
			args := `{"questions":[{"id":"scope","question":"Which scope?","options":["Core"]}]}`
			stream(w, "", []domain.ToolCall{{ID: "q1", Type: "function", Function: domain.FunctionCall{Name: AskUserQuestionsToolName, Arguments: args}}})
			return
		}
		stream(w, "After the interruption.", nil)
	})
	c := chat(t, s, g)
	d, e := s.send(c.ID, SendRequest{Content: "Plan it", Generation: g})
	if e != nil {
		t.Fatal(e)
	}
	tool := questionPending(t, s, c.ID, "q1")
	finish := s.stopChat(c.ID)
	waitDispatch(t, d)
	finish()
	assertQuestionState(t, s)
	ms, e := s.messages(c.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range ms {
		if m.ID == tool.ID {
			if m.Status != "cancelled" {
				t.Fatal("cancelled question kept running status", m.Status)
			}
			if !strings.Contains(m.Content, "canceled") {
				t.Fatal("cancel reason missing from the tool result", m.Content)
			}
		}
	}
}

func TestPendingQuestionEvents(t *testing.T) {
	for _, resolution := range []string{"answer", "skip", "cancel"} {
		t.Run(resolution, func(t *testing.T) {
			s := testServer(t)
			events := make(chan domain.Event, 16)
			s.mu.Lock()
			s.subscribers[events] = true
			s.mu.Unlock()
			defer func() { s.mu.Lock(); delete(s.subscribers, events); s.mu.Unlock() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			scope := domain.ExecutionScope{ChatID: "chat", ToolMessageID: "question", RunID: "run", Bot: domain.Bot{ID: "echo", Name: "Echo"}}
			go func() {
				_, err := s.askUserQuestions(ctx, scope, json.RawMessage(`{"questions":[{"id":"scope","question":"Which scope?","options":["Core"]}]}`))
				done <- err
			}()
			nextQuestionEvent := func() domain.Event {
				t.Helper()
				deadline := time.After(5 * time.Second)
				for {
					select {
					case event := <-events:
						if event.Type == "questions" {
							return event
						}
					case <-deadline:
						t.Fatal("missing question event")
					}
				}
			}
			appeared := nextQuestionEvent()
			if appeared.ChatID != scope.ChatID || appeared.BotID != scope.Bot.ID || appeared.RunID != scope.RunID {
				t.Fatal("question event lost its scope", appeared)
			}
			assertQuestionState(t, s, domain.PendingQuestion{ChatID: "chat", MessageID: "question", BotID: "echo"})
			if resolution == "cancel" {
				cancel()
			} else if err := s.resolveChatQuestions("chat", "question", []PlanAnswer{{QuestionID: "scope", OptionIndex: 0}}, resolution == "skip"); err != nil {
				t.Fatal(err)
			}
			cleared := nextQuestionEvent()
			var payload struct {
				ID       string `json:"id"`
				Resolved bool   `json:"resolved"`
			}
			if err := json.Unmarshal(cleared.Payload, &payload); err != nil || payload.ID != "question" || !payload.Resolved {
				t.Fatal("missing question resolution", cleared, err)
			}
			// A refresh triggered by the clearing event must already see no question.
			assertQuestionState(t, s)
			select {
			case err := <-done:
				if (err != nil) != (resolution == "cancel") {
					t.Fatal("unexpected question result", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("question did not finish")
			}
		})
	}
}
