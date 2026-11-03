package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// RequestSizeError is a transport byte limit, not evidence of a token overflow.
// Keep the message independent of the response body: proxies often return HTML.
type RequestSizeError struct{}

func (*RequestSizeError) Error() string {
	return "Model request too large (HTTP 413). The model server or its proxy rejected the request's byte size. Large attachments, conversation history, or tool results can cause this even when the model has token space available. Use smaller attachments, reduce the included context or start a new chat, or ask the server administrator to increase the request-body size limit."
}

var htmlErrorPage = regexp.MustCompile(`(?i)<\s*(?:!doctype\s+html|/?(?:html|head|body|title|h[1-6]|div|p|br|pre|script|style|center))(?:\s|/?>)`)

func contextErrorCode(code string) bool {
	switch strings.ToLower(code) {
	case "context_length_exceeded", "exceed_context_size_error", "context_window_exceeded", "prompt_too_long", "input_too_long":
		return true
	}
	return false
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
	} else {
		// Some compatible endpoints use {"error":"message"} or a JSON string.
		var plain string
		if json.Unmarshal(detail, &plain) == nil && strings.TrimSpace(plain) != "" {
			message = plain
		}
	}
	marker := strings.ToLower(fields.Code + " " + fields.Type + " " + message)
	typedContext := contextErrorCode(fields.Code) || contextErrorCode(fields.Type)
	isHTML := htmlErrorPage.MatchString(message)
	rateLimited := status == http.StatusTooManyRequests
	for _, phrase := range []string{"rate_limit", "rate limit", "tokens per minute", "tokens per day"} {
		rateLimited = rateLimited || strings.Contains(marker, phrase)
	}
	byteLimited := strings.Contains(marker, "bytes") || strings.Contains(marker, "request body") || strings.Contains(marker, "request-body") || strings.Contains(marker, "payload size")
	canBeContext := (typedContext || status == 0 || status == 400 || status == 413 || status == 422) && !rateLimited && !isHTML && (typedContext || !byteLimited)
	isContext := typedContext
	for _, known := range []string{"context_length_exceeded", "exceed_context_size_error", "context_window_exceeded", "exceeds the available context size", "maximum context length", "maximum context size", "context window is too small", "too many tokens", "exceeds the context window", "context size exceeded", "prompt is too long", "prompt too long", "input is too long", "input too long"} {
		isContext = isContext || strings.Contains(marker, known)
	}
	if canBeContext && isContext {
		limit := max(fields.Limit, fields.Context, fields.Maximum, fields.NCtx, messageCount(limitCount, message))
		return &ContextOverflowError{Message: message, LimitTokens: limit, InputTokens: max(fields.Input, fields.Prompt, fields.NPrompt, messageCount(inputCount, message)), OutputTokens: max(fields.Output, fields.Completion)}
	}
	if status == http.StatusRequestEntityTooLarge {
		return &RequestSizeError{}
	}
	if isHTML {
		if status != 0 {
			return fmt.Errorf("The model server or its proxy returned an HTML error page (HTTP %d: %s). Check the endpoint URL and server/proxy logs.", status, http.StatusText(status))
		}
		return errors.New("The model endpoint returned an HTML error page. Check the endpoint URL and server/proxy logs.")
	}
	if status != 0 {
		if message == "" {
			message = http.StatusText(status)
		}
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
