package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/brent/echo/internal/domain"
)

// AskUserQuestionsToolName is the core tool that lets a model ask the user
// structured clarifying questions. Its execution blocks the run until the
// owner answers through POST /api/v1/chats/{id}/questions/{messageId}, so the
// answers arrive as the ordinary tool result and the model continues the same
// run. The tool message itself persists the question set while it waits, so
// transcripts and snapshots restore the pending state without extra storage.
const AskUserQuestionsToolName = "ask_user_questions"

const (
	maxPlanQuestionOptions   = 3
	maxPlanQuestionIDLen     = 64
	maxPlanQuestionTextLen   = 500
	maxPlanQuestionOptionLen = 200
	maxPlanAnswerTextLen     = 2000
)

// PlanQuestion is one structured question delivered to the browser.
type PlanQuestion struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// PlanAnswer selects a zero-based option, or supplies free text when
// OptionIndex is negative.
type PlanAnswer struct {
	QuestionID  string `json:"questionId"`
	OptionIndex int    `json:"optionIndex"`
	Text        string `json:"text,omitempty"`
}

type planQuestionRequest struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

type askUserQuestionsArgs struct {
	Questions []planQuestionRequest `json:"questions"`
}

// ParseAskUserQuestionsArgs validates and parses the raw tool arguments. It
// returns a user-facing error when the payload is malformed or violates the
// question constraints.
func ParseAskUserQuestionsArgs(raw json.RawMessage) (askUserQuestionsArgs, error) {
	var args askUserQuestionsArgs
	if len(strings.TrimSpace(string(raw))) == 0 {
		return args, errors.New("ask_user_questions requires a non-empty questions array")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("ask_user_questions received invalid arguments: %v", err)
	}
	if len(args.Questions) == 0 {
		return args, errors.New("ask_user_questions requires at least one question")
	}
	seen := make(map[string]bool, len(args.Questions))
	for _, question := range args.Questions {
		id := strings.TrimSpace(question.ID)
		if id == "" {
			return args, errors.New("each question requires a non-empty id")
		}
		if len(id) > maxPlanQuestionIDLen {
			return args, fmt.Errorf("question id %q exceeds %d characters", id, maxPlanQuestionIDLen)
		}
		if seen[id] {
			return args, fmt.Errorf("duplicate question id %q", id)
		}
		seen[id] = true
		text := strings.TrimSpace(question.Question)
		if text == "" {
			return args, fmt.Errorf("question %q is missing its text", id)
		}
		if len(text) > maxPlanQuestionTextLen {
			return args, fmt.Errorf("question %q exceeds %d characters", id, maxPlanQuestionTextLen)
		}
		if len(question.Options) > maxPlanQuestionOptions {
			return args, fmt.Errorf("question %q has more than %d options", id, maxPlanQuestionOptions)
		}
		for _, option := range question.Options {
			if len(strings.TrimSpace(option)) > maxPlanQuestionOptionLen {
				return args, fmt.Errorf("question %q has an option exceeding %d characters", id, maxPlanQuestionOptionLen)
			}
		}
	}
	return args, nil
}

func questionSet(args askUserQuestionsArgs) []PlanQuestion {
	set := make([]PlanQuestion, 0, len(args.Questions))
	for _, question := range args.Questions {
		options := make([]string, 0, len(question.Options))
		for _, option := range question.Options {
			if option = strings.TrimSpace(option); option != "" {
				options = append(options, option)
			}
		}
		set = append(set, PlanQuestion{ID: strings.TrimSpace(question.ID), Question: strings.TrimSpace(question.Question), Options: options})
	}
	return set
}

// pendingQuestion is one awaiting ask_user_questions tool call, keyed by its
// tool message ID. The resolved channel carries exactly one resolution; the
// sent flag makes a second submit or skip a visible error instead of a race.
type pendingQuestion struct {
	chatID   string
	set      []PlanQuestion
	resolved chan questionResolution
	mu       sync.Mutex
	sent     bool
}

type questionResolution struct {
	answers []PlanAnswer
	skipped bool
}

// askUserQuestions registers the pending question set, notifies subscribers,
// and blocks the run until the owner answers or the dispatch is cancelled.
func (s *Server) askUserQuestions(ctx context.Context, scope domain.ExecutionScope, raw json.RawMessage) (any, error) {
	args, e := ParseAskUserQuestionsArgs(raw)
	if e != nil {
		return nil, e
	}
	set := questionSet(args)
	wait := &pendingQuestion{chatID: scope.ChatID, set: set, resolved: make(chan questionResolution, 1)}
	s.questionsMu.Lock()
	if s.pendingQuestions == nil {
		s.pendingQuestions = map[string]*pendingQuestion{}
	}
	s.pendingQuestions[scope.ToolMessageID] = wait
	s.questionsMu.Unlock()
	m := domain.Message{ID: scope.ToolMessageID, ChatID: scope.ChatID, RunID: scope.RunID, BotID: scope.Bot.ID, Author: scope.Bot.Name + " · " + AskUserQuestionsToolName, Role: "tool", Status: "running", Content: string(raw)}
	for _, a := range s.messageAlert(m) {
		_ = s.Store.AddQuestionAlert(a)
	}
	s.emit("questions", scope.ChatID, scope.RunID, scope.Bot.ID, map[string]any{"id": scope.ToolMessageID, "questions": set})
	defer s.clearPendingQuestion(scope.ToolMessageID, wait)
	select {
	case resolution := <-wait.resolved:
		return map[string]any{"answers": resolution.answers, "skipped": resolution.skipped, "questions": set}, nil
	case <-ctx.Done():
		return nil, errors.New("the clarifying questions were canceled before an answer was provided")
	}
}

func (s *Server) clearPendingQuestion(messageID string, wait *pendingQuestion) {
	s.questionsMu.Lock()
	if s.pendingQuestions[messageID] == wait {
		delete(s.pendingQuestions, messageID)
	}
	s.questionsMu.Unlock()
}

// resolveChatQuestions delivers the owner's answers to the run waiting on the
// given tool message. It mirrors the old plan_questions_submit/skip contract:
// every question must be answered exactly once, either with a valid option
// index or with free text, or the whole set is skipped.
func (s *Server) resolveChatQuestions(chatID, messageID string, answers []PlanAnswer, skipped bool) error {
	s.questionsMu.Lock()
	wait, ok := s.pendingQuestions[messageID]
	s.questionsMu.Unlock()
	if !ok || wait.chatID != chatID {
		return errors.New("these clarifying questions are no longer awaiting answers")
	}
	wait.mu.Lock()
	defer wait.mu.Unlock()
	if wait.sent {
		return errors.New("these clarifying questions were already answered")
	}
	normalized, e := ValidatePlanAnswers(wait.set, answers, skipped)
	if e != nil {
		return e
	}
	wait.sent = true
	_ = s.Store.ResolveQuestionAlert(chatID, messageID)
	wait.resolved <- questionResolution{answers: normalized, skipped: skipped}
	return nil
}

// ValidatePlanAnswers normalizes the submitted answers against the question
// set. Skipped sets return no answers; otherwise every question needs exactly
// one answer carrying a valid option selection or free text.
func ValidatePlanAnswers(set []PlanQuestion, answers []PlanAnswer, skipped bool) ([]PlanAnswer, error) {
	if skipped {
		return nil, nil
	}
	if len(answers) == 0 {
		return nil, errors.New("answer every question, or skip the questions")
	}
	if len(answers) > len(set) {
		return nil, errors.New("too many answers for the given questions")
	}
	questions := make(map[string]PlanQuestion, len(set))
	for _, question := range set {
		questions[question.ID] = question
	}
	seen := make(map[string]bool, len(answers))
	normalized := make([]PlanAnswer, 0, len(answers))
	for _, answer := range answers {
		answer.QuestionID = strings.TrimSpace(answer.QuestionID)
		answer.Text = strings.TrimSpace(answer.Text)
		question, ok := questions[answer.QuestionID]
		if !ok {
			return nil, fmt.Errorf("answer references unknown question %q", answer.QuestionID)
		}
		if seen[answer.QuestionID] {
			return nil, fmt.Errorf("duplicate answer for question %q", answer.QuestionID)
		}
		seen[answer.QuestionID] = true
		if answer.Text != "" {
			if len(answer.Text) > maxPlanAnswerTextLen {
				return nil, fmt.Errorf("answer for question %q exceeds %d characters", answer.QuestionID, maxPlanAnswerTextLen)
			}
			answer.OptionIndex = -1
		} else if answer.OptionIndex < 0 || answer.OptionIndex >= len(question.Options) {
			return nil, fmt.Errorf("question %q needs a valid option selection or free-text answer", answer.QuestionID)
		}
		normalized = append(normalized, answer)
	}
	for _, question := range set {
		if !seen[question.ID] {
			return nil, fmt.Errorf("question %q is unanswered", question.ID)
		}
	}
	return normalized, nil
}
