package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewChatRequestMapsSettings(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	settings.Model = "test-model"
	settings.Temperature = 0
	settings.TopK = 12
	settings.TopP = 0.75
	settings.MinP = 0.05
	settings.ContextLength = 8192
	settings.MaxTokens = 256
	settings.FrequencyPenalty = 0.25
	settings.PresencePenalty = 0.5
	settings.RepetitionPenalty = 1.1

	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}}, WithStream(true))
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}

	if request.Model != "test-model" {
		t.Fatalf("expected model mapping, got %q", request.Model)
	}
	if !request.Stream {
		t.Fatal("expected stream to be enabled")
	}
	if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
		t.Fatal("expected streaming usage to be requested")
	}
	assertFloatPtr(t, "temperature", request.Temperature, 0)
	assertIntPtr(t, "top_k", request.TopK, 12)
	assertFloatPtr(t, "top_p", request.TopP, 0.75)
	assertFloatPtr(t, "min_p", request.MinP, 0.05)
	assertIntPtr(t, "context_length", request.ContextLength, 8192)
	assertIntPtr(t, "max_tokens", request.MaxTokens, 256)
	assertFloatPtr(t, "frequency_penalty", request.FrequencyPenalty, 0.25)
	assertFloatPtr(t, "presence_penalty", request.PresencePenalty, 0.5)
	assertFloatPtr(t, "repetition_penalty", request.RepetitionPenalty, 1.1)
	if request.ChatTemplateKwargs == nil {
		t.Fatal("expected default thinking mode to include chat template kwargs")
	}
	assertIntPtr(t, "thinking_token_budget", request.ChatTemplateKwargs.ThinkingTokenBudget, -1)
	if request.ChatTemplateKwargs.EnableThinking != nil {
		t.Fatalf("expected default enable_thinking to be omitted, got %#v", request.ChatTemplateKwargs)
	}
}

func TestCompleteDoesNotLogGeneratedContextSummary(t *testing.T) {
	const summary = "PRIVATE GENERATED CONTEXT SUMMARY"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, summary)
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	settings.Endpoints[0].Endpoint = settings.Endpoint
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := NewClient(settings, WithLogger(logger))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{
		{Role: RoleSystem, Name: "echo-context-summary", Content: "summarize"},
		{Role: RoleUser, Content: "history"},
	})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Complete(context.Background(), request); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if strings.Contains(logs.String(), summary) || !strings.Contains(logs.String(), "payload_bytes") || !strings.Contains(logs.String(), "TotalTokens:15") {
		t.Fatalf("compression response logging was not metrics-only: %s", logs.String())
	}
}

func TestCompleteFallsBackToStreamingForContextSummaryTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			if payload.Stream {
				t.Error("first completion attempt must be non-streaming")
			}
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"error":"upstream timed out"}`)
		default:
			if !payload.Stream {
				t.Error("fallback completion attempt must be streaming")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"role":"assistant","content":"## Goal\n"}}]}` + "\n\n")
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Continue the build."},"finish_reason":null}]}` + "\n\n")
			_, _ = fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}` + "\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	settings.Endpoints[0].Endpoint = settings.Endpoint
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{
		{Role: RoleSystem, Name: "echo-context-summary", Content: "summarize"},
		{Role: RoleUser, Content: "history"},
	})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	response, err := client.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("complete with streaming fallback: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one non-streaming attempt plus one streamed retry, got %d calls", calls.Load())
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "## Goal\nContinue the build." {
		t.Fatalf("streamed fallback did not assemble the summary: %#v", response)
	}
	if response.Usage == nil || response.Usage.TotalTokens != 14 {
		t.Fatalf("streamed fallback lost usage: %#v", response.Usage)
	}
}

func TestCompleteFallsBackToStreamingForEmptyCompressionContent(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			if payload.Stream {
				t.Error("first completion attempt must be non-streaming")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
		default:
			if !payload.Stream {
				t.Error("fallback completion attempt must be streaming")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"## Goal\nFix the build."}}]}` + "\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	settings.Endpoints[0].Endpoint = settings.Endpoint
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{
		{Role: RoleSystem, Name: "echo-context-summary", Content: "summarize"},
		{Role: RoleUser, Content: "history"},
	})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	response, err := client.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("complete with empty-content fallback: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one non-streaming attempt plus one streamed retry, got %d calls", calls.Load())
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "## Goal\nFix the build." {
		t.Fatalf("streamed fallback did not assemble the summary: %#v", response)
	}
}

func TestCompleteDoesNotRetryNonCompressionOrCanceledRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, `{"error":"upstream timed out"}`)
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	settings.Endpoints[0].Endpoint = settings.Endpoint
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	plainRequest, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Complete(context.Background(), plainRequest); err == nil {
		t.Fatal("expected the non-streaming failure to surface")
	}
	if calls.Load() != 1 {
		t.Fatalf("non-compression request must not be retried: %d calls", calls.Load())
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	compressionRequest, err := NewChatRequest(settings, []Message{
		{Role: RoleSystem, Name: "echo-context-summary", Content: "summarize"},
		{Role: RoleUser, Content: "history"},
	})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Complete(cancelled, compressionRequest); err == nil {
		t.Fatal("expected the canceled request to fail")
	}
	if calls.Load() != 1 {
		t.Fatalf("canceled compression request must not be retried: %d calls", calls.Load())
	}
}

func TestIsRetryableCompletionError(t *testing.T) {
	for _, message := range []string{
		`send chat request: Post "http://10.0.0.11:30000/v1/chat/completions": context deadline exceeded`,
		"read chat response: EOF",
		"llm endpoint returned 502 Bad Gateway: upstream timeout",
	} {
		if !isRetryableCompletionError(fmt.Errorf("%s", message)) {
			t.Fatalf("expected completion error to be retryable: %s", message)
		}
	}
	for _, message := range []string{
		"operation canceled",
		`generate context summary: endpoint returned an empty summary`,
		`llm endpoint returned 401 Unauthorized: invalid api key`,
	} {
		if isRetryableCompletionError(fmt.Errorf("%s", message)) {
			t.Fatalf("expected completion error to be non-retryable: %s", message)
		}
	}
}

func TestStreamChatCachesUnsupportedUsageOption(t *testing.T) {
	var calls atomic.Int32
	usageOptions := make(chan bool, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			StreamOptions *StreamOptions `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		usageOptions <- payload.StreamOptions != nil && payload.StreamOptions.IncludeUsage
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"stream_options.include_usage is not supported"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	settings.Endpoints[0].Endpoint = settings.Endpoint
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}}, WithStream(true))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for range client.StreamChat(context.Background(), request).Events {
	}
	for range client.StreamChat(context.Background(), request).Events {
	}

	got := []bool{<-usageOptions, <-usageOptions, <-usageOptions}
	want := []bool{true, false, false}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("request %d include_usage = %v, want %v (all: %v)", index+1, got[index], want[index], got)
		}
	}
	if !client.streamUsageUnsupported.Load() {
		t.Fatal("expected unsupported stream usage capability to be cached")
	}
}

func TestIsContextLengthExceeded(t *testing.T) {
	for _, message := range []string{
		`llm endpoint returned 400 Bad Request: {"error":{"type":"exceed_context_size_error"}}`,
		`context_length_exceeded: maximum context length is 128000 tokens`,
		`request exceeds the available context size`,
		`too many tokens in prompt`,
	} {
		if !IsContextLengthExceeded(fmt.Errorf("%s", message)) {
			t.Fatalf("expected context error to be recognized: %s", message)
		}
	}
	if IsContextLengthExceeded(fmt.Errorf("llm endpoint returned 500 Internal Server Error")) {
		t.Fatal("expected unrelated endpoint error not to be recognized")
	}
}

func TestResponseError413ReturnsUserFriendlyMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte("nginx 413 error page html content"))
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	_, err = client.Complete(context.Background(), request)
	if err == nil {
		t.Fatal("expected error for 413 response")
	}

	msg := err.Error()
	if !strings.Contains(msg, "413") {
		t.Fatalf("expected error to mention 413, got: %s", msg)
	}
	if !strings.Contains(msg, "Request Entity Too Large") {
		t.Fatalf("expected user-friendly message, got: %s", msg)
	}
	if strings.Contains(msg, "nginx") {
		t.Fatalf("expected error to hide raw response body, got: %s", msg)
	}
}

func TestNewChatRequestAddsThinkingCorrectionToLatestUserMessage(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	settings.ThinkingCorrection = true
	messages := []Message{
		{Role: RoleSystem, Content: "be helpful"},
		{Role: RoleUser, Content: "first task"},
		{Role: RoleAssistant, Content: "done"},
		{Role: RoleUser, Content: "second task"},
	}

	request, err := NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}

	if request.Messages[1].Content != "first task" {
		t.Fatalf("expected only latest user message to change, got %#v", request.Messages)
	}
	if !strings.HasPrefix(request.Messages[3].Content, "second task\n\n") ||
		!strings.Contains(request.Messages[3].Content, ThinkingCorrectionText) {
		t.Fatalf("expected thinking correction on latest user message, got %q", request.Messages[3].Content)
	}
	if messages[3].Content != "second task" {
		t.Fatalf("expected source messages to remain unchanged, got %#v", messages)
	}
}

func TestNewChatRequestAppendsModelInstructionsToSystemPrompt(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	settings.SystemPromptAppendage = "  Prefer concise answers.\nUse metric units.  "
	messages := []Message{
		{Role: RoleSystem, Content: "Be helpful."},
		{Role: RoleUser, Content: "Hello"},
	}

	request, err := NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}

	if got, want := request.Messages[0].Content, "Be helpful.\n\nPrefer concise answers.\nUse metric units."; got != want {
		t.Fatalf("expected system prompt appendage %q, got %q", want, got)
	}
	if messages[0].Content != "Be helpful." {
		t.Fatalf("expected source system prompt to remain unchanged, got %q", messages[0].Content)
	}
}

func TestNewChatRequestMergesLeadingSystemMessages(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	messages := []Message{
		{Role: RoleSystem, Name: "echo-agent-mode", Content: "Be helpful."},
		{Role: RoleSystem, Name: "echo-code-context", Content: "Selected code: answer := 42"},
		{Role: RoleUser, Content: "Explain this selection."},
	}

	request, err := NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}

	if len(request.Messages) != 2 {
		t.Fatalf("expected one system message and one user message, got %#v", request.Messages)
	}
	if request.Messages[0].Role != RoleSystem || request.Messages[0].Name != "echo-agent-mode" {
		t.Fatalf("expected the first system message to remain the request prefix, got %#v", request.Messages[0])
	}
	if got, want := request.Messages[0].Content, "Be helpful.\n\nSelected code: answer := 42"; got != want {
		t.Fatalf("expected merged system content %q, got %q", want, got)
	}
	if request.Messages[1].Role != RoleUser || request.Messages[1].Content != "Explain this selection." {
		t.Fatalf("expected user message after the merged system prefix, got %#v", request.Messages[1])
	}
	if messages[0].Content != "Be helpful." || messages[1].Content != "Selected code: answer := 42" {
		t.Fatalf("expected source messages to remain unchanged, got %#v", messages)
	}
}

func TestNewChatRequestAddsThinkingCorrectionAsFinalContentPart(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	settings.ThinkingCorrection = true
	imageURL := ImageURLContentPart("data:image/png;base64,abc123")
	messages := []Message{{
		Role:    RoleUser,
		Content: "Review this image.",
		ContentParts: []MessageContentPart{
			TextContentPart("Review this image."),
			imageURL,
		},
	}}

	request, err := NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}

	parts := request.Messages[0].ContentParts
	if len(parts) != 3 {
		t.Fatalf("expected correction content part, got %#v", parts)
	}
	if parts[2].Type != "text" || parts[2].Text != ThinkingCorrectionText {
		t.Fatalf("expected final correction text part, got %#v", parts[2])
	}
	if len(messages[0].ContentParts) != 2 {
		t.Fatalf("expected source content parts to remain unchanged, got %#v", messages[0].ContentParts)
	}
	if request.Messages[0].ContentParts[1].ImageURL == messages[0].ContentParts[1].ImageURL {
		t.Fatal("expected image URL content part to be deep-copied")
	}
}

func TestNewChatRequestDisablesThinkingAndThinkingCorrectionForZeroBudget(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	settings.ThinkingCorrection = true
	settings.ThinkingTokenBudget = 0

	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}

	if request.ChatTemplateKwargs == nil || request.ChatTemplateKwargs.EnableThinking == nil {
		t.Fatalf("expected chat template kwargs to disable thinking, got %#v", request.ChatTemplateKwargs)
	}
	if *request.ChatTemplateKwargs.EnableThinking {
		t.Fatalf("expected enable_thinking false, got %#v", request.ChatTemplateKwargs)
	}
	assertIntPtr(t, "thinking_token_budget", request.ChatTemplateKwargs.ThinkingTokenBudget, 0)
	if strings.Contains(request.Messages[0].Content, ThinkingCorrectionText) {
		t.Fatalf("expected disabled thinking to skip correction text, got %q", request.Messages[0].Content)
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	kwargs, ok := decoded["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("expected serialized enable_thinking false, got %#v", decoded["chat_template_kwargs"])
	}
	if kwargs["thinking_token_budget"] != float64(0) {
		t.Fatalf("expected serialized thinking_token_budget 0, got %#v", kwargs)
	}
}

func TestNewChatRequestAddsThinkingTokenBudget(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	settings.ThinkingTokenBudget = 4096

	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}

	if request.ChatTemplateKwargs == nil || request.ChatTemplateKwargs.ThinkingTokenBudget == nil {
		t.Fatalf("expected thinking token budget kwargs, got %#v", request.ChatTemplateKwargs)
	}
	if *request.ChatTemplateKwargs.ThinkingTokenBudget != 4096 {
		t.Fatalf("expected thinking token budget 4096, got %#v", request.ChatTemplateKwargs)
	}
	if request.ChatTemplateKwargs.EnableThinking != nil {
		t.Fatalf("expected default enable_thinking to be omitted, got %#v", request.ChatTemplateKwargs)
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	kwargs, ok := decoded["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["thinking_token_budget"] != float64(4096) {
		t.Fatalf("expected serialized thinking_token_budget 4096, got %#v", decoded["chat_template_kwargs"])
	}
	if _, ok := kwargs["enable_thinking"]; ok {
		t.Fatalf("expected default enable_thinking to be omitted, got %#v", kwargs)
	}
}

func TestNewChatRequestAddsReasoningEffortWithoutTemplateKwargs(t *testing.T) {
	for _, effort := range []string{ReasoningEffortMax, ReasoningEffortXHigh, ReasoningEffortNone} {
		t.Run(effort, func(t *testing.T) {
			settings := DefaultSettings()
			settings.Endpoint = "https://example.test/v1"
			settings.ReasoningEffort = effort
			settings.ThinkingTokenBudget = 0
			settings.ThinkingCorrection = true

			request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
			if err != nil {
				t.Fatalf("new chat request: %v", err)
			}

			if request.ReasoningEffort != effort {
				t.Fatalf("expected reasoning effort %q, got %q", effort, request.ReasoningEffort)
			}
			if request.ChatTemplateKwargs != nil {
				t.Fatalf("expected reasoning effort to omit chat template kwargs, got %#v", request.ChatTemplateKwargs)
			}
			hasCorrection := strings.Contains(request.Messages[0].Content, ThinkingCorrectionText)
			if wantCorrection := effort != ReasoningEffortNone; hasCorrection != wantCorrection {
				t.Fatalf("thinking correction presence = %v, want %v for %q", hasCorrection, wantCorrection, effort)
			}

			data, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if decoded["reasoning_effort"] != effort {
				t.Fatalf("expected serialized reasoning effort %q, got %#v", effort, decoded)
			}
			if _, exists := decoded["chat_template_kwargs"]; exists {
				t.Fatalf("expected chat_template_kwargs to be omitted, got %#v", decoded)
			}
		})
	}
}

func TestChatRequestSerialization(t *testing.T) {
	request := ChatRequest{
		Model: "model-a",
		Messages: []Message{
			{Role: RoleSystem, Content: "be helpful"},
			{Role: RoleUser, Content: "list files"},
		},
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_files",
				Description: "List files in a directory",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			},
		}},
		ToolChoice: "auto",
		Stream:     true,
		MaxTokens:  intPtr(99),
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decoded["model"] != "model-a" {
		t.Fatalf("expected serialized model, got %v", decoded["model"])
	}
	if decoded["stream"] != true {
		t.Fatalf("expected serialized stream flag, got %v", decoded["stream"])
	}
	if decoded["tool_choice"] != "auto" {
		t.Fatalf("expected serialized tool choice, got %v", decoded["tool_choice"])
	}
	if decoded["max_tokens"] != float64(99) {
		t.Fatalf("expected serialized max tokens, got %v", decoded["max_tokens"])
	}
	if _, ok := decoded["tools"].([]any); !ok {
		t.Fatalf("expected tools array, got %#v", decoded["tools"])
	}
}

func TestMessageSerializesStringContent(t *testing.T) {
	data, err := json.Marshal(Message{Role: RoleUser, Content: "hello"})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if decoded["content"] != "hello" {
		t.Fatalf("expected string content, got %#v", decoded["content"])
	}
}

func TestMessageSerializesExplicitEmptyAssistantContent(t *testing.T) {
	data, err := json.Marshal(Message{Role: RoleAssistant})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	content, ok := decoded["content"]
	if !ok || content != "" {
		t.Fatalf("expected explicit empty assistant content, got %s", data)
	}
}

func TestNewChatRequestRemovesEmptyAssistantHistory(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	messages := []Message{
		{Role: RoleSystem, Content: "be helpful"},
		{Role: RoleUser, Content: "first task"},
		{Role: RoleAssistant},
		{Role: RoleUser, Content: "continue"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: FunctionCall{
				Name:      "read_file",
				Arguments: `{}`,
			},
		}}},
	}

	request, err := NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}
	if len(request.Messages) != len(messages)-1 {
		t.Fatalf("expected only empty assistant history to be removed, got %#v", request.Messages)
	}
	for _, message := range request.Messages {
		if message.Role == RoleAssistant && strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
			t.Fatalf("found empty assistant message in request: %#v", request.Messages)
		}
	}
	if len(request.Messages[len(request.Messages)-1].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call to be preserved, got %#v", request.Messages)
	}
	if messages[2].Role != RoleAssistant {
		t.Fatalf("expected source history to remain unchanged, got %#v", messages)
	}
}

func TestNewChatRequestRepairsMalformedHistoricalToolArguments(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	messages := []Message{
		{Role: RoleUser, Content: "inspect the file"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID: "call-1", Type: "function",
			Function: FunctionCall{Name: "read_file", Arguments: `{"path":"internal/server/chat_sessions.go`},
		}}},
		{Role: RoleTool, ToolCallID: "call-1", Content: `{"success":true}`},
		{Role: RoleUser, Content: "continue"},
	}

	request, err := NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}
	got := request.Messages[1].ToolCalls[0].Function.Arguments
	if got != `{"path":"internal/server/chat_sessions.go"}` {
		t.Fatalf("expected repaired tool arguments, got %q", got)
	}
	if messages[1].ToolCalls[0].Function.Arguments == got {
		t.Fatal("expected source history to remain unchanged")
	}
}

func TestNewChatRequestReplacesIrreparableHistoricalToolArguments(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	messages := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{
		ID: "call-1", Type: "function",
		Function: FunctionCall{Name: "read_file", Arguments: `not json`},
	}}}}

	request, err := NewChatRequest(settings, messages)
	if err != nil {
		t.Fatalf("new chat request: %v", err)
	}
	if got := request.Messages[0].ToolCalls[0].Function.Arguments; got != `{}` {
		t.Fatalf("expected safe empty arguments, got %q", got)
	}
}

func TestMessageSerializesTextAndImageContentParts(t *testing.T) {
	message := Message{
		Role:    RoleUser,
		Content: "Review this image.",
		ContentParts: []MessageContentPart{
			TextContentPart("Review this image."),
			ImageURLContentPart("data:image/png;base64,abc123"),
		},
	}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	parts, ok := decoded["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected content parts array, got %#v", decoded["content"])
	}
	textPart := parts[0].(map[string]any)
	if textPart["type"] != "text" || textPart["text"] != "Review this image." {
		t.Fatalf("unexpected text part: %#v", textPart)
	}
	imagePart := parts[1].(map[string]any)
	imageURL := imagePart["image_url"].(map[string]any)
	if imagePart["type"] != "image_url" || imageURL["url"] != "data:image/png;base64,abc123" {
		t.Fatalf("unexpected image part: %#v", imagePart)
	}
	if _, ok := imageURL["detail"]; ok {
		t.Fatalf("expected image detail to be omitted, got %#v", imageURL)
	}

	var roundTrip Message
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if roundTrip.Content != "Review this image." || len(roundTrip.ContentParts) != 2 {
		t.Fatalf("unexpected round-trip message: %#v", roundTrip)
	}
}

func TestCompleteUsesOpenAICompatibleRequestShape(t *testing.T) {
	var captured ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected chat completions path, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("expected JSON accept header, got %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("X-Client-Request-Id") == "" {
			t.Fatal("expected a client request ID")
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(ChatResponse{
			ID:    "chatcmpl-test",
			Model: captured.Model,
			Choices: []ChatChoice{{
				Index:   0,
				Message: Message{Role: RoleAssistant, Content: "done"},
			}},
		})
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = strings.TrimRight(server.URL, "/") + "/v1"
	settings.Model = "shape-model"

	client, err := NewClient(settings, WithAPIKey("secret"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := client.Complete(context.Background(), request)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if captured.Model != "shape-model" {
		t.Fatalf("expected model in request, got %q", captured.Model)
	}
	if captured.Stream {
		t.Fatal("expected non-streaming request")
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Content != "hello" {
		t.Fatalf("unexpected messages: %#v", captured.Messages)
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "done" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestChatCompletionsURLAcceptsFullPath(t *testing.T) {
	endpoint := "https://example.test/v1/chat/completions/"
	if got := chatCompletionsURL(endpoint); got != strings.TrimRight(endpoint, "/") {
		t.Fatalf("expected full path to be preserved, got %q", got)
	}
}

func TestClientRequestIDsAreUniqueAndCustomizable(t *testing.T) {
	settings := DefaultSettings()
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	first, _ := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
	second, _ := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
	firstID := client.applyHeaders(first)
	secondID := client.applyHeaders(second)
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("expected unique client request IDs, got %q and %q", firstID, secondID)
	}

	settings.Headers = map[string]string{"X-Client-Request-Id": "caller-owned-id"}
	client, err = NewClient(settings)
	if err != nil {
		t.Fatalf("new custom-header client: %v", err)
	}
	custom, _ := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
	if got := client.applyHeaders(custom); got != "caller-owned-id" {
		t.Fatalf("expected caller-provided request ID, got %q", got)
	}
}

func TestConversationMessagesAreInMemoryAndCopied(t *testing.T) {
	settings := DefaultSettings()
	settings.Endpoint = "https://example.test/v1"
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	messages := []Message{{Role: RoleUser, Content: "original"}}
	client.SetConversationMessages("workspace-1", messages)
	messages[0].Content = "mutated"

	stored := client.ConversationMessages("workspace-1")
	if len(stored) != 1 || stored[0].Content != "original" {
		t.Fatalf("expected stored copy, got %#v", stored)
	}

	stored[0].Content = "mutated again"
	stored = client.ConversationMessages("workspace-1")
	if stored[0].Content != "original" {
		t.Fatalf("expected returned copy, got %#v", stored)
	}

	client.ClearConversation("workspace-1")
	if stored := client.ConversationMessages("workspace-1"); len(stored) != 0 {
		t.Fatalf("expected conversation to clear, got %#v", stored)
	}
}

func TestStreamChatCancellationReleasesActiveStream(t *testing.T) {
	requestSeen := make(chan struct{})
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("expected SSE accept header, got %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("X-Client-Request-Id") == "" {
			t.Error("expected a client request ID")
		}
		if path.Clean(r.URL.Path) != "/v1/chat/completions" {
			t.Errorf("unexpected stream path: %s", r.URL.Path)
		}
		close(requestSeen)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(requestCanceled)
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	stream := client.StreamChat(context.Background(), request)
	<-requestSeen
	event := nextEvent(t, stream.Events)
	if event.Type != EventToken || event.Content != "first" {
		t.Fatalf("expected first token, got %#v", event)
	}

	if !client.Cancel(stream.ID) {
		t.Fatal("expected active stream to cancel")
	}
	event = nextEvent(t, stream.Events)
	if event.Type != EventCanceled {
		t.Fatalf("expected canceled event, got %#v", event)
	}

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe request cancellation")
	}

	for range stream.Events {
	}
	if count := client.ActiveStreamCount(); count != 0 {
		t.Fatalf("expected active stream to be released, got %d", count)
	}
}

func TestStreamChatDoesNotUseTotalRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(1100 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	settings.TimeoutSeconds = 1
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	stream := client.StreamChat(context.Background(), request)
	first := nextEvent(t, stream.Events)
	if first.Type != EventToken || first.Content != "first" {
		t.Fatalf("expected first token, got %#v", first)
	}
	second := nextEvent(t, stream.Events)
	if second.Type != EventToken || second.Content != "second" {
		t.Fatalf("expected second token after timeout seconds elapsed, got %#v", second)
	}
	complete := nextEvent(t, stream.Events)
	if complete.Type != EventComplete {
		t.Fatalf("expected complete event, got %#v", complete)
	}
}

func TestStreamChatReportsIdleProvider(t *testing.T) {
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"thinking\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(requestCanceled)
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.Endpoint = server.URL + "/v1"
	settings.StreamIdleTimeoutSeconds = 1
	client, err := NewClient(settings)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	request, err := NewChatRequest(settings, []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	stream := client.StreamChat(context.Background(), request)
	reasoning := nextEvent(t, stream.Events)
	if reasoning.Type != EventReasoning || reasoning.Content != "thinking" {
		t.Fatalf("expected initial reasoning, got %#v", reasoning)
	}
	idle := nextEvent(t, stream.Events)
	if idle.Type != EventError || !strings.Contains(idle.Error, "idle for 1s") {
		t.Fatalf("expected idle timeout error, got %#v", idle)
	}

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not cancel the provider request")
	}
}

func assertFloatPtr(t *testing.T, name string, actual *float64, expected float64) {
	t.Helper()
	if actual == nil {
		t.Fatalf("expected %s to be set", name)
	}
	if *actual != expected {
		t.Fatalf("expected %s %v, got %v", name, expected, *actual)
	}
}

func assertIntPtr(t *testing.T, name string, actual *int, expected int) {
	t.Helper()
	if actual == nil {
		t.Fatalf("expected %s to be set", name)
	}
	if *actual != expected {
		t.Fatalf("expected %s %v, got %v", name, expected, *actual)
	}
}

func nextEvent(t *testing.T, events <-chan StreamEvent) StreamEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event channel closed")
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return StreamEvent{}
	}
}
