package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/brent/echo/internal/llm"
)

func TestEstimateContextRequestTokensUsesFixedImageCost(t *testing.T) {
	base := llm.Message{
		Role: llm.RoleUser,
		ContentParts: []llm.MessageContentPart{
			llm.TextContentPart("inspect this image"),
			llm.ImageURLContentPart("data:image/png;base64,a"),
		},
	}
	large := cloneLLMMessages([]llm.Message{base})[0]
	large.ContentParts[1].ImageURL.URL = "data:image/png;base64," + strings.Repeat("a", 2_000_000)

	baseEstimate := estimateContextRequestTokens([]llm.Message{base}, nil)
	largeEstimate := estimateContextRequestTokens([]llm.Message{large}, nil)
	if baseEstimate != largeEstimate {
		t.Fatalf("expected fixed image token estimate, got %d and %d", baseEstimate, largeEstimate)
	}
	if baseEstimate < contextCompactionImageTokens {
		t.Fatalf("expected image token allowance, got %d", baseEstimate)
	}
}

func TestContextTailNeverSplitsToolCallGroup(t *testing.T) {
	call := llm.ToolCall{ID: "call-latest", Type: "function", Function: llm.FunctionCall{Name: "filesystem_read_image"}}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "original"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("old ", 1000)},
		{Role: llm.RoleUser, Content: "current"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}},
		{Role: llm.RoleTool, ToolCallID: call.ID, Content: `{"tool":"filesystem_read_image","success":true}`},
		{
			Role:    llm.RoleUser,
			Content: "Image returned by tool filesystem_read_image: image.png (image/png, 1 KB).",
			ContentParts: []llm.MessageContentPart{
				llm.TextContentPart("Image returned by tool filesystem_read_image: image.png (image/png, 1 KB)."),
				llm.ImageURLContentPart("data:image/png;base64,abc"),
			},
		},
	}

	segments := contextSegments(messages, 2)
	tailStart := chooseContextTailStart(messages, segments, 4096, 2)
	if tailStart != 4 {
		t.Fatalf("expected latest tool group to start at assistant message 4, got %d", tailStart)
	}
}

func TestForcedCompactionCanTightenRecentTail(t *testing.T) {
	settings := contextCompactionTestSettings("http://127.0.0.1:1", 10_512, 512)
	current := llm.Message{Role: llm.RoleUser, Content: "current"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "original"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("older ", 300)},
		current,
		{Role: llm.RoleAssistant, Content: strings.Repeat("recent ", 120)},
	}
	if contextHasCompressibleStale(settings, messages, contextCompactionPolicy{CurrentUser: current, Aggressiveness: 1}) {
		t.Fatal("expected the first forced tail to retain all messages")
	}
	if !contextHasCompressibleStale(settings, messages, contextCompactionPolicy{CurrentUser: current, Aggressiveness: 2}) {
		t.Fatal("expected the second forced pass to tighten the tail")
	}
}

func TestCompactContextPreservesHeadCurrentUserAndRecentTail(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Stream {
			t.Fatal("expected non-streaming checkpoint request")
		}
		if len(request.Tools) != 0 {
			t.Fatalf("expected checkpoint request without tools, got %#v", request.Tools)
		}
		_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"## Goal and Constraints\n- Keep the task.\n## Current State\n- Recent work retained.\n## Completed Checklist\n- [x] Inspected old output.\n## Remaining Checklist\n- [ ] Finish.\n## Decisions and Rejected Approaches\n- None.\n## Relevant Files and Commands\n- old.go\n## Findings, Errors, and Verification\n- None.\n## Immediate Next Action\n- Continue."},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	settings := contextCompactionTestSettings(server.URL, 4096, 512)
	client, err := llm.NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	current := llm.Message{Role: llm.RoleUser, Content: "current request"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{
			Role:    llm.RoleUser,
			Content: "original request",
			ContentParts: []llm.MessageContentPart{
				llm.TextContentPart("original request"),
				llm.ImageURLContentPart("data:image/png;base64,original"),
			},
		},
		{Role: llm.RoleAssistant, Content: contextCheckpointStart + "\nold checkpoint\n" + contextCheckpointEnd},
		{Role: llm.RoleAssistant, Content: strings.Repeat("stale details ", 800)},
		current,
		{Role: llm.RoleAssistant, Content: "recent assistant state"},
	}

	result, err := compactContextIfNeeded(context.Background(), client, settings, messages, nil, contextCompactionPolicy{
		CurrentUser:    current,
		Force:          true,
		Aggressiveness: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.UsedFallback {
		t.Fatalf("expected AI compaction, got %#v", result)
	}
	if result.AfterTokens >= result.BeforeTokens {
		t.Fatalf("expected context reduction, got before=%d after=%d", result.BeforeTokens, result.AfterTokens)
	}
	if result.Messages[0].Content != "system" || result.Messages[1].Content != "original request" {
		t.Fatalf("expected exact protected head, got %#v", result.Messages[:2])
	}
	if len(result.Messages[1].ContentParts) != 2 ||
		result.Messages[1].ContentParts[1].ImageURL == nil ||
		result.Messages[1].ContentParts[1].ImageURL.URL != "data:image/png;base64,original" {
		t.Fatalf("expected original attachment to remain verbatim, got %#v", result.Messages[1].ContentParts)
	}
	if !requestContainsContent(llm.ChatRequest{Messages: result.Messages}, "current request") ||
		!requestContainsContent(llm.ChatRequest{Messages: result.Messages}, "recent assistant state") {
		t.Fatalf("expected current user and recent tail, got %#v", result.Messages)
	}
	checkpoints := 0
	for _, message := range result.Messages {
		if isContextCheckpointMessage(message) {
			checkpoints++
		}
	}
	if checkpoints != 1 {
		t.Fatalf("expected one replacement checkpoint, got %d in %#v", checkpoints, result.Messages)
	}
	if requests.Load() < 1 {
		t.Fatal("expected at least one checkpoint request")
	}
}

func TestCompactContextFallsBackAfterSummaryFailures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "summary unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	settings := contextCompactionTestSettings(server.URL, 4096, 512)
	client, err := llm.NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	current := llm.Message{Role: llm.RoleUser, Content: "current"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "original"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("stale assistant result ", 700)},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID: "call-1", Type: "function",
			Function: llm.FunctionCall{Name: "filesystem_stat", Arguments: `{"path":"echo/main.go"}`},
		}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: `{"tool":"filesystem_stat","success":true,"output":{"path":"echo/main.go"}}`},
		current,
		{Role: llm.RoleAssistant, Content: "recent"},
	}

	result, err := compactContextIfNeeded(context.Background(), client, settings, messages, nil, contextCompactionPolicy{
		CurrentUser:    current,
		Force:          true,
		Aggressiveness: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || !result.UsedFallback || result.Warning == "" {
		t.Fatalf("expected bounded fallback compaction, got %#v", result)
	}
	if requests.Load() != 2 {
		t.Fatalf("expected normal and smaller summary attempts, got %d", requests.Load())
	}
	if !requestContainsContent(llm.ChatRequest{Messages: result.Messages}, "filesystem_stat") {
		t.Fatalf("expected deterministic tool checkpoint, got %#v", result.Messages)
	}
}

func TestChunkContextTranscriptHonorsBound(t *testing.T) {
	chunks := chunkContextTranscript([]string{
		strings.Repeat("a", 700),
		strings.Repeat("b", 700),
		strings.Repeat("c", 700),
	}, 1000)
	if len(chunks) != 3 {
		t.Fatalf("expected three bounded chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > 1000 {
			t.Fatalf("chunk exceeded bound: %d", len(chunk))
		}
	}
}

func TestTruncateContextTextPreservesUTF8(t *testing.T) {
	truncated := truncateContextText(strings.Repeat("🙂context", 100), 103)
	if !utf8.ValidString(truncated) {
		t.Fatalf("expected valid UTF-8, got %q", truncated)
	}
}

func TestCompactContextHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("canceled compaction should not reach the endpoint")
	}))
	defer server.Close()

	settings := contextCompactionTestSettings(server.URL, 4096, 512)
	client, err := llm.NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	current := llm.Message{Role: llm.RoleUser, Content: "current"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "original"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("stale ", 2000)},
		current,
		{Role: llm.RoleAssistant, Content: "recent"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = compactContextIfNeeded(ctx, client, settings, messages, nil, contextCompactionPolicy{
		CurrentUser:    current,
		Force:          true,
		Aggressiveness: 2,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestCompactContextRejectsIrreducibleProtectedHistory(t *testing.T) {
	settings := contextCompactionTestSettings("http://127.0.0.1:1", 4096, 512)
	client, err := llm.NewClient(settings)
	if err != nil {
		t.Fatal(err)
	}
	current := llm.Message{Role: llm.RoleUser, Content: "original"}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		current,
		{Role: llm.RoleAssistant, Content: "latest state"},
	}
	_, err = compactContextIfNeeded(context.Background(), client, settings, messages, nil, contextCompactionPolicy{
		CurrentUser: current,
		Force:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "no stale messages") {
		t.Fatalf("expected irreducible-context error, got %v", err)
	}
}

func contextCompactionTestSettings(endpoint string, contextLength int, maxTokens int) llm.Settings {
	settings := llm.DefaultSettings()
	settings.Endpoint = endpoint
	settings.Model = "test-model"
	settings.ContextLength = contextLength
	settings.MaxTokens = maxTokens
	settings.Endpoints[0].Endpoint = endpoint
	settings.Endpoints[0].Model = "test-model"
	settings.Endpoints[0].ContextLength = contextLength
	settings.Endpoints[0].MaxTokens = maxTokens
	settings.Endpoints[0].TimeoutSeconds = 5
	settings = settings.ForInteraction(llm.InteractionChat)
	return settings
}
