package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func ptr[T any](v T) *T { return &v }

func TestEndpointParametersAndMultimodalWire(t *testing.T) {
	var wire map[string]any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()
	e := domain.Endpoint{URL: provider.URL, Model: "vision", TopK: ptr(20), TopP: ptr(.95), MinP: ptr(0.0), MaxTokens: ptr(32168), FrequencyPenalty: ptr(0.0), PresencePenalty: ptr(1.5), RepetitionPenalty: ptr(1.05), ReasoningEffort: "high", ThinkingTokenBudget: ptr(200)}
	m := Message{Role: "user", Content: "Inspect this", Parts: []ContentPart{{Type: "image_url", ImageURL: &MediaURL{URL: "data:image/png;base64,abc"}}, {Type: "video_url", VideoURL: &MediaURL{URL: "data:video/mp4;base64,def"}}, {Type: "file", File: &FileData{Filename: "report.pdf", Data: "data:application/pdf;base64,ghi"}}}}
	_, err := New().Complete(context.Background(), e, domain.Generation{Temperature: .2}, []Message{m}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]any{"temperature": .2, "top_k": float64(20), "top_p": .95, "min_p": 0.0, "max_tokens": float64(32168), "frequency_penalty": 0.0, "presence_penalty": 1.5, "repetition_penalty": 1.05, "reasoning_effort": "high"} {
		if wire[key] != expected {
			t.Errorf("%s: got %v; want %v", key, wire[key], expected)
		}
	}
	if _, ok := wire["chat_template_kwargs"]; ok {
		t.Fatal("named effort did not suppress budget")
	}
	parts := wire["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 4 || parts[0].(map[string]any)["text"] != "Inspect this" || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,abc" {
		t.Fatalf("lost multimodal content: %#v", parts)
	}
	data, _ := json.Marshal(m)
	var round Message
	if err := json.Unmarshal(data, &round); err != nil || round.Content != m.Content || len(round.Parts) != 3 {
		t.Fatalf("round trip: %+v %v", round, err)
	}
	if strings.Contains(round.Summary().Content, "base64") {
		t.Fatal("binary payload leaked into text summary")
	}
	e.ReasoningEffort = ""
	body := generationParameters(e, domain.Generation{Temperature: 0})
	if body["chat_template_kwargs"].(map[string]any)["thinking_token_budget"] != 200 {
		t.Fatal(body)
	}
	body = generationParameters(domain.Endpoint{}, domain.Generation{})
	if _, exists := body["top_k"]; exists {
		t.Fatal("unset parameter was sent")
	}
}

func TestStreamIdleAndRequestTimeouts(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		for i := 0; i < 4; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
			if r.URL.Path != "/silent/chat/completions" {
				w.Write([]byte(": heartbeat\n\n"))
				w.(http.Flusher).Flush()
			}
		}
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()
	for _, test := range []struct {
		name, path  string
		idle, total int
		fails       bool
	}{
		{"heartbeat", "", 1, 0, false}, {"idle", "/silent", 1, 0, true}, {"disabled", "/silent", -1, 0, false}, {"total deadline", "", -1, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New().Complete(context.Background(), domain.Endpoint{URL: provider.URL + test.path, StreamIdleTimeoutSeconds: test.idle, RequestTimeoutSeconds: test.total}, domain.Generation{}, nil, nil, "", nil)
			if (err != nil) != test.fails {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestInvalidEndpointSettings(t *testing.T) {
	for _, e := range []domain.Endpoint{{TopK: ptr(-1)}, {TopP: ptr(1.01)}, {MinP: ptr(-.1)}, {MaxTokens: ptr(0)}, {CompressionThreshold: 100}, {StreamIdleTimeoutSeconds: -2}, {ReasoningEffort: "unknown"}, {ThinkingTokenBudget: ptr(-2)}} {
		if ValidateSettings(e) == nil {
			t.Fatalf("accepted invalid settings %+v", e)
		}
	}
}
