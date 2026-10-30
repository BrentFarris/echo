package model

import "github.com/brent/echo/internal/domain"

type wireUsage struct {
	Prompt     *int `json:"prompt_tokens"`
	Completion *int `json:"completion_tokens"`
	Input      *int `json:"input_tokens"`
	Output     *int `json:"output_tokens"`
	Total      *int `json:"total_tokens"`
}

func (u wireUsage) normalized() *domain.TokenUsage {
	input, output := u.Prompt, u.Completion
	if input == nil {
		input = u.Input
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
