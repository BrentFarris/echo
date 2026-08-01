package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/flowlog"
)

func TestCompleteFlowLogCapturesExactPayloadWithoutConfiguredURLsOrHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"server-model","choices":[{"index":0,"message":{"role":"assistant","content":"answer","reasoning_content":"private-thought"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "echo.log")
	logger := flowlog.NewController()
	if err := logger.Enable(path); err != nil {
		t.Fatal(err)
	}
	settings := traceTestSettings(server.URL)
	client, err := NewClient(settings, WithFlowLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewChatRequest(settings, []Message{
		{Role: RoleSystem, Content: "exact-system"},
		{Role: RoleUser, Content: "exact-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := logger.Disable(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"llm_request", "trace-model", "exact-system", "exact-user", "llm_response", "private-thought", "answer"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("log missing %q: %s", expected, text)
		}
	}
	for _, forbidden := range []string{server.URL, "search.private.invalid", "header-secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("log unexpectedly contains configured private value %q: %s", forbidden, text)
		}
	}
}

func TestStreamFlowLogCapturesThinkingToolCallsAndSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"filesystem_stat\",\"arguments\":\"{\\\"path\\\":\\\"echo/main.go\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"total_tokens\":12}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "echo.log")
	logger := flowlog.NewController()
	if err := logger.Enable(path); err != nil {
		t.Fatal(err)
	}
	settings := traceTestSettings(server.URL)
	client, err := NewClient(settings, WithFlowLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "go"}}, WithStream(true))
	if err != nil {
		t.Fatal(err)
	}
	stream := client.StreamChat(context.Background(), request)
	for range stream.Events {
	}
	if err := logger.Disable(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"llm_stream_chunk", "thinking", "answer", "filesystem_stat", "call-1", "llm_response_summary", "total_tokens"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("stream log missing %q: %s", expected, text)
		}
	}
}

func traceTestSettings(endpoint string) Settings {
	settings := DefaultSettings()
	settings.Endpoint = endpoint
	settings.Model = "trace-model"
	settings.SearxngURL = "http://search.private.invalid"
	settings.Headers = map[string]string{"X-Secret": "header-secret"}
	return settings
}
