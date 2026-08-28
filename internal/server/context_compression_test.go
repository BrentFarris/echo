package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
)

type contextCompressionCompleter struct {
	summary    string
	requests   []llm.ChatRequest
	err        error
	failOnce   bool
	emptyFirst bool
}

func (c *contextCompressionCompleter) Complete(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	c.requests = append(c.requests, request)
	if c.failOnce && len(c.requests) == 1 {
		return llm.ChatResponse{}, fmt.Errorf("context_length_exceeded: summary input was too large")
	}
	if c.err != nil {
		return llm.ChatResponse{}, c.err
	}
	content := c.summary
	if c.emptyFirst && len(c.requests) == 1 {
		content = ""
	}
	return llm.ChatResponse{
		Choices: []llm.ChatChoice{{Message: llm.Message{Role: llm.RoleAssistant, Content: content}}},
		Usage:   &llm.Usage{PromptTokens: 100, CompletionTokens: 25, TotalTokens: 125},
	}, nil
}

func compressionTestSettings() llm.Settings {
	settings := llm.DefaultSettings()
	settings.ContextLength = 8192
	settings.MaxTokens = 512
	settings.ContextCompressionThresholdPercent = 70
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	return settings.NormalizedEndpointProfiles()
}

func compressionTestHistory(exchanges int) []llm.Message {
	messages := []llm.Message{{Role: llm.RoleUser, Content: "Opening request: preserve this exact instruction."}}
	for index := 0; index < exchanges; index++ {
		messages = append(messages,
			llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat(string(rune('a'+index%20)), 2400)},
			llm.Message{Role: llm.RoleUser, Content: "Follow-up " + strings.Repeat(string(rune('A'+index%20)), 1400)},
		)
	}
	return messages
}

func TestBuildCompressedModelHistoryPreservesProtectedHeadAndRawTail(t *testing.T) {
	media := llm.Message{Role: llm.RoleUser, Content: "latest exact request", ContentParts: []llm.MessageContentPart{
		llm.TextContentPart("latest exact request"), llm.ImageURLContentPart("data:image/png;base64,AAAA"),
	}}
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "opening prompt"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-old", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"old.go"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "call-old", Content: "old result"},
		media,
		{Role: llm.RoleAssistant, Content: "latest answer"},
	}
	checkpoint := &sessions.ContextCheckpoint{
		Summary: "## Goal\nContinue the task.", ProtectedHeadIndex: 0, CompactedThrough: 3,
	}

	modelHistory := buildCompressedModelHistory(canonical, checkpoint)
	if len(modelHistory) != 4 || modelHistory[0].Content != "opening prompt" || modelHistory[1].Name != contextSummaryName {
		t.Fatalf("unexpected compressed history: %#v", modelHistory)
	}
	if !strings.Contains(modelHistory[1].Content, "## Goal") || modelHistory[2].Content != "latest exact request" {
		t.Fatalf("summary or raw tail was not preserved: %#v", modelHistory)
	}
	if len(modelHistory[2].ContentParts) != 2 || modelHistory[2].ContentParts[1].ImageURL == nil {
		t.Fatalf("tail media was not preserved: %#v", modelHistory[2])
	}
	modelHistory[2].ContentParts[0].Text = "changed"
	if canonical[3].ContentParts[0].Text == "changed" {
		t.Fatal("compressed model history aliased canonical media content")
	}
}

func TestContextMeterIncludesSchemasMediaStreamDeltasAndProviderUsage(t *testing.T) {
	settings := compressionTestSettings()
	messages := []llm.Message{{Role: llm.RoleUser, Content: "inspect", ContentParts: []llm.MessageContentPart{
		llm.TextContentPart("inspect"), llm.ImageURLContentPart("data:image/png;base64,AAAA"),
	}}}
	withoutTools := contextRequestTokens(settings, messages, nil)
	withTools := contextRequestTokens(settings, messages, []llm.Tool{{Type: "function", Function: llm.ToolFunction{
		Name: "read_file", Description: strings.Repeat("schema", 100), Parameters: map[string]any{"type": "object"},
	}}})
	if withoutTools < 1000 || withTools <= withoutTools {
		t.Fatalf("meter omitted media or tool schemas: without=%d with=%d", withoutTools, withTools)
	}
	delta := estimateStreamEventTokens(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
		ID: "call-1", Type: "function", Function: llm.FunctionCallDelta{Name: "read_file", Arguments: `{"path":"main.go"}`},
	}})
	if delta <= 0 || !contextCompressionThresholdReached(90, delta, 100, nil) {
		t.Fatalf("streamed tool-call delta did not cross the estimated threshold: delta=%d", delta)
	}
	if !contextCompressionThresholdReached(1, 0, 100, &llm.Usage{PromptTokens: 75, CompletionTokens: 25}) {
		t.Fatal("provider-reported usage did not take precedence at the response boundary")
	}
}

func TestSelectCompressionBoundaryUsesExchangeBoundaries(t *testing.T) {
	settings := compressionTestSettings()
	canonical := compressionTestHistory(8)
	head, start, cutoff, err := selectCompressionBoundary(settings, canonical, nil, nil, nil)
	if err != nil {
		t.Fatalf("select boundary: %v", err)
	}
	if head != 0 || start != 1 || cutoff <= start || cutoff >= len(canonical) {
		t.Fatalf("unexpected boundary head=%d start=%d cutoff=%d", head, start, cutoff)
	}
	if canonical[cutoff].Role == llm.RoleTool {
		t.Fatalf("cutoff split a tool group at role %q", canonical[cutoff].Role)
	}
}

func TestSelectCompressionBoundaryCompressesAgentOnlyHistory(t *testing.T) {
	settings := compressionTestSettings()
	// Long agent run: one user message, then only assistant/tool exchanges.
	// This shape used to fail with "not enough completed history to compress".
	canonical := []llm.Message{{Role: llm.RoleUser, Content: "Build the feature and keep going until it passes."}}
	for index := 0; index < 12; index++ {
		canonical = append(canonical,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("call-%d", index), Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: fmt.Sprintf("call-%d", index), Content: strings.Repeat(string(rune('a'+index%20)), 2000)},
			llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat(string(rune('b'+index%20)), 1200)},
		)
	}
	head, start, cutoff, err := selectCompressionBoundary(settings, canonical, nil, nil, nil)
	if err != nil {
		t.Fatalf("agent-only history must be compressible: %v", err)
	}
	if head != 0 || start != 1 || cutoff <= start || cutoff >= len(canonical) {
		t.Fatalf("unexpected boundary head=%d start=%d cutoff=%d", head, start, cutoff)
	}
	if canonical[cutoff].Role == llm.RoleTool {
		t.Fatalf("cutoff split a tool group at role %q", canonical[cutoff].Role)
	}
}

func TestValidateContextMessageOrderingRejectsSplitToolGroups(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "inspect"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
		{Role: llm.RoleUser, Content: "continue"},
	}
	if err := validateContextMessageOrdering(messages); err == nil {
		t.Fatal("expected a missing tool result to fail validation")
	}
	messages[2] = llm.Message{Role: llm.RoleTool, ToolCallID: "call-1", Content: "ok"}
	if err := validateContextMessageOrdering(messages); err != nil {
		t.Fatalf("valid tool group was rejected: %v", err)
	}
}

func TestSanitizeContextToolPairsRepairsMalformedHistory(t *testing.T) {
	malformed := []llm.Message{
		{Role: llm.RoleUser, Content: "inspect"},
		// Dangling call: results never recorded before the next user message.
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
		{Role: llm.RoleUser, Content: "continue"},
		// Orphaned result with no matching pending call.
		{Role: llm.RoleTool, ToolCallID: "call-ghost", Content: "orphan"},
		// Call without an id (unusable) plus a valid one.
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "", Type: "function", Function: llm.FunctionCall{Name: "a"}}, {ID: "call-2", Type: "function", Function: llm.FunctionCall{Name: "b"}}}},
		{Role: llm.RoleTool, ToolCallID: "call-2", Content: "ok"},
	}
	fixed := sanitizeContextToolPairs(malformed)
	if err := validateContextMessageOrdering(fixed); err != nil {
		t.Fatalf("sanitized history should validate, got: %v", err)
	}
	// A stub result must be injected for the dangling call-1.
	var sawStub bool
	for _, message := range fixed {
		if message.Role == llm.RoleTool && message.ToolCallID == "call-1" && message.Content != "" {
			sawStub = true
		}
	}
	if !sawStub {
		t.Fatalf("expected a stub result for the dangling call: %#v", fixed)
	}
	// The orphaned result must be dropped.
	for _, message := range fixed {
		if message.Role == llm.RoleTool && message.ToolCallID == "call-ghost" {
			t.Fatalf("orphaned tool result was not dropped: %#v", fixed)
		}
	}
	// The id-less call must be stripped.
	for _, message := range fixed {
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				t.Fatalf("id-less tool call was not stripped: %#v", fixed)
			}
		}
	}
	// The input must not be mutated.
	if len(malformed[1].ToolCalls) != 1 || malformed[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("sanitizer mutated the input: %#v", malformed)
	}
}

func TestCompressContextRepairsMalformedHistoryInsteadOfFailing(t *testing.T) {
	settings := compressionTestSettings()
	// Build a history of normal tool exchanges followed by an interrupted
	// tool call (no result recorded), which previously made compressContext
	// fail with "appears before all tool results were recorded".
	canonical := []llm.Message{{Role: llm.RoleUser, Content: "Opening request: preserve this exact instruction."}}
	for index := 0; index < 6; index++ {
		canonical = append(canonical,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("call-%d", index), Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
			llm.Message{Role: llm.RoleTool, ToolCallID: fmt.Sprintf("call-%d", index), Content: strings.Repeat(string(rune('a'+index%20)), 1400)},
			llm.Message{Role: llm.RoleUser, Content: "Follow-up " + string(rune('A'+index%20))},
		)
	}
	// The final exchange is interrupted: a tool call with no recorded result.
	canonical = append(canonical,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-interrupted", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
	)
	completer := &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived.\n### In Progress\nContinue.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nNone.\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request."}
	server := &Server{llmCompleter: completer, llmSettings: settings}
	result, err := server.compressContext(context.Background(), settings, canonical, nil, nil, nil, 0, "estimated")
	if err != nil {
		t.Fatalf("compression should repair malformed history instead of failing: %v", err)
	}
	if result.Checkpoint == nil || result.Checkpoint.Summary == "" {
		t.Fatalf("expected a committed checkpoint: %#v", result)
	}
	// The rebuilt context must validate (stub injected for the dangling call).
	rebuilt := buildCompressedModelHistory(canonical, result.Checkpoint)
	if err := validateContextMessageOrdering(sanitizeContextToolPairs(rebuilt)); err != nil {
		t.Fatalf("rebuilt compressed history should validate: %v", err)
	}
}

func TestCompressionChunkingKeepsToolGroupsAndRetriesOneOversizedSummary(t *testing.T) {
	settings := compressionTestSettings()
	toolGroup := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-large", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}}},
		{Role: llm.RoleTool, ToolCallID: "call-large", Content: strings.Repeat("result", 1000)},
		{Role: llm.RoleUser, Content: "next exchange"},
		{Role: llm.RoleAssistant, Content: "done"},
	}

	// A limit between the raw and pruned sizes of the first exchange forces
	// pruning without splitting the tool call/result group.
	rawEstimate := estimateSummaryRequestTokens("", toolGroup[:2])
	prunedEstimate := estimateSummaryRequestTokens("", pruneToolOutputs(toolGroup[:2]))
	chunks, _ := chunkSummaryUnits("", toolGroup, (rawEstimate+prunedEstimate)/2)
	if len(chunks) == 0 {
		t.Fatalf("expected at least one summary chunk: %#v", chunks)
	}
	for _, chunk := range chunks {
		if err := validateContextMessageOrdering(chunk); err != nil {
			t.Fatalf("tool group was split across compression chunks: %v", err)
		}
	}
	foundStub := false
	for _, chunk := range chunks {
		for _, message := range chunk {
			if message.Content == "[Old tool output cleared to save context space]" {
				foundStub = true
			}
		}
	}
	if !foundStub {
		t.Fatalf("expected the archived tool output to be pruned for the summary: %#v", chunks)
	}

	completer := &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived.\n### In Progress\nContinue.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nNone.\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request.", failOnce: true}
	server := &Server{llmCompleter: completer, llmSettings: settings}
	result, err := server.compressContext(context.Background(), settings, compressionTestHistory(10), nil, nil, nil, 0, "estimated")
	if err != nil {
		t.Fatalf("compression did not retry a splittable context-limit failure: %v", err)
	}
	if len(completer.requests) <= result.ChunkCount || result.ChunkCount < 2 {
		t.Fatalf("expected one failed request followed by smaller aligned chunks: calls=%d chunks=%d", len(completer.requests), result.ChunkCount)
	}
}

func TestCompressionChunkingDropsExchangeThatCannotFitSummaryWindow(t *testing.T) {
	// One exchange whose content exceeds the summary window and cannot be
	// pruned or excerpted (no tool output), followed by a normal exchange.
	oversized := []llm.Message{
		{Role: llm.RoleUser, Content: strings.Repeat("pasted data ", 28_000)},
		{Role: llm.RoleAssistant, Content: "done"},
		{Role: llm.RoleUser, Content: "small follow-up"},
		{Role: llm.RoleAssistant, Content: "ack"},
	}
	chunks, dropped := chunkSummaryUnits("", oversized, 819)
	if dropped != 1 {
		t.Fatalf("expected the oversized exchange to be dropped from the summary, got dropped=%d chunks=%d", dropped, len(chunks))
	}
	if len(chunks) != 1 || len(chunks[0]) != 2 || chunks[0][0].Role != llm.RoleUser {
		t.Fatalf("expected only the small exchange to be summarized: %#v", chunks)
	}
}

func TestCompressContextCommitsOnlyMeaningfulNonemptySummary(t *testing.T) {
	settings := compressionTestSettings()
	canonical := compressionTestHistory(8)
	completer := &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nKeep identifiers exact.\n## Progress\n### Done\nArchived work.\n### In Progress\nCurrent work.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nold.go\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request."}
	server := &Server{llmCompleter: completer, llmSettings: settings}

	result, err := server.compressContext(context.Background(), settings, canonical, nil, nil, nil, 0, "estimated")
	if err != nil {
		t.Fatalf("compress context: %v", err)
	}
	if result.Checkpoint == nil || result.Checkpoint.Summary == "" || result.AfterTokens >= result.BeforeTokens {
		t.Fatalf("compression did not produce a reduced checkpoint: %#v", result)
	}
	if result.Checkpoint.ProtectedHeadIndex != 0 || result.Checkpoint.CompactedThrough <= 1 || result.RetiredMessages <= 0 {
		t.Fatalf("unexpected checkpoint boundary: %#v", result.Checkpoint)
	}
	if len(completer.requests) == 0 || len(completer.requests[0].Tools) != 0 || completer.requests[0].Stream {
		t.Fatalf("summary request must be non-streaming and tool-free: %#v", completer.requests)
	}
	if kwargs := completer.requests[0].ChatTemplateKwargs; kwargs == nil || kwargs.EnableThinking == nil || *kwargs.EnableThinking {
		t.Fatalf("summary request must disable thinking so the output budget buys summary text: %#v", completer.requests[0].ChatTemplateKwargs)
	}

	empty := &contextCompressionCompleter{summary: "   "}
	server.llmCompleter = empty
	previous := *result.Checkpoint
	_, err = server.compressContext(context.Background(), settings, append(canonical, compressionTestHistory(3)[1:]...), result.Checkpoint, nil, nil, 0, "estimated")
	if err == nil || *result.Checkpoint != previous {
		t.Fatalf("empty summary should fail atomically without changing the checkpoint: err=%v checkpoint=%#v", err, result.Checkpoint)
	}
	if len(empty.requests) != maxEmptyAssistantRetries+1 {
		t.Fatalf("expected %d empty-summary attempts before failing, got %d calls", maxEmptyAssistantRetries+1, len(empty.requests))
	}
}

func TestCompressContextContinuesEmptySummaryLikeChatRetries(t *testing.T) {
	settings := compressionTestSettings()
	canonical := compressionTestHistory(3)
	completer := &contextCompressionCompleter{
		summary:    "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived work.\n### In Progress\nCurrent work.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nold.go\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request.",
		emptyFirst: true,
	}
	server := &Server{llmCompleter: completer, llmSettings: settings}

	result, err := server.compressContext(context.Background(), settings, canonical, nil, nil, nil, 0, "estimated")
	if err != nil {
		t.Fatalf("compression should recover from an empty first summary: %v", err)
	}
	if len(completer.requests) != 2 {
		t.Fatalf("expected one continue retry after the empty summary, got %d calls", len(completer.requests))
	}
	retryMessages := completer.requests[1].Messages
	lastUser := retryMessages[len(retryMessages)-1]
	if lastUser.Role != llm.RoleUser || !strings.Contains(lastUser.Content, "completed without the summary") {
		t.Fatalf("empty-summary retry should continue with a user message like the chat loop: %#v", lastUser)
	}
	if result.Checkpoint.Summary == "" || !strings.HasPrefix(result.Checkpoint.Summary, "## Goal") {
		t.Fatalf("retry did not produce a committed summary: %q", result.Checkpoint.Summary)
	}
	if result.SummaryUsage == nil || result.SummaryUsage.TotalTokens != 250 {
		t.Fatalf("summary usage should merge both calls: %#v", result.SummaryUsage)
	}
}

func TestCompressContextEscalatesEmptySummaryToNoPreamblePrompt(t *testing.T) {
	settings := compressionTestSettings()
	canonical := compressionTestHistory(3)
	completer := &contextCompressionCompleter{summary: "   "}
	server := &Server{llmCompleter: completer, llmSettings: settings}

	if _, err := server.compressContext(context.Background(), settings, canonical, nil, nil, nil, 0, "estimated"); err == nil {
		t.Fatal("expected all-empty summaries to fail")
	}
	if len(completer.requests) != maxEmptyAssistantRetries+1 {
		t.Fatalf("expected %d attempts, got %d", maxEmptyAssistantRetries+1, len(completer.requests))
	}
	finalSystem := completer.requests[len(completer.requests)-1].Messages[0]
	if !strings.Contains(finalSystem.Content, "nothing else") {
		t.Fatalf("final attempt should use the stricter no-preamble system prompt: %q", finalSystem.Content)
	}
	firstSystem := completer.requests[0].Messages[0]
	if firstSystem.Content == finalSystem.Content {
		t.Fatal("only the final attempt should escalate to the no-preamble prompt")
	}
}

func TestConversationHistorySearchIsBoundedAndFilterable(t *testing.T) {
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "opening request"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"src/hidden.go"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: "exact archived error ECHO-417 in src/hidden.go"},
		{Role: llm.RoleUser, Content: "older follow-up"},
		{Role: llm.RoleAssistant, Content: "resolved ECHO-417"},
		{Role: llm.RoleUser, Content: "current request"},
	}
	checkpoint := &sessions.ContextCheckpoint{Summary: "summary", ProtectedHeadIndex: 0, CompactedThrough: 5}
	arguments, _ := json.Marshal(contextHistorySearchArgs{Query: "ECHO-417 hidden.go", Roles: []string{llm.RoleTool}, Tools: []string{"read_file"}, Limit: 20})

	result := (&chatSession{}).executeContextHistorySearch(canonical, checkpoint, arguments)
	if !result.Success {
		t.Fatalf("history search failed: %#v", result.Error)
	}
	data, err := json.Marshal(result.Output)
	if err != nil {
		t.Fatalf("marshal search output: %v", err)
	}
	var output struct {
		Count   int `json:"count"`
		Matches []struct {
			MessageIndex int    `json:"messageIndex"`
			Tool         string `json:"tool"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("decode search output: %v", err)
	}
	if output.Count != 1 || len(output.Matches) != 1 || output.Matches[0].MessageIndex != 2 || output.Matches[0].Tool != "read_file" {
		t.Fatalf("unexpected filtered match: %#v", output)
	}

	arguments, _ = json.Marshal(contextHistorySearchArgs{Query: "src/hidden.go", Roles: []string{llm.RoleAssistant}, Tools: []string{"read_file"}, Limit: 1})
	result = (&chatSession{}).executeContextHistorySearch(canonical, checkpoint, arguments)
	data, _ = json.Marshal(result.Output)
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("decode tool-argument search output: %v", err)
	}
	if output.Count != 1 || len(output.Matches) != 1 || output.Matches[0].MessageIndex != 1 {
		t.Fatalf("exact tool arguments were not searchable: %#v", output)
	}
}

func TestCompressContextCommitsBestEffortWhenTailExceedsTarget(t *testing.T) {
	settings := llm.DefaultSettings()
	settings.ContextLength = 102400
	settings.MaxTokens = 1024
	settings.ContextCompressionThresholdPercent = 64
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	settings = settings.NormalizedEndpointProfiles()

	// One large retired exchange plus a recent tail that alone exceeds the
	// compression target. The old gate failed this with "above the target";
	// it should now commit because it still reclaims meaningfully.
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "Opening request"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("a", 52500)},
		{Role: llm.RoleUser, Content: strings.Repeat("b", 140000)},
	}
	completer := &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived work.\n### In Progress\nCurrent work.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nNone.\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request."}
	server := &Server{llmCompleter: completer, llmSettings: settings}

	result, err := server.compressContext(context.Background(), settings, canonical, nil, nil, nil, 0, "estimated")
	if err != nil {
		t.Fatalf("best-effort compression should commit when it reclaims meaningfully: %v", err)
	}
	target := max(1024, compressionThresholdTokens(settings)/2)
	if result.AfterTokens <= target {
		t.Fatalf("expected the committed context to stay above the target (tail is oversized): after=%d target=%d", result.AfterTokens, target)
	}
	if result.AfterTokens >= result.BeforeTokens {
		t.Fatalf("best-effort compression must still reduce the context: before=%d after=%d", result.BeforeTokens, result.AfterTokens)
	}
	if result.Checkpoint == nil || result.Checkpoint.CompactedThrough != 2 {
		t.Fatalf("unexpected checkpoint: %#v", result.Checkpoint)
	}
}

func TestCompressContextStillFailsWhenBestEffortReclaimsNothing(t *testing.T) {
	settings := llm.DefaultSettings()
	settings.ContextLength = 102400
	settings.MaxTokens = 1024
	settings.ContextCompressionThresholdPercent = 64
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	settings = settings.NormalizedEndpointProfiles()

	// The only compressible content is tiny next to the tail, so even a
	// best-effort boundary would not reclaim anything meaningful.
	canonical := []llm.Message{
		{Role: llm.RoleUser, Content: "Opening request"},
		{Role: llm.RoleAssistant, Content: "small answer"},
		{Role: llm.RoleUser, Content: strings.Repeat("b", 140000)},
	}
	completer := &contextCompressionCompleter{summary: "## Goal\nContinue."}
	server := &Server{llmCompleter: completer, llmSettings: settings}

	if _, err := server.compressContext(context.Background(), settings, canonical, nil, nil, nil, 0, "estimated"); !errors.Is(err, errNothingToCompress) {
		t.Fatalf("expected nothing-to-compress for a non-reclaiming boundary, got %v", err)
	}
}

func TestManualCompressionWorksWhenAutomaticCompressionIsDisabled(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "manual-compression")
	settings := compressionTestSettings()
	settings.ContextLength = 65536
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	settings = settings.NormalizedEndpointProfiles()
	disabled := false
	settings.ContextCompressionEnabled = &disabled
	settings.Endpoints[0].ContextCompressionEnabled = &disabled
	server.llmSettings = settings
	server.llmCompleter = &contextCompressionCompleter{summary: "## Goal\nContinue.\n## Constraints & Preferences\nExact.\n## Progress\n### Done\nArchived.\n### In Progress\nContinue.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nNone.\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nOpening request."}
	canonical := compressionTestHistory(8)
	transcript := sessions.TabTranscript{
		ChatID: "chat-compression", Preview: "Continue", Revision: 1, Messages: canonical,
		Turns: []sessions.Turn{{
			ID: "turn-compression", RequestID: "request-compression", UserContent: canonical[len(canonical)-1].Content,
			UserMessageIndex: len(canonical) - 1, Status: "done", AssistantTurns: []sessions.AssistantTurn{},
		}},
	}
	store := sessions.NewWorkspaceStore(workspace.MainPath)
	if err := store.Save(sessions.ChatWorkspace{
		Version: sessions.WorkspaceVersion, WorkspaceID: workspace.ID, ActiveChatID: transcript.ChatID,
		Tabs: []sessions.TabTranscript{transcript},
	}); err != nil {
		t.Fatalf("seed chat workspace: %v", err)
	}

	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	subscribeChat(t, connection, workspace.ID)
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_compress", "workspaceId": workspace.ID, "chatId": transcript.ChatID,
	}); err != nil {
		t.Fatalf("request manual compression: %v", err)
	}
	readSessionEventForChat(t, connection, transcript.ChatID, "context_compression_started")
	completed := readSessionEventForChat(t, connection, transcript.ChatID, "context_compression_completed")
	activity, _ := completed["compression"].(map[string]any)
	if activity["status"] != "completed" || activity["recoveryAvailable"] != true {
		t.Fatalf("unexpected compression lifecycle: %#v", completed)
	}

	stored, err := store.Load(workspace.ID)
	if err != nil {
		t.Fatalf("reload compressed chat: %v", err)
	}
	if len(stored.Tabs) != 1 || stored.Tabs[0].ContextCheckpoint == nil || len(stored.Tabs[0].Messages) != len(canonical) {
		t.Fatalf("checkpoint or canonical history was not persisted atomically: %#v", stored)
	}
}

func TestManualCompressionQueuesDuringFirstStreamingTurn(t *testing.T) {
	server, _ := newTestServer(t)
	workspace := createChatWorkspace(t, server, "queued-first-turn-compression")
	settings := compressionTestSettings()
	settings.ContextLength = 65536
	disabled := false
	settings.ContextCompressionEnabled = &disabled
	settings.Endpoints[0] = settings.Endpoints[0].WithGenerationFromSettings(settings)
	settings.Endpoints[0].ContextCompressionEnabled = &disabled
	server.llmSettings = settings.NormalizedEndpointProfiles()
	streamer := &gatedStreamer{started: make(chan struct{}), release: make(chan struct{})}
	server.llm = streamer

	url := startWebSocketTestServer(t, server)
	connection := dialSharedClient(t, url)
	if err := connection.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, connection)
	chatID := snapshot["activeChatId"].(string)
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID,
		"requestId": "queued-compression", "message": "keep streaming",
	}); err != nil {
		t.Fatal(err)
	}
	readSessionEventForChat(t, connection, chatID, "turn_started")
	select {
	case <-streamer.started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	if err := connection.WriteJSON(map[string]any{
		"type": "chat_compress", "workspaceId": workspace.ID, "chatId": chatID,
	}); err != nil {
		t.Fatal(err)
	}
	queued := readSessionEventForChat(t, connection, chatID, "context_compression_queued")
	activity, _ := queued["compression"].(map[string]any)
	if activity["status"] != "queued" || activity["phase"] != "mid_turn" {
		t.Fatalf("unexpected queued lifecycle: %#v", queued)
	}
	close(streamer.release)
	readSessionEventForChat(t, connection, chatID, "turn_finished")
}

func TestDeferredManualCompressionUpdatesItsOriginalActivityAnchor(t *testing.T) {
	anchor := 0
	session := &chatSession{
		transcript: sessions.TabTranscript{Turns: []sessions.Turn{{
			ID: "completed-turn", Compressions: []sessions.CompressionActivity{{
				ID: "compression-deferred", Trigger: "manual", Phase: "mid_turn", Status: "queued", AfterAssistantNumber: &anchor,
			}},
		}}},
		active: &sessions.Turn{ID: "next-turn"},
	}
	existing, turnID, ok := session.compressionActivityLocked("compression-deferred")
	if !ok || turnID != "completed-turn" || existing.AfterAssistantNumber == nil || *existing.AfterAssistantNumber != 0 {
		t.Fatalf("deferred activity anchor was not found: activity=%#v turn=%q", existing, turnID)
	}
	existing.Status = "completed"
	session.upsertCompressionActivityLocked(existing)
	if session.transcript.Turns[0].Compressions[0].Status != "completed" || len(session.active.Compressions) != 0 {
		t.Fatalf("deferred activity was duplicated onto the next turn: transcript=%#v active=%#v", session.transcript, session.active)
	}
}
