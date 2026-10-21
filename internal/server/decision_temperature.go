// Decision-model temperature selection (Phase 2). When the
// DecisionModelTemperature setting is enabled and a decision-model endpoint is
// configured, Echo asks the decision model how creative the response to the
// current prompt should be and overrides the endpoint's configured
// temperature for that turn. Everything lives in this file so the chat
// pipeline only sees a single (float64, bool) call.
package server

import (
	"context"
	"strings"
	"time"

	"github.com/brent/echo/internal/llm"
)

const (
	// decisionTemperatureTimeout bounds the per-prompt temperature query so a
	// slow or hung decision model never delays the chat turn noticeably.
	// Fail-open: on timeout the endpoint's configured temperature is kept.
	decisionTemperatureTimeout = 2 * time.Second
	// decisionTemperatureFloor and decisionTemperatureCeiling map the score
	// legend onto the valid user-facing temperature range. Inbound chat
	// temperatures are validated to [0,1], so the override stays inside it.
	decisionTemperatureFloor = 0.1
	// decisionTemperatureCeiling is the temperature used for the most
	// creative band.
	decisionTemperatureCeiling = 1.0
	// decisionTemperatureMaxScore is the highest legend index of the score
	// question (the legend has four entries).
	decisionTemperatureMaxScore = 3
	// decisionTemperatureStateLimit caps the prompt text sent as state so the
	// decision-model call stays cheap even for very long prompts.
	decisionTemperatureStateLimit = 2000
)

// decisionTemperatureLegend maps score indices onto response styles. The
// decision model returns an expected index in [0, max] which is linearly
// mapped onto [decisionTemperatureFloor, decisionTemperatureCeiling].
var decisionTemperatureLegend = []string{
	"Deterministic: code edits, exact commands, factual lookups",
	"Focused: mostly precise with minor flexibility",
	"Balanced: general conversation and mixed tasks",
	"Creative: brainstorming, writing, open-ended exploration",
}

// decisionModelTemperature asks the decision model how creative the response
// to this prompt should be and returns the temperature to use for the turn.
// The second return is false when the feature is disabled, the endpoint is
// unconfigured, the call fails, or the answer is unusable — callers keep
// their configured temperature in every failure case.
func (s *Server) decisionModelTemperature(settings llm.Settings, prompt string) (float64, bool) {
	if s == nil || s.decisionModel == nil || !s.decisionModel.Enabled() || !settings.DecisionModelTemperature {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), decisionTemperatureTimeout)
	defer cancel()
	response, err := s.decisionModel.Ask(ctx, decisionTemperatureState(prompt)).
		Score("temperature", "How creative or exploratory should the assistant's response to this prompt be?", decisionTemperatureLegend).
		Do()
	if err != nil {
		return 0, false
	}
	answer, ok := response.Answer("temperature")
	if !ok {
		return 0, false
	}
	score := answer.Score
	if score < 0 {
		score = 0
	}
	if score > decisionTemperatureMaxScore {
		score = decisionTemperatureMaxScore
	}
	span := decisionTemperatureCeiling - decisionTemperatureFloor
	return decisionTemperatureFloor + (score/decisionTemperatureMaxScore)*span, true
}

// decisionTemperatureState builds the compact state string sent to the
// decision model. Only the user's prompt is relevant to the creativity
// decision, and it is truncated by runes to keep the call cheap.
func decisionTemperatureState(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if runes := []rune(prompt); len(runes) > decisionTemperatureStateLimit {
		prompt = string(runes[:decisionTemperatureStateLimit])
	}
	return "User prompt:\n" + prompt
}
