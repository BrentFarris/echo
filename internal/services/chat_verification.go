package services

import (
	"context"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/llm"
)

// chatVerificationMaxAttempts bounds how many repair rounds Echo drives after
// an auto-verification failure before it finalizes the chat with a visible
// failure notice instead of a hard block.
const (
	chatVerificationMaxAttempts = 2
	chatVerificationToolName    = "verification"
)

// chatVerificationAction tells the chat loop how to proceed after a
// verification pass without the helper reaching into loop state.
type chatVerificationAction int

const (
	// chatVerificationActionVerified means the pass did not fail
	// (passed/skipped/unverified); finalize the turn normally.
	chatVerificationActionVerified chatVerificationAction = iota
	// chatVerificationActionRetryRepair means the pass failed and the repair
	// attempt budget remains; feed the report back to the model.
	chatVerificationActionRetryRepair
	// chatVerificationActionFinalizedFailed means the pass failed and the
	// repair attempt budget is exhausted; finalize with a visible notice.
	chatVerificationActionFinalizedFailed
)

// chatVerificationToolCall returns the synthetic tool activity record used to
// surface the auto-verification pass in the chat transcript. It keeps an empty
// ID so repeated activity updates merge into a single entry.
func chatVerificationToolCall() llm.ToolCall {
	return llm.ToolCall{
		Type: "function",
		Function: llm.FunctionCall{
			Name:      chatVerificationToolName,
			Arguments: "{}",
		},
	}
}

// checkChatFileChanges runs Echo's detected verification commands for the
// changed files accumulated during a chat turn. It mirrors the Kanban
// post-execution verification gate, surfacing progress as a synthetic tool
// activity entry that is never injected into the model's messages. If the
// SystemService has a runner override set (tests), it is used instead of the
// production runner.
func (s *SystemService) checkChatFileChanges(ctx context.Context, workspace Workspace, streamID string, messageID string, changedPaths map[string]bool, attempt int) (kanbanVerificationReport, chatVerificationAction, error) {
	call := chatVerificationToolCall()
	s.updateToolActivity(workspace.ID, streamID, messageID, call, "running", "", "", "Running detected verification on changed files...")

	runner := s.chatVerificationRunner
	if runner == nil {
		runner = s.runKanbanVerification
	}
	report, err := runner(ctx, workspace, sortedKanbanChangedPaths(changedPaths))
	if err != nil {
		s.updateToolActivity(workspace.ID, streamID, messageID, call, "error", "", err.Error(), "")
		return report, chatVerificationActionFinalizedFailed, err
	}

	status := "complete"
	errorText := ""
	if !kanbanVerificationReportSucceeded(report) {
		status = "error"
		errorText = report.Message
	}
	s.updateToolActivity(workspace.ID, streamID, messageID, call, status, report.Message, errorText, kanbanVerificationReportText(report))

	if !kanbanVerificationReportSucceeded(report) {
		if attempt < chatVerificationMaxAttempts {
			return report, chatVerificationActionRetryRepair, nil
		}
		return report, chatVerificationActionFinalizedFailed, nil
	}
	return report, chatVerificationActionVerified, nil
}

// chatVerificationRepairMessage builds the model-facing repair instruction for
// a failed chat verification pass. Like the Kanban repair prompt and the skill
// checkpoint reminders, it is kept in-memory and not appended to chat history.
func chatVerificationRepairMessage(report kanbanVerificationReport, attempt int) string {
	return "Automatic verification failed (attempt " + strconv.Itoa(attempt) + "). Fix the code using the available tools, re-run the checks yourself, and provide the final answer only after they pass.\n\n" + kanbanVerificationReportText(report)
}

// chatVerificationResultLine returns the concise, persisted line appended to
// the assistant message when verification did not fail. Empty when there is
// nothing meaningful to append.
func chatVerificationResultLine(report kanbanVerificationReport) string {
	switch report.Status {
	case kanbanVerificationStatusPassed:
		commands := verificationCommandNames(report)
		if commands == "" {
			return "\n\nVerification passed."
		}
		return "\n\nVerification passed (ran " + commands + ")."
	case kanbanVerificationStatusUnverified:
		return "\n\nVerification: no matching test/build command was detected for the changed files, so Echo could not auto-verify."
	case kanbanVerificationStatusSkipped:
		return ""
	default:
		return ""
	}
}

// chatVerificationFailedNotice returns the visible fallback line appended to
// the assistant message when verification keeps failing past the repair budget.
func chatVerificationFailedNotice(report kanbanVerificationReport, attempt int) string {
	return "\n\n\u26a0\ufe0f Verification failed after " + strconv.Itoa(attempt) + " attempt(s): " + report.Message
}

func verificationCommandNames(report kanbanVerificationReport) string {
	names := make([]string, 0, len(report.Commands))
	for _, command := range report.Commands {
		names = append(names, command.Command)
	}
	return strings.Join(names, ", ")
}
