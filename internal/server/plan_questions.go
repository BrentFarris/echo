package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

type planQuestionResolution struct {
	answers []sessions.PlanAnswer
	skipped bool
}

type planQuestionWait struct {
	turnID         string
	assistantTurn  int
	callID         string
	callOrder      int
	set            *sessions.PlanQuestionSet
	resolved       chan planQuestionResolution
	resolutionSent bool
}

type planQuestionToolOutput struct {
	Answers []sessions.PlanAnswer `json:"answers,omitempty"`
	Skipped bool                  `json:"skipped,omitempty"`
}

func preparePlanQuestions(callID, arguments string, planMode bool, questionRound int) (*sessions.PlanQuestionSet, *tools.ExecutionError) {
	if !planMode {
		return nil, &tools.ExecutionError{Code: "tool_not_allowed", Message: "ask_user_questions is only available in Plan mode"}
	}
	if questionRound > tools.MaxPlanQuestionRounds {
		return nil, &tools.ExecutionError{
			Code:    "question_limit_reached",
			Message: fmt.Sprintf("You have already asked the user %d rounds of questions this turn, which is the maximum. Finalize the plan now with the information you have; do not ask more questions.", tools.MaxPlanQuestionRounds),
		}
	}
	args, err := tools.ParseAskUserQuestionsArgs(json.RawMessage(arguments))
	if err != nil {
		return nil, &tools.ExecutionError{Code: "invalid_arguments", Message: err.Error()}
	}
	set := &sessions.PlanQuestionSet{QuestionSetID: callID, Questions: make([]sessions.PlanQuestion, 0, len(args.Questions))}
	for _, question := range args.Questions {
		options := make([]string, 0, len(question.Options))
		for _, option := range question.Options {
			if option = strings.TrimSpace(option); option != "" {
				options = append(options, option)
			}
		}
		set.Questions = append(set.Questions, sessions.PlanQuestion{
			ID: strings.TrimSpace(question.ID), Question: strings.TrimSpace(question.Question), Options: options,
		})
	}
	return set, nil
}

func (s *chatSession) awaitPlanQuestions(ctx context.Context, wait *planQuestionWait) tools.ExecutionResult {
	var resolution planQuestionResolution
	select {
	case resolution = <-wait.resolved:
	case <-ctx.Done():
		s.clearPendingPlanQuestions(wait)
		return tools.ExecutionResult{
			Tool:  tools.AskUserQuestionsToolName,
			Error: &tools.ExecutionError{Code: "canceled", Message: "The clarifying questions were canceled before an answer was provided."},
		}
	}
	s.clearPendingPlanQuestions(wait)
	return tools.ExecutionResult{
		Tool: tools.AskUserQuestionsToolName, Success: true,
		Output: planQuestionToolOutput{Answers: clonePlanAnswers(resolution.answers), Skipped: resolution.skipped},
	}
}

func (s *chatSession) clearPendingPlanQuestions(wait *planQuestionWait) {
	s.mu.Lock()
	if s.pendingPlanQuestions == wait {
		s.pendingPlanQuestions = nil
	}
	s.mu.Unlock()
}

func (m *chatSessionManager) resolvePlanQuestions(c *client, workspaceID, chatID, surfaceValue, questionSetID string, answers []sessions.PlanAnswer, skipped bool, requestID string) {
	surface, surfaceErr := normalizeChatSurface(surfaceValue)
	if surfaceErr != nil {
		m.commandErrorForSurface(c, workspaceID, chatSurfaceMain, "invalid_surface", surfaceErr.Error(), requestID)
		return
	}
	parent, err := m.get(workspaceID)
	if err != nil {
		m.commandErrorForSurface(c, workspaceID, surface, "invalid_workspace", err.Error(), requestID)
		return
	}
	if surface == chatSurfaceCode {
		if err := parent.ensureCodeChat(); err != nil {
			m.commandErrorForSurface(c, workspaceID, surface, "session_load_failed", err.Error(), requestID)
			return
		}
	}
	session, resolvedChatID, err := parent.resolveSurfaceTab(chatID, surface)
	if err != nil {
		m.commandErrorForTabSurface(c, workspaceID, chatID, surface, "invalid_chat", err.Error(), requestID)
		return
	}

	questionSetID = strings.TrimSpace(questionSetID)
	session.mu.Lock()
	wait := session.pendingPlanQuestions
	if wait == nil || wait.set == nil || wait.set.QuestionSetID != questionSetID {
		session.mu.Unlock()
		m.commandErrorForTabSurface(c, workspaceID, resolvedChatID, surface, "plan_questions_not_pending", "these clarifying questions are no longer awaiting answers", requestID)
		return
	}
	normalized, validationErr := validatePlanAnswers(wait.set, answers, skipped)
	if validationErr != nil {
		session.mu.Unlock()
		m.commandErrorForTabSurface(c, workspaceID, resolvedChatID, surface, "invalid_plan_answers", validationErr.Error(), requestID)
		return
	}
	if wait.resolutionSent {
		session.mu.Unlock()
		m.commandErrorForTabSurface(c, workspaceID, resolvedChatID, surface, "plan_questions_already_answered", "these clarifying questions were already answered", requestID)
		return
	}
	wait.resolutionSent = true
	wait.resolved <- planQuestionResolution{answers: normalized, skipped: skipped}
	if activity := activeToolActivity(session.active, wait.assistantTurn, wait.callID); activity != nil {
		activity.Status = "answered"
		activity.Answers = clonePlanAnswers(normalized)
		activity.Skipped = skipped
	}
	session.emitLocked(map[string]any{
		"type": "plan_questions_resolved", "turnId": wait.turnID, "turn": wait.assistantTurn,
		"callId": wait.callID, "callOrder": wait.callOrder, "tool": tools.AskUserQuestionsToolName,
		"planQuestions": wait.set, "answers": normalized, "skipped": skipped,
	})
	session.mu.Unlock()
}

func validatePlanAnswers(set *sessions.PlanQuestionSet, answers []sessions.PlanAnswer, skipped bool) ([]sessions.PlanAnswer, error) {
	if set == nil {
		return nil, fmt.Errorf("clarifying question set is missing")
	}
	if skipped {
		return nil, nil
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("answer every question, or skip the questions")
	}
	if len(answers) > len(set.Questions) {
		return nil, fmt.Errorf("too many answers for the given questions")
	}
	questions := make(map[string]sessions.PlanQuestion, len(set.Questions))
	for _, question := range set.Questions {
		questions[question.ID] = question
	}
	seen := make(map[string]bool, len(answers))
	normalized := make([]sessions.PlanAnswer, 0, len(answers))
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
			if len(answer.Text) > tools.MaxPlanAnswerTextLen {
				return nil, fmt.Errorf("answer for question %q exceeds %d characters", answer.QuestionID, tools.MaxPlanAnswerTextLen)
			}
			answer.OptionIndex = -1
		} else if answer.OptionIndex < 0 || answer.OptionIndex >= len(question.Options) {
			return nil, fmt.Errorf("question %q needs a valid option selection or free-text answer", answer.QuestionID)
		}
		normalized = append(normalized, answer)
	}
	for _, question := range set.Questions {
		if !seen[question.ID] {
			return nil, fmt.Errorf("question %q is unanswered", question.ID)
		}
	}
	return normalized, nil
}

func activeToolActivity(turn *sessions.Turn, assistantTurn int, callID string) *sessions.ToolActivity {
	if turn == nil {
		return nil
	}
	for i := range turn.AssistantTurns {
		if turn.AssistantTurns[i].Number != assistantTurn {
			continue
		}
		for j := range turn.AssistantTurns[i].Tools {
			if turn.AssistantTurns[i].Tools[j].CallID == callID {
				return &turn.AssistantTurns[i].Tools[j]
			}
		}
	}
	return nil
}

func clonePlanAnswers(answers []sessions.PlanAnswer) []sessions.PlanAnswer {
	if len(answers) == 0 {
		return nil
	}
	return append([]sessions.PlanAnswer(nil), answers...)
}
