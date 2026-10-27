package model

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/brent/echo/internal/domain"
)

func ValidateSettings(e domain.Endpoint) error {
	for _, field := range []struct {
		name     string
		value    *float64
		min, max float64
	}{
		{"temperature", e.Temperature, 0, 2}, {"top P", e.TopP, 0, 1}, {"min P", e.MinP, 0, 1},
		{"frequency penalty", e.FrequencyPenalty, -2, 2}, {"presence penalty", e.PresencePenalty, -2, 2},
		{"repetition penalty", e.RepetitionPenalty, 0, 100},
	} {
		if field.value != nil && (math.IsNaN(*field.value) || math.IsInf(*field.value, 0) || *field.value < field.min || *field.value > field.max) {
			return fmt.Errorf("%s must be between %g and %g", field.name, field.min, field.max)
		}
	}
	if e.TopK != nil && *e.TopK < 0 {
		return errors.New("top K must be nonnegative")
	}
	if e.MaxTokens != nil && *e.MaxTokens < 1 {
		return errors.New("max tokens must be positive")
	}
	if e.ThinkingTokenBudget != nil && *e.ThinkingTokenBudget < -1 {
		return errors.New("thinking token budget must be -1 or nonnegative")
	}
	if e.ContextTokens < 0 {
		return errors.New("context length must be positive")
	}
	if e.CompressionThreshold != 0 && (e.CompressionThreshold < 1 || e.CompressionThreshold > 99) {
		return errors.New("compression threshold must be 1–99 percent")
	}
	if e.RequestTimeoutSeconds < 0 || e.RequestTimeoutSeconds > 86400 {
		return errors.New("request timeout must be 1–86400 seconds (0 uses the default)")
	}
	if e.StreamIdleTimeoutSeconds < -1 || e.StreamIdleTimeoutSeconds > 86400 {
		return errors.New("stream idle timeout must be -1, or 1–86400 seconds (0 uses the default)")
	}
	if !slices.Contains([]string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"}, e.ReasoningEffort) {
		return errors.New("unknown reasoning effort")
	}
	return nil
}

func generationParameters(e domain.Endpoint, g domain.Generation) map[string]any {
	model := g.Model
	if model == "" {
		model = e.Model
	}
	body := map[string]any{"model": model, "stream": true, "temperature": g.Temperature}
	// Leave unset provider-specific parameters out entirely. Explicit zeros survive.
	if e.TopK != nil {
		body["top_k"] = *e.TopK
	}
	if e.TopP != nil {
		body["top_p"] = *e.TopP
	}
	if e.MinP != nil {
		body["min_p"] = *e.MinP
	}
	if e.MaxTokens != nil {
		body["max_tokens"] = *e.MaxTokens
	}
	if e.FrequencyPenalty != nil {
		body["frequency_penalty"] = *e.FrequencyPenalty
	}
	if e.PresencePenalty != nil {
		body["presence_penalty"] = *e.PresencePenalty
	}
	if e.RepetitionPenalty != nil {
		body["repetition_penalty"] = *e.RepetitionPenalty
	}
	effort := g.Thinking
	if effort == "" || effort == "default" {
		effort = e.ReasoningEffort
	}
	if effort != "" {
		body["reasoning_effort"] = effort
		if effort == "none" {
			body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
		}
	} else if e.ThinkingTokenBudget != nil {
		body["chat_template_kwargs"] = map[string]any{"thinking_token_budget": *e.ThinkingTokenBudget}
	}
	return body
}
