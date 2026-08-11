package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
)

// PlanQuestion is one structured clarifying question shown to the user.
type PlanQuestion struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// PlanQuestionSet is the full set of questions for a single ask_user_questions
// call. QuestionSetID is the tool call ID, which uniquely keys the wait.
type PlanQuestionSet struct {
	QuestionSetID string         `json:"questionSetId"`
	Questions     []PlanQuestion `json:"questions"`
}

// PlanAnswer is one user answer. OptionIndex is 0-based; -1 (or a negative
// value) means a free-text answer captured in Text. Questions without options
// accept free text only.
type PlanAnswer struct {
	QuestionID  string `json:"questionId"`
	OptionIndex int    `json:"optionIndex"`
	Text        string `json:"text,omitempty"`
}

// planQuestionResolution is delivered to a blocked ask_user_questions call
// when the user submits answers or skips.
type planQuestionResolution struct {
	answers []PlanAnswer
	skipped bool
}

// planQuestionWait tracks a pending question set while the chat turn is parked.
type planQuestionWait struct {
	chatID   string
	set      *PlanQuestionSet
	resolved chan planQuestionResolution
}

// planAnswersResult is the JSON payload returned to the model as the tool result.
type planAnswersResult struct {
	Tool    string       `json:"tool"`
	Success bool         `json:"success"`
	Answers []PlanAnswer `json:"answers,omitempty"`
	Skipped bool         `json:"skipped,omitempty"`
}

// executePlanQuestions intercepts ask_user_questions tool calls. It validates
// and persists the question set, publishes it to the UI, then parks the turn
// until the user submits answers, skips, or the turn is cancelled.
func (s *SystemService) executePlanQuestions(ctx context.Context, workspace Workspace, chatID string, streamID string, messageID string, call llm.ToolCall, readOnlyOnly bool, questionRound int) chatToolCallExecution {
	if !readOnlyOnly {
		return s.planQuestionToolError(workspace.ID, streamID, messageID, call, "tool_not_allowed", "ask_user_questions is only available in plan mode")
	}
	if questionRound > tools.MaxPlanQuestionRounds {
		return s.planQuestionToolError(workspace.ID, streamID, messageID, call, "question_limit_reached",
			fmt.Sprintf("You have already asked the user %d rounds of questions this turn, which is the maximum. Finalize the plan now with the information you have; do not ask more questions.", tools.MaxPlanQuestionRounds))
	}

	args, err := tools.ParseAskUserQuestionsArgs(json.RawMessage(call.Function.Arguments))
	if err != nil {
		return s.planQuestionToolError(workspace.ID, streamID, messageID, call, "invalid_arguments", err.Error())
	}

	set := &PlanQuestionSet{
		QuestionSetID: call.ID,
		Questions:     make([]PlanQuestion, 0, len(args.Questions)),
	}
	for _, question := range args.Questions {
		set.Questions = append(set.Questions, PlanQuestion{
			ID:       strings.TrimSpace(question.ID),
			Question: strings.TrimSpace(question.Question),
			Options:  append([]string(nil), question.Options...),
		})
	}

	wait := &planQuestionWait{
		chatID:   chatID,
		set:      set,
		resolved: make(chan planQuestionResolution, 1),
	}
	s.chatMu.Lock()
	s.planQuestionWaits[set.QuestionSetID] = wait
	s.chatMu.Unlock()
	defer func() {
		s.chatMu.Lock()
		delete(s.planQuestionWaits, set.QuestionSetID)
		s.chatMu.Unlock()
	}()

	s.publishPlanQuestions(workspace.ID, chatID, streamID, messageID, call, set)

	var resolution planQuestionResolution
	select {
	case resolution = <-wait.resolved:
	case <-ctx.Done():
		return s.planQuestionToolError(workspace.ID, streamID, messageID, call, "cancelled", "The plan questions were cancelled before an answer was provided.")
	}

	result := planAnswersResult{
		Tool:    tools.AskUserQuestionsToolName,
		Success: true,
		Answers: resolution.answers,
		Skipped: resolution.skipped,
	}
	data, err := json.Marshal(result)
	if err != nil {
		return s.planQuestionToolError(workspace.ID, streamID, messageID, call, "marshal_error", err.Error())
	}
	execution := tools.ExecutionResult{
		Tool:    tools.AskUserQuestionsToolName,
		Success: true,
		Output:  result,
	}
	s.updateToolActivity(workspace.ID, streamID, messageID, call, "complete", string(data), "", "")
	return chatToolCallExecution{
		Messages: s.loggedToolResultMessages(call, execution, data),
	}
}

// planQuestionToolError builds a failed tool execution result and publishes it.
func (s *SystemService) planQuestionToolError(workspaceID string, streamID string, messageID string, call llm.ToolCall, code string, message string) chatToolCallExecution {
	result := tools.ExecutionResult{
		Tool:    tools.AskUserQuestionsToolName,
		Success: false,
		Error: &tools.ExecutionError{
			Code:    code,
			Message: message,
		},
	}
	data, _ := json.Marshal(result)
	s.updateToolActivity(workspaceID, streamID, messageID, call, "error", string(data), message, "")
	return chatToolCallExecution{
		Messages: s.loggedToolResultMessages(call, result, data),
	}
}

// publishPlanQuestions records the awaiting-input tool activity on the chat
// message and emits both a tool_call event (carrying the typed question set)
// and a plan_questions event so clients render interactive cards.
func (s *SystemService) publishPlanQuestions(workspaceID string, chatID string, streamID string, messageID string, call llm.ToolCall, set *PlanQuestionSet) {
	activity := ChatToolActivity{
		ID:            call.ID,
		Name:          call.Function.Name,
		Arguments:     call.Function.Arguments,
		Status:        "awaiting_input",
		PlanQuestions: set,
	}
	s.mutateChatMessage(workspaceID, messageID, func(message *ChatMessage) {
		for i := range message.ToolCalls {
			if message.ToolCalls[i].ID != "" && message.ToolCalls[i].ID == activity.ID {
				message.ToolCalls[i] = activity
				return
			}
			if message.ToolCalls[i].ID == "" && message.ToolCalls[i].Name == activity.Name {
				message.ToolCalls[i] = activity
				return
			}
		}
		message.ToolCalls = append(message.ToolCalls, activity)
	}, ChatStreamEvent{
		WorkspaceID:   workspaceID,
		ChatID:        chatID,
		StreamID:      streamID,
		MessageID:     messageID,
		Type:          "plan_questions",
		PlanQuestions: set,
		ToolCall:      &activity,
	})
}

// SubmitPlanAnswers delivers user answers to a parked plan question set and
// unblocks the chat turn so the model can finalize the plan.
func (s *SystemService) SubmitPlanAnswers(workspaceID string, chatID string, questionSetID string, answers []PlanAnswer) (ChatSession, error) {
	return s.resolvePlanQuestions(workspaceID, chatID, questionSetID, answers, false)
}

// SkipPlanQuestions unblocks a parked plan question set without answers so the
// model finalizes with its best judgment.
func (s *SystemService) SkipPlanQuestions(workspaceID string, chatID string, questionSetID string) (ChatSession, error) {
	return s.resolvePlanQuestions(workspaceID, chatID, questionSetID, nil, true)
}

func (s *SystemService) resolvePlanQuestions(workspaceID string, chatID string, questionSetID string, answers []PlanAnswer, skipped bool) (ChatSession, error) {
	questionSetID = strings.TrimSpace(questionSetID)
	s.chatMu.Lock()
	defer s.chatMu.Unlock()

	session := s.chatSessionForIDLocked(workspaceID, chatID)
	if session == nil {
		return ChatSession{}, fmt.Errorf("chat tab was not found")
	}
	if questionSetID == "" {
		return ChatSession{}, fmt.Errorf("plan question set id is required")
	}
	wait, ok := s.planQuestionWaits[questionSetID]
	if !ok {
		return ChatSession{}, fmt.Errorf("these plan questions are no longer awaiting answers")
	}
	if wait.chatID != "" && wait.chatID != chatID {
		return ChatSession{}, fmt.Errorf("these plan questions do not belong to this chat")
	}
	if !skipped && len(answers) == 0 {
		return ChatSession{}, fmt.Errorf("provide at least one answer, or skip the questions")
	}
	if err := validatePlanAnswers(wait.set, answers, skipped); err != nil {
		return ChatSession{}, err
	}

	select {
	case wait.resolved <- planQuestionResolution{answers: answers, skipped: skipped}:
	default:
		return ChatSession{}, fmt.Errorf("these plan questions were already answered")
	}
	return cloneChatSession(session), nil
}

// validatePlanAnswers ensures every answer references a known question and that
// each question carries either a valid option selection or free text.
func validatePlanAnswers(set *PlanQuestionSet, answers []PlanAnswer, skipped bool) error {
	if set == nil {
		return fmt.Errorf("plan question set is missing")
	}
	if skipped {
		return nil
	}
	if len(answers) > len(set.Questions) {
		return fmt.Errorf("too many answers for the given questions")
	}
	byID := make(map[string]PlanQuestion, len(set.Questions))
	for _, question := range set.Questions {
		byID[question.ID] = question
	}
	seen := make(map[string]bool, len(answers))
	for _, answer := range answers {
		question, ok := byID[answer.QuestionID]
		if !ok {
			return fmt.Errorf("answer references unknown question %q", answer.QuestionID)
		}
		if seen[answer.QuestionID] {
			return fmt.Errorf("duplicate answer for question %q", answer.QuestionID)
		}
		seen[answer.QuestionID] = true
		if strings.TrimSpace(answer.Text) != "" {
			if len(answer.Text) > tools.MaxPlanAnswerTextLen {
				return fmt.Errorf("answer for question %q exceeds %d characters", answer.QuestionID, tools.MaxPlanAnswerTextLen)
			}
			continue
		}
		if answer.OptionIndex >= 0 {
			if answer.OptionIndex >= len(question.Options) {
				return fmt.Errorf("answer for question %q selects an option that does not exist", answer.QuestionID)
			}
			continue
		}
		return fmt.Errorf("question %q needs an option selection or free text", answer.QuestionID)
	}
	for _, question := range set.Questions {
		if !seen[question.ID] {
			return fmt.Errorf("question %q is unanswered", question.ID)
		}
	}
	return nil
}
