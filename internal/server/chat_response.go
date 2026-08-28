package server

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/brent/echo/internal/llm"
)

const (
	maxEmptyAssistantRetries  = 2
	maxTransientStreamRetries = 1
	// maxTruncationContinues bounds how many times a turn may be continued
	// after the model stops at its output token limit before failing.
	maxTruncationContinues = 2
	// compressionFailureCooldownRounds backs off automatic compression for
	// several rounds after it cannot reclaim anything (for example when the
	// recent tail alone exceeds the target). A one-round cooldown would retry
	// every other round and re-fail identically until the context hits the
	// hard limit. Hard-limit preflights are unaffected and still run every
	// round as the safety valve.
	compressionFailureCooldownRounds = 8
	// maxContextLengthRecoveries bounds how many times a turn may recover from
	// a provider context-size rejection by forcing compression against the
	// observed limit before failing.
	maxContextLengthRecoveries = 3
)

var errTruncatedResponse = errors.New("response hit the token limit")

var observedContextLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"n_ctx"\s*:\s*(\d+)`),
	regexp.MustCompile(`available context size \((\d+)\)`),
}

// parseObservedContextLimit extracts the endpoint's real context window from a
// provider rejection message when it reports one. Echo's configured Context
// Length can be optimistic; the provider's number is authoritative for the
// rest of the turn.
func parseObservedContextLimit(err error) int {
	if err == nil {
		return 0
	}
	text := err.Error()
	for _, pattern := range observedContextLimitPatterns {
		match := pattern.FindStringSubmatch(text)
		if len(match) > 1 {
			value, convErr := strconv.Atoi(match[1])
			if convErr == nil && value > 0 {
				return value
			}
		}
	}
	return 0
}

func finishReasonError(finishReason string, hasToolCalls bool) error {
	switch strings.TrimSpace(finishReason) {
	case "", "stop":
		return nil
	case "tool_calls":
		if hasToolCalls {
			return nil
		}
		return errors.New("The LLM stopped to call a tool, but no tool call was received. Try again.")
	case "length":
		return errTruncatedResponse
	case "content_filter":
		return errors.New("The LLM stopped because the provider filtered the response.")
	default:
		return fmt.Errorf("The LLM stopped before completing normally (finish_reason: %s).", finishReason)
	}
}

// isTruncationFinishError reports whether a finish-reason error means the model
// hit its output token limit. Those turns can be auto-continued instead of
// failing.
func isTruncationFinishError(err error) bool {
	return errors.Is(err, errTruncatedResponse)
}

func truncationContinueMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "Your previous response was cut off because it hit the output token limit. " +
			"Continue exactly from where it stopped and finish the work. " +
			"Do not repeat content that was already produced.",
	}
}

func truncationExhaustedError(attempts int) error {
	return fmt.Errorf(
		"The LLM hit its output token limit %d times in a row and could not finish the response. "+
			"Increase Max Tokens or reduce the thinking-token budget for this endpoint and try again.",
		attempts,
	)
}

// withoutThinkingSettings returns a copy of settings with thinking disabled.
// Truncated responses usually mean reasoning tokens consumed the output
// budget; continuing with thinking off gives the actual answer (or tool
// calls) the full Max Tokens instead of re-spending it on reasoning.
func withoutThinkingSettings(settings llm.Settings) llm.Settings {
	settings.ThinkingTokenBudget = 0
	return settings
}

func isEmptyAssistantResponse(content string, toolCalls []llm.ToolCall) bool {
	return strings.TrimSpace(content) == "" && len(toolCalls) == 0
}

func emptyAssistantRetryMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "Your previous response completed without visible content or a tool call. " +
			"Continue from the current context and now return either a visible final answer or a valid tool call. " +
			"Do not return another reasoning-only response.",
	}
}

func emptyAssistantResponseError() error {
	return fmt.Errorf(
		"The LLM completed %d times without producing visible content or a tool call. "+
			"It may be exhausting its output budget on reasoning.",
		maxEmptyAssistantRetries+1,
	)
}

func transientStreamRetryMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "The previous model stream ended before returning usable content. " +
			"Retry once from the current context and produce a valid tool call or final answer.",
	}
}

func contextSummaryContinueMessage() llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: "Your previous response completed without the summary. " +
			"Continue from the current context and now return the complete continuation summary with all required headings. " +
			"Do not return another empty or reasoning-only response.",
	}
}
