package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

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
					return m
				}
				if m.Status != "" {
					t.Fatalf("question finished without answers: %s", m.Status)
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("question never became pending")
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
	if e := s.resolveChatQuestions(c.ID, tool.ID, []PlanAnswer{{QuestionID: "scope", OptionIndex: 1}, {QuestionID: "lang", OptionIndex: -1, Text: "Go"}}, false); e != nil {
		t.Fatal(e)
	}
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
	waitDispatch(t, d)
	var parsed struct {
		Skipped  bool           `json:"skipped"`
		Answers  []PlanAnswer   `json:"answers"`
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
