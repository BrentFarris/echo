// Decision-model agent-mode selection (Phase 3). When the
// DecisionModelAgentMode setting is enabled and a decision-model endpoint is
// configured, Echo asks the decision model which agent mode should consume
// the user's prompt and overrides the mode resolved from the request. The
// override is always surfaced (turn event + trajectory) so it stays visible.
// Everything lives in this file so the chat pipeline only sees a single
// (agentmodes.Mode, float64, bool) call.
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brent/echo/internal/agentmodes"
	"github.com/brent/echo/internal/llm"
)

const (
	// decisionAgentModeTimeout bounds the per-prompt mode query so a slow or
	// hung decision model never delays the chat turn noticeably. Fail-open:
	// on timeout the request's resolved mode is kept.
	decisionAgentModeTimeout = 2 * time.Second
	// decisionAgentModeStateLimit caps the prompt text sent as state so the
	// decision-model call stays cheap even for very long prompts.
	decisionAgentModeStateLimit = 2000
	// decisionAgentModeDescriptionLimit caps each mode description sent as
	// choice criteria so a long custom-mode prompt cannot bloat the request.
	decisionAgentModeDescriptionLimit = 160
)

// decisionModelAgentMode asks the decision model which agent mode should
// handle this prompt and returns the selected mode plus the probability
// assigned to it. The third return is false when the feature is disabled,
// the endpoint is unconfigured, the call fails, or the answer is unusable —
// callers keep the request's resolved mode in every failure case.
//
// Goal mode is deliberately excluded from the choices: autonomous execution
// must always be an explicit user decision, so the decision model can never
// select it. Call sites additionally skip the override entirely for explicit
// goal starts.
func (s *Server) decisionModelAgentMode(settings llm.Settings, workspacePath, prompt string) (agentmodes.Mode, float64, bool) {
	if s == nil || s.decisionModel == nil || !s.decisionModel.Enabled() || !settings.DecisionModelAgentMode {
		return agentmodes.Mode{}, 0, false
	}
	modes, err := s.modes.List(workspacePath)
	if err != nil {
		return agentmodes.Mode{}, 0, false
	}
	criteria := decisionAgentModeCriteria(modes)
	if len(criteria) < 2 {
		// Nothing meaningful to choose between.
		return agentmodes.Mode{}, 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), decisionAgentModeTimeout)
	defer cancel()
	response, err := s.decisionModel.Ask(ctx, decisionAgentModeState(prompt)).
		Choice("agentMode", "Which agent mode should handle this prompt?", criteria).
		Do()
	if err != nil {
		return agentmodes.Mode{}, 0, false
	}
	answer, ok := response.Answer("agentMode")
	if !ok {
		return agentmodes.Mode{}, 0, false
	}
	choice, confidence, found := answer.Best()
	if !found {
		return agentmodes.Mode{}, 0, false
	}
	// Modes from List are per-call clones, so returning one directly is safe.
	// Goal is rejected here too (defense in depth): it is excluded from the
	// criteria, but a misbehaving endpoint could still name it.
	for _, mode := range modes {
		if mode.ID == choice {
			if mode.ID == agentmodes.GoalID {
				break
			}
			return mode, confidence, true
		}
	}
	// Unknown or excluded choice (e.g. goal): keep the resolved mode.
	return agentmodes.Mode{}, 0, false
}

// decisionAgentModeCriteria builds the choice criteria from the workspace's
// available modes, keyed by mode ID. Goal mode is excluded so the decision
// model can never select autonomous execution.
func decisionAgentModeCriteria(modes []agentmodes.Mode) map[string]string {
	criteria := make(map[string]string, len(modes))
	for _, mode := range modes {
		if mode.ID == "" || mode.ID == agentmodes.GoalID {
			continue
		}
		criteria[mode.ID] = decisionAgentModeDescription(mode)
	}
	return criteria
}

// decisionAgentModeDescription returns a compact description of a mode for
// the decision model. Built-in modes get curated one-liners; custom modes
// are summarized from the first sentence of their prompt.
func decisionAgentModeDescription(mode agentmodes.Mode) string {
	switch mode.ID {
	case agentmodes.GeneralID:
		return "General-purpose assistant for conversation, questions, and everyday tasks with full tool access."
	case agentmodes.PlanID:
		return "Planning only: inspect and reason about the workspace without changing anything; produce an implementation plan instead of acting."
	case agentmodes.PairProgrammingID:
		return "Pair on implementation: make edits and run tools directly while keeping the user in the loop and asking clarifying questions."
	}
	return decisionAgentModePromptSummary(mode.Prompt, mode.Name)
}

// decisionAgentModePromptSummary condenses a custom mode's prompt to its
// first sentence, truncated by runes, with a fallback for empty prompts.
func decisionAgentModePromptSummary(prompt, name string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Sprintf("Custom agent mode %q.", name)
	}
	if end := strings.IndexAny(prompt, ".\n"); end >= 0 {
		prompt = strings.TrimSpace(prompt[:end])
	}
	runes := []rune(prompt)
	if len(runes) > decisionAgentModeDescriptionLimit {
		runes = append(runes[:decisionAgentModeDescriptionLimit-1], '…')
	}
	return string(runes)
}

// decisionAgentModeState builds the compact state string sent to the decision
// model. Only the user's prompt is relevant to the routing decision, and it
// is truncated by runes to keep the call cheap.
func decisionAgentModeState(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if runes := []rune(prompt); len(runes) > decisionAgentModeStateLimit {
		prompt = string(runes[:decisionAgentModeStateLimit])
	}
	return "User prompt:\n" + prompt
}
