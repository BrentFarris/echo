package services

import (
	"errors"
	"strings"
	"testing"
)

func TestUserFacingLLMError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		notWant  string // raw substring that must NOT appear in user-facing message
		mustHave string // expected user-friendly text
	}{
		{
			name:     "nil returns empty",
			err:      nil,
			mustHave: "",
		},
		{
			name:     "connection refused gives friendly message",
			err:      errors.New("dial tcp 1.2.3.4:8080: connection refused"),
			mustHave: "Could not reach the LLM endpoint",
		},
		{
			name:     "context deadline exceeded gives friendly message",
			err:      errors.New("context deadline exceeded"),
			mustHave: "timed out",
		},
		{
			name:     "read stream unexpected EOF gives friendly message without raw",
			err:      errors.New("read stream: unexpected EOF"),
			notWant:  "unexpected EOF",
			mustHave: "closed the connection mid-response",
		},
		{
			name:     "read stream other error gives friendly message",
			err:      errors.New("read stream: i/o timeout"),
			mustHave: "closed the connection mid-response",
		},
		{
			name:     "unknown error returns raw string",
			err:      errors.New("some unknown error"),
			mustHave: "some unknown error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := userFacingLLMError(tt.err)
			if !strings.Contains(result, tt.mustHave) {
				t.Errorf("expected result to contain %q, got %q", tt.mustHave, result)
			}
			if tt.notWant != "" && strings.Contains(result, tt.notWant) {
				t.Errorf("expected result to NOT contain %q, but got: %q", tt.notWant, result)
			}
		})
	}
}

func TestFinishReasonError(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		hasCalls   bool
		wantNil    bool
		mustHave   string
	}{
		{reason: "stop", wantNil: true},
		{reason: "", wantNil: true},
		{reason: "tool_calls", hasCalls: true, wantNil: true},
		{reason: "tool_calls", hasCalls: false, wantNil: false, mustHave: "no tool call was received"},
		{reason: "length", wantNil: false, mustHave: "token limit"},
		{reason: "content_filter", wantNil: false, mustHave: "filtered"},
		{reason: "other", wantNil: false, mustHave: "finish_reason: other"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			err := finishReasonError(tt.reason, tt.hasCalls)
			if tt.wantNil {
				if err != nil {
					t.Errorf("expected nil error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if !strings.Contains(err.Error(), tt.mustHave) {
				t.Errorf("expected error to contain %q, got: %q", tt.mustHave, err.Error())
			}
		})
	}
}
