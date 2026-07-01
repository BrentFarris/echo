package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
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
