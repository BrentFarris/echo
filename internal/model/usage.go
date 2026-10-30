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
	// An output/total-only event cannot establish an accurate input baseline.
	if input == nil || *input < 0 {
		return nil
	}
	out := &domain.TokenUsage{InputTokens: *input}
	if output != nil && *output >= 0 {
		out.OutputTokens = *output
	}
	out.TotalTokens = out.InputTokens + out.OutputTokens
	if u.Total != nil && *u.Total >= out.TotalTokens {
		out.TotalTokens = *u.Total
	}
	return out
}
