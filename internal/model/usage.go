package model

import "github.com/brent/echo/internal/domain"

type wireUsage struct {
	PromptDetails *wireInputDetails `json:"prompt_tokens_details"`
	InputDetails  *wireInputDetails `json:"input_tokens_details"`
	Prompt        *int              `json:"prompt_tokens"`
	Completion    *int              `json:"completion_tokens"`
	Input         *int              `json:"input_tokens"`
	Output        *int              `json:"output_tokens"`
	Total         *int              `json:"total_tokens"`
}

type wireInputDetails struct {
	Cached *int `json:"cached_tokens"`
}

func (u wireUsage) normalized() *domain.TokenUsage {
	input, output := u.Prompt, u.Completion
	details := u.PromptDetails
	if input == nil {
		input = u.Input
		details = u.InputDetails
	}
	if output == nil {
		output = u.Output
	}
	inputMissing, outputMissing := input == nil || *input < 0, output == nil || *output < 0
	if inputMissing && outputMissing {
		return nil
	}
	out := &domain.TokenUsage{InputMissing: inputMissing, OutputMissing: outputMissing}
	if !inputMissing {
		out.InputTokens = *input
		if details != nil && details.Cached != nil && *details.Cached >= 0 && *details.Cached <= *input {
			out.CachedInputTokens = details.Cached
		}
	}
	if !outputMissing {
		out.OutputTokens = *output
	}
	out.TotalTokens = out.InputTokens + out.OutputTokens
	if u.Total != nil && *u.Total >= out.TotalTokens {
		out.TotalTokens = *u.Total
	}
	return out
}
