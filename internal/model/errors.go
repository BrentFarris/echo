package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ContextOverflowError is transport-independent, and may be returned by a custom provider.
// Zero counts mean unknown. A generic HTTP 413 is deliberately not this error.
type ContextOverflowError struct {
	Message      string `json:"message"`
	LimitTokens  int    `json:"limitTokens,omitempty"`
	InputTokens  int    `json:"inputTokens,omitempty"`
	OutputTokens int    `json:"outputTokens,omitempty"`
}

func (e *ContextOverflowError) Error() string { return e.Message }
func IsContextOverflow(err error) bool {
	var overflow *ContextOverflowError
	return errors.As(err, &overflow)
}

var limitCount = regexp.MustCompile(`(?i)(?:maximum context (?:length|size)(?: is| of|:)?\s*|available context size\s*\(?)([0-9]+)`)

// A provider's "requested tokens" may include output reservation; do not label
// that combined number as input tokens.
var inputCount = regexp.MustCompile(`(?i)prompt\s*(?:contains|has|is|:)\s*([0-9]+)\s*tokens`)

func messageCount(pattern *regexp.Regexp, message string) int {
	match := pattern.FindStringSubmatch(message)
	if len(match) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(match[1])
	return n
}

func ProviderError(status int, raw []byte) error {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	detail := raw
	if len(envelope.Error) > 0 {
		detail = envelope.Error
	}
	var fields struct {
		Message    string `json:"message"`
		Code       string `json:"code"`
		Type       string `json:"type"`
		Limit      int    `json:"limitTokens"`
		Input      int    `json:"inputTokens"`
		Output     int    `json:"outputTokens"`
		Context    int    `json:"context_length"`
		Maximum    int    `json:"max_context_length"`
		Prompt     int    `json:"prompt_tokens"`
		Completion int    `json:"completion_tokens"`
		NCtx       int    `json:"n_ctx"`
		NPrompt    int    `json:"n_prompt_tokens"`
	}
	_ = json.Unmarshal(detail, &fields)
	message := strings.TrimSpace(string(raw))
	if fields.Message != "" {
		message = fields.Message
	}
	marker := strings.ToLower(fields.Code + " " + fields.Type + " " + message)
	typedContext := fields.Code == "context_length_exceeded" || fields.Type == "context_length_exceeded" || fields.Code == "exceed_context_size_error" || fields.Type == "exceed_context_size_error"
	canBeContext := (typedContext || status == 0 || status == 400 || status == 413 || status == 422) && !strings.Contains(marker, "rate_limit")
	for _, known := range []string{"context_length_exceeded", "exceed_context_size_error", "exceeds the available context size", "maximum context length", "maximum context size", "context window is too small", "too many tokens", "exceeds the context window", "context size exceeded"} {
		if canBeContext && strings.Contains(marker, known) {
			limit := max(fields.Limit, fields.Context, fields.Maximum, fields.NCtx, messageCount(limitCount, message))
			return &ContextOverflowError{Message: message, LimitTokens: limit, InputTokens: max(fields.Input, fields.Prompt, fields.NPrompt, messageCount(inputCount, message)), OutputTokens: max(fields.Output, fields.Completion)}
		}
	}
	if status != 0 {
		return fmt.Errorf("model HTTP %d: %s", status, message)
	}
	return errors.New(message)
}

func unsupportedStreamUsage(status int, message string) bool {
	if status != 400 && status != 422 {
		return false
	}
	message = strings.ToLower(message)
	option := strings.Contains(message, "stream_options") || strings.Contains(message, "include_usage")
	return option && (strings.Contains(message, "not supported") || strings.Contains(message, "unsupported") || strings.Contains(message, "unknown") || strings.Contains(message, "unrecognized") || strings.Contains(message, "extra inputs") || strings.Contains(message, "not permitted"))
}
