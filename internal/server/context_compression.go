package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/tools"
)

const contextSummaryName = "echo-context-summary"
const contextHistorySearchToolName = "conversation_history_search"

var errNothingToCompress = errors.New("there is not enough completed history to compress")

func classifyCompressionError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, errNothingToCompress):
		return "nothing_to_compress"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case llm.IsContextLengthExceeded(err):
		return "context_limit"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "summary") && (strings.Contains(message, "empty") || strings.Contains(message, "no choices")) {
		return "invalid_summary"
	}
	if strings.Contains(message, "validate") || strings.Contains(message, "tool result") || strings.Contains(message, "tool call") {
		return "invalid_message_order"
	}
	return "provider_error"
}

type compressionResult struct {
	Checkpoint      *sessions.ContextCheckpoint
	BeforeTokens    int
	AfterTokens     int
	UsageSource     string
	SummaryUsage    *llm.Usage
	ChunkCount      int
	RetiredMessages int
}

func (s *chatSession) takeManualCompressionPending(turnID string) (bool, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(turnID) || !s.manualCompressionPending {
		return false, "", ""
	}
	id := s.manualCompressionID
	model := s.manualCompressionModel
	s.manualCompressionPending = false
	s.manualCompressionID = ""
	s.manualCompressionModel = ""
	return true, id, model
}

func (s *chatSession) compressActiveContext(ctx context.Context, settings llm.Settings, canonical []llm.Message, checkpoint *sessions.ContextCheckpoint, prefix []llm.Message, toolSchema []llm.Tool, turnID string, assistantNumber int, manual bool, activityID string, observedBefore int, usageSource string) (*sessions.ContextCheckpoint, compressionResult, error) {
	trigger := "automatic"
	if manual {
		trigger = "manual"
	}
	phase := "pre_turn"
	var anchor *int
	if assistantNumber > 0 {
		phase = "mid_turn"
		value := assistantNumber - 1
		anchor = &value
	}
	if activityID == "" {
		activityID = newSessionID("compression")
	}
	started := time.Now().UTC()
	activity := sessions.CompressionActivity{
		ID: activityID, Trigger: trigger, Phase: phase, Status: "running",
		AfterAssistantNumber: anchor, ThresholdPercent: settings.ContextCompressionThresholdPercent,
		ContextLength: settings.ContextLength, UsageSource: usageSource, StartedAt: started,
	}
	s.mu.Lock()
	if !s.isActiveLocked(turnID) {
		s.mu.Unlock()
		return nil, compressionResult{}, errChatCanceled
	}
	displayTurnID := turnID
	if manual {
		if existing, existingTurnID, ok := s.compressionActivityLocked(activityID); ok {
			activity.Phase = existing.Phase
			if existing.AfterAssistantNumber != nil {
				value := *existing.AfterAssistantNumber
				activity.AfterAssistantNumber = &value
			} else {
				activity.AfterAssistantNumber = nil
			}
			displayTurnID = existingTurnID
		}
	}
	s.upsertCompressionActivityLocked(activity)
	step := assistantNumber
	s.appendTrajectoryLocked("context/compression_start", turnID, &step, map[string]any{
		"compressionId": activityID, "trigger": trigger, "phase": phase,
		"thresholdPercent": settings.ContextCompressionThresholdPercent,
		"contextLength":    settings.ContextLength, "model": settings.Model, "endpoint": settings.Endpoint,
		"usageSource": usageSource, "startedAt": started,
	})
	s.emitLocked(map[string]any{
		"type": "context_compression_started", "turnId": displayTurnID, "compression": activity,
	})
	s.mu.Unlock()

	result, err := s.manager.server.compressContext(ctx, settings, canonical, checkpoint, prefix, toolSchema, observedBefore, usageSource)
	completed := time.Now().UTC()
	activity.CompletedAt = &completed
	activity.DurationMs = completed.Sub(started).Milliseconds()
	activity.BeforeTokens = result.BeforeTokens
	activity.AfterTokens = result.AfterTokens
	activity.ReclaimedTokens = max(0, result.BeforeTokens-result.AfterTokens)
	if result.UsageSource != "" {
		activity.UsageSource = result.UsageSource
	}
	activity.RecoveryAvailable = checkpoint != nil

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isActiveLocked(turnID) {
		return nil, compressionResult{}, errChatCanceled
	}
	if err != nil {
		activity.Error = err.Error()
		activity.ErrorClass = classifyCompressionError(err)
		trajectoryType := "context/compression_error"
		eventType := "context_compression_failed"
		activity.Status = "failed"
		if errors.Is(err, errNothingToCompress) {
			trajectoryType = "context/compression_skipped"
			eventType = "context_compression_skipped"
			activity.Status = "skipped"
		}
		s.upsertCompressionActivityLocked(activity)
		s.appendTrajectoryLocked(trajectoryType, turnID, &step, map[string]any{
			"compressionId": activityID, "trigger": trigger, "phase": phase,
			"status": activity.Status, "thresholdPercent": settings.ContextCompressionThresholdPercent,
			"contextLength": settings.ContextLength, "model": settings.Model, "endpoint": settings.Endpoint,
			"usageSource": activity.UsageSource, "beforeTokens": activity.BeforeTokens,
			"afterTokens": activity.AfterTokens, "reclaimedTokens": activity.ReclaimedTokens,
			"summaryUsage": result.SummaryUsage, "chunkCount": result.ChunkCount,
			"recoveryAvailable": activity.RecoveryAvailable, "errorClass": activity.ErrorClass, "error": activity.Error,
			"durationMs": activity.DurationMs, "completedAt": completed,
		})
		s.emitLocked(map[string]any{"type": eventType, "turnId": displayTurnID, "compression": activity})
		logf("context compression chat=%s trigger=%s phase=%s status=%s before=%d after=%d duration_ms=%d error=%q", s.transcript.ChatID, trigger, phase, activity.Status, activity.BeforeTokens, activity.AfterTokens, activity.DurationMs, activity.Error)
		return nil, compressionResult{}, err
	}
	result.Checkpoint.LastCompactedAt = completed
	result.Checkpoint.LastAssistantNumber = assistantNumber
	activity.Status = "completed"
	activity.RecoveryAvailable = true
	s.upsertCompressionActivityLocked(activity)
	s.appendTrajectoryLocked("context/compression_complete", turnID, &step, map[string]any{
		"compressionId": activityID, "trigger": trigger, "phase": phase, "status": activity.Status,
		"thresholdPercent": settings.ContextCompressionThresholdPercent, "contextLength": settings.ContextLength,
		"model": settings.Model, "endpoint": settings.Endpoint, "usageSource": result.UsageSource,
		"beforeTokens": result.BeforeTokens, "afterTokens": result.AfterTokens,
		"reclaimedTokens": result.BeforeTokens - result.AfterTokens, "retiredMessages": result.RetiredMessages,
		"summaryUsage": result.SummaryUsage, "chunkCount": result.ChunkCount, "summary": result.Checkpoint.Summary,
		"recoveryAvailable": true, "durationMs": activity.DurationMs, "completedAt": completed,
	})
	s.emitLocked(map[string]any{"type": "context_compression_completed", "turnId": displayTurnID, "compression": activity})
	logf("context compression chat=%s trigger=%s phase=%s status=completed before=%d after=%d duration_ms=%d", s.transcript.ChatID, trigger, phase, activity.BeforeTokens, activity.AfterTokens, activity.DurationMs)
	return cloneContextCheckpoint(result.Checkpoint), result, nil
}

func (s *chatSession) upsertCompressionActivityLocked(activity sessions.CompressionActivity) {
	if s.active != nil {
		for index := range s.active.Compressions {
			if s.active.Compressions[index].ID == activity.ID {
				s.active.Compressions[index] = activity
				return
			}
		}
	}
	for turnIndex := len(s.transcript.Turns) - 1; turnIndex >= 0; turnIndex-- {
		for index := range s.transcript.Turns[turnIndex].Compressions {
			if s.transcript.Turns[turnIndex].Compressions[index].ID == activity.ID {
				s.transcript.Turns[turnIndex].Compressions[index] = activity
				return
			}
		}
	}
	if s.active != nil {
		s.active.Compressions = append(s.active.Compressions, activity)
	}
}

func (s *chatSession) compressionActivityLocked(activityID string) (sessions.CompressionActivity, string, bool) {
	if s.active != nil {
		for _, activity := range s.active.Compressions {
			if activity.ID == activityID {
				return activity, s.active.ID, true
			}
		}
	}
	for turnIndex := len(s.transcript.Turns) - 1; turnIndex >= 0; turnIndex-- {
		for _, activity := range s.transcript.Turns[turnIndex].Compressions {
			if activity.ID == activityID {
				return activity, s.transcript.Turns[turnIndex].ID, true
			}
		}
	}
	return sessions.CompressionActivity{}, "", false
}

func buildCompressedModelHistory(canonical []llm.Message, checkpoint *sessions.ContextCheckpoint) []llm.Message {
	if checkpoint == nil || strings.TrimSpace(checkpoint.Summary) == "" ||
		checkpoint.ProtectedHeadIndex < 0 || checkpoint.ProtectedHeadIndex >= len(canonical) ||
		checkpoint.CompactedThrough <= checkpoint.ProtectedHeadIndex || checkpoint.CompactedThrough > len(canonical) {
		return cloneContextMessages(canonical)
	}
	output := make([]llm.Message, 0, checkpoint.ProtectedHeadIndex+2+len(canonical)-checkpoint.CompactedThrough)
	output = append(output, cloneContextMessages(canonical[:checkpoint.ProtectedHeadIndex+1])...)
	output = append(output, llm.Message{
		Role: llm.RoleAssistant,
		Name: contextSummaryName,
		Content: "[CONTEXT COMPACTION]\nEarlier completed exchanges were compressed into this continuation summary. " +
			"Use conversation_history_search when an exact archived detail is needed.\n\n" + strings.TrimSpace(checkpoint.Summary),
	})
	output = append(output, cloneContextMessages(canonical[checkpoint.CompactedThrough:])...)
	return output
}

func cloneContextMessages(messages []llm.Message) []llm.Message {
	output := append([]llm.Message(nil), messages...)
	for index := range output {
		output[index].ContentParts = append([]llm.MessageContentPart(nil), messages[index].ContentParts...)
		output[index].ToolCalls = append([]llm.ToolCall(nil), messages[index].ToolCalls...)
	}
	return output
}

func cloneContextCheckpoint(checkpoint *sessions.ContextCheckpoint) *sessions.ContextCheckpoint {
	if checkpoint == nil {
		return nil
	}
	copy := *checkpoint
	return &copy
}

func validateContextMessageOrdering(messages []llm.Message) error {
	pending := map[string]struct{}{}
	for index, message := range messages {
		if len(pending) > 0 && message.Role != llm.RoleTool {
			return fmt.Errorf("message %d (%s) appears before all tool results were recorded", index, message.Role)
		}
		switch message.Role {
		case llm.RoleAssistant:
			for _, call := range message.ToolCalls {
				callID := strings.TrimSpace(call.ID)
				if callID == "" {
					return fmt.Errorf("assistant message %d contains a tool call without an id", index)
				}
				if _, exists := pending[callID]; exists {
					return fmt.Errorf("assistant message %d repeats tool call id %q", index, callID)
				}
				pending[callID] = struct{}{}
			}
		case llm.RoleTool:
			callID := strings.TrimSpace(message.ToolCallID)
			if _, exists := pending[callID]; !exists {
				return fmt.Errorf("tool message %d has no matching assistant call %q", index, callID)
			}
			delete(pending, callID)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("context ends before %d tool result(s) were recorded", len(pending))
	}
	return nil
}

func compressionThresholdTokens(settings llm.Settings) int {
	settings = settings.Normalized()
	return settings.ContextLength * settings.ContextCompressionThresholdPercent / 100
}

func contextRequestTokens(settings llm.Settings, messages []llm.Message, tools []llm.Tool) int {
	request, err := llm.NewChatRequest(settings, messages, llm.WithStream(true), llm.WithTools(tools))
	if err != nil {
		return estimateMessagesTokens(messages) + estimateToolsTokens(tools)
	}
	return estimateChatRequestTokens(request)
}

func estimateChatRequestTokens(request llm.ChatRequest) int {
	return estimateMessagesTokens(request.Messages) + estimateToolsTokens(request.Tools) + 16
}

func estimateMessagesTokens(messages []llm.Message) int {
	total := 0
	for _, message := range messages {
		total += 6 + estimateTextTokens(message.Role) + estimateTextTokens(message.Name) +
			estimateTextTokens(message.Content) + estimateTextTokens(message.ToolCallID)
		for _, part := range message.ContentParts {
			total += estimateTextTokens(part.Type) + estimateTextTokens(part.Text)
			if part.ImageURL != nil {
				total += 1024
			}
			if part.VideoURL != nil {
				total += 4096
			}
		}
		for _, call := range message.ToolCalls {
			total += 8 + estimateTextTokens(call.ID) + estimateTextTokens(call.Type) +
				estimateTextTokens(call.Function.Name) + estimateTextTokens(call.Function.Arguments)
		}
	}
	return total
}

func estimateToolsTokens(tools []llm.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return len(tools) * 64
	}
	return estimateTextTokens(string(data)) + len(tools)*8
}

func estimateTextTokens(value string) int {
	if value == "" {
		return 0
	}
	ascii := 0
	nonASCII := 0
	for _, r := range value {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
	}
	return int(math.Ceil(float64(ascii)/3.5)) + nonASCII
}

func estimateStreamEventTokens(event llm.StreamEvent) int {
	switch event.Type {
	case llm.EventToken, llm.EventReasoning:
		return estimateTextTokens(event.Content)
	case llm.EventToolCall:
		if event.ToolCall != nil {
			return estimateTextTokens(event.ToolCall.ID) + estimateTextTokens(event.ToolCall.Type) +
				estimateTextTokens(event.ToolCall.Function.Name) + estimateTextTokens(event.ToolCall.Function.Arguments)
		}
	}
	return 0
}

func contextCompressionThresholdReached(requestTokens, estimatedOutputTokens, threshold int, usage *llm.Usage) bool {
	if threshold <= 0 {
		return false
	}
	if usage != nil {
		total := usage.TotalTokens
		if total == 0 {
			total = usage.PromptTokens + usage.CompletionTokens
		}
		if total >= threshold {
			return true
		}
	}
	return requestTokens+estimatedOutputTokens >= threshold
}

func firstRealUserIndex(messages []llm.Message) int {
	for index, message := range messages {
		if message.Role == llm.RoleUser && strings.TrimSpace(message.Content) != "" {
			return index
		}
	}
	return -1
}

func selectCompressionBoundary(settings llm.Settings, canonical []llm.Message, checkpoint *sessions.ContextCheckpoint, prefix []llm.Message, tools []llm.Tool) (head, start, cutoff int, err error) {
	head = firstRealUserIndex(canonical)
	if head < 0 {
		return 0, 0, 0, errNothingToCompress
	}
	start = head + 1
	if checkpoint != nil && checkpoint.ProtectedHeadIndex == head && checkpoint.CompactedThrough > start && checkpoint.CompactedThrough <= len(canonical) {
		start = checkpoint.CompactedThrough
	}
	trigger := compressionThresholdTokens(settings)
	target := max(1024, trigger/2)
	summaryReserve := min(12000, max(1024, settings.ContextLength*5/100))
	full := append(cloneContextMessages(prefix), buildCompressedModelHistory(canonical, checkpoint)...)
	minimumRetired := max(1024, contextRequestTokens(settings, full, tools)/10)
	for index := start + 1; index < len(canonical); index++ {
		if canonical[index].Role != llm.RoleUser {
			continue
		}
		if estimateMessagesTokens(canonical[start:index]) < minimumRetired {
			continue
		}
		candidate := make([]llm.Message, 0, len(prefix)+head+3+len(canonical)-index)
		candidate = append(candidate, prefix...)
		candidate = append(candidate, canonical[:head+1]...)
		candidate = append(candidate, llm.Message{Role: llm.RoleAssistant, Name: contextSummaryName, Content: strings.Repeat("s", summaryReserve*3)})
		candidate = append(candidate, canonical[index:]...)
		if contextRequestTokens(settings, candidate, tools) <= target {
			return head, start, index, nil
		}
		cutoff = index
	}
	if cutoff > start {
		return head, start, cutoff, nil
	}
	return 0, 0, 0, errNothingToCompress
}

func (s *Server) compressionCompleter(settings llm.Settings) (chatCompleter, error) {
	settings = settings.Normalized()
	if s.llmCompleter != nil && settings.Endpoint == s.llmSettings.Endpoint && settings.Model == s.llmSettings.Model {
		return s.llmCompleter, nil
	}
	if completer, ok := s.researchLLM.(chatCompleter); ok && settings.Endpoint == s.researchSettings.Endpoint && settings.Model == s.researchSettings.Model {
		return completer, nil
	}
	if completer, ok := s.visionLLM.(chatCompleter); ok && settings.Endpoint == s.visionSettings.Endpoint && settings.Model == s.visionSettings.Model {
		return completer, nil
	}
	return llm.NewClient(settings)
}

func (s *Server) compressContext(ctx context.Context, settings llm.Settings, canonical []llm.Message, checkpoint *sessions.ContextCheckpoint, prefix []llm.Message, tools []llm.Tool, observedBefore int, usageSource string) (compressionResult, error) {
	settings = settings.Normalized()
	head, start, cutoff, err := selectCompressionBoundary(settings, canonical, checkpoint, prefix, tools)
	if err != nil {
		return compressionResult{}, err
	}
	beforeHistory := buildCompressedModelHistory(canonical, checkpoint)
	beforeMessages := append(cloneContextMessages(prefix), beforeHistory...)
	if err := validateContextMessageOrdering(beforeMessages); err != nil {
		return compressionResult{}, fmt.Errorf("validate context before compression: %w", err)
	}
	before := contextRequestTokens(settings, beforeMessages, tools)
	if observedBefore > before {
		before = observedBefore
	} else if observedBefore > 0 && usageSource == "provider" {
		usageSource = "provider+estimated"
	}
	if usageSource == "" {
		usageSource = "estimated"
	}
	retired := cloneContextMessages(canonical[start:cutoff])
	previous := ""
	compressionCount := 0
	if checkpoint != nil {
		previous = strings.TrimSpace(checkpoint.Summary)
		compressionCount = checkpoint.CompressionCount
	}
	chunks := splitCompressionChunks(retired, max(2048, settings.ContextLength/2))
	if len(chunks) == 0 {
		return compressionResult{}, errNothingToCompress
	}
	completer, err := s.compressionCompleter(settings)
	if err != nil {
		return compressionResult{}, err
	}
	var totalUsage llm.Usage
	var usage *llm.Usage
	contextRetry := false
	for index := 0; index < len(chunks); index++ {
		chunk := chunks[index]
		nextSummary, nextUsage, summarizeErr := summarizeCompressionChunk(ctx, completer, settings, previous, chunk)
		err = summarizeErr
		if err != nil {
			if !contextRetry && llm.IsContextLengthExceeded(err) {
				smaller := splitCompressionChunks(chunk, max(1024, estimateMessagesTokens(chunk)/2))
				if len(smaller) > 1 {
					contextRetry = true
					chunks = append(append(append([][]llm.Message(nil), chunks[:index]...), smaller...), chunks[index+1:]...)
					index--
					continue
				}
			}
			return compressionResult{}, err
		}
		previous = nextSummary
		usage = nextUsage
		if usage != nil {
			totalUsage.PromptTokens += usage.PromptTokens
			totalUsage.CompletionTokens += usage.CompletionTokens
			totalUsage.TotalTokens += usage.TotalTokens
		}
	}
	checkpointResult := &sessions.ContextCheckpoint{
		Summary: previous, ProtectedHeadIndex: head, CompactedThrough: cutoff,
		Endpoint: settings.Endpoint, Model: settings.Model, UsageSource: usageSource,
		BeforeTokens: before, CompressionCount: compressionCount + 1,
	}
	afterHistory := buildCompressedModelHistory(canonical, checkpointResult)
	afterMessages := append(cloneContextMessages(prefix), afterHistory...)
	if err := validateContextMessageOrdering(afterMessages); err != nil {
		return compressionResult{}, fmt.Errorf("validate compressed context: %w", err)
	}
	after := contextRequestTokens(settings, afterMessages, tools)
	target := max(1024, compressionThresholdTokens(settings)/2)
	if after > target {
		return compressionResult{}, fmt.Errorf("%w: compressed context is %d tokens, above the %d-token target", errNothingToCompress, after, target)
	}
	minimumReclaim := max(512, before/20)
	if after >= before || before-after < minimumReclaim {
		return compressionResult{}, fmt.Errorf("%w: compression would reclaim only %d tokens", errNothingToCompress, max(0, before-after))
	}
	checkpointResult.AfterTokens = after
	var summaryUsage *llm.Usage
	if totalUsage.PromptTokens > 0 || totalUsage.CompletionTokens > 0 || totalUsage.TotalTokens > 0 {
		summaryUsage = &totalUsage
	}
	return compressionResult{
		Checkpoint: checkpointResult, BeforeTokens: before, AfterTokens: after,
		UsageSource: usageSource, SummaryUsage: summaryUsage, ChunkCount: len(chunks),
		RetiredMessages: cutoff - start,
	}, nil
}

func splitCompressionChunks(messages []llm.Message, maxTokens int) [][]llm.Message {
	if len(messages) == 0 {
		return nil
	}
	boundaries := []int{0}
	for index := 1; index < len(messages); index++ {
		if messages[index].Role == llm.RoleUser {
			boundaries = append(boundaries, index)
		}
	}
	boundaries = append(boundaries, len(messages))
	var chunks [][]llm.Message
	var current []llm.Message
	for index := 0; index+1 < len(boundaries); index++ {
		unit := messages[boundaries[index]:boundaries[index+1]]
		candidate := append(cloneContextMessages(current), unit...)
		if len(current) > 0 && estimateMessagesTokens(candidate) > maxTokens {
			chunks = append(chunks, cloneContextMessages(current))
			current = nil
		}
		current = append(current, cloneContextMessages(unit)...)
	}
	if len(current) > 0 {
		chunks = append(chunks, cloneContextMessages(current))
	}
	return chunks
}

func summarizeCompressionChunk(ctx context.Context, completer chatCompleter, settings llm.Settings, previous string, messages []llm.Message) (string, *llm.Usage, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return "", nil, fmt.Errorf("marshal context for compression: %w", err)
	}
	settings.MaxTokens = min(12000, max(1024, settings.ContextLength*5/100))
	settings.Temperature = 0.2
	var prompt strings.Builder
	prompt.WriteString("Update the structured continuation summary using the archived completed exchanges below. Preserve exact user constraints, file paths, identifiers, commands, test results, and error strings. Do not invent work. Return only the summary with all required headings.\n\n")
	if strings.TrimSpace(previous) != "" {
		prompt.WriteString("Existing rolling summary:\n")
		prompt.WriteString(previous)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("New archived exchanges (JSON):\n")
	prompt.Write(data)
	request, err := llm.NewChatRequest(settings, []llm.Message{
		{Role: llm.RoleSystem, Name: contextSummaryName, Content: contextCompressionSystemPrompt},
		{Role: llm.RoleUser, Content: prompt.String()},
	})
	if err != nil {
		return "", nil, err
	}
	response, err := completer.Complete(ctx, request)
	if err != nil {
		return "", nil, fmt.Errorf("generate context summary: %w", err)
	}
	if len(response.Choices) == 0 {
		return "", response.Usage, errors.New("generate context summary: endpoint returned no choices")
	}
	summary := strings.TrimSpace(response.Choices[0].Message.Content)
	if summary == "" {
		return "", response.Usage, errors.New("generate context summary: endpoint returned an empty summary")
	}
	return summary, response.Usage, nil
}

const contextCompressionSystemPrompt = `You maintain continuation state for a long-running coding agent. Produce a compact, faithful Markdown summary with exactly these headings:
## Goal
## Constraints & Preferences
## Progress
### Done
### In Progress
### Blocked
## Key Decisions
## Relevant Files & Artifacts
## Commands, Tests & Errors
## Next Steps
## Critical Exact Context

Prefer exact paths, symbols, IDs, values, commands, test outcomes, and error text over narrative. Preserve unresolved user instructions. Fold new facts into the existing state, remove superseded detail, and never claim work was completed unless the exchanges establish it.`

func contextHistorySearchToolSchema() llm.Tool {
	return llm.Tool{Type: "function", Function: llm.ToolFunction{
		Name:        contextHistorySearchToolName,
		Description: "Search exact user, assistant, and tool content archived by context compression. Use this when the continuation summary lacks an exact older path, command, error, identifier, decision, or instruction.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Case-insensitive phrase or space-separated terms to find."},
				"roles": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{llm.RoleUser, llm.RoleAssistant, llm.RoleTool}}},
				"tools": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional tool-name filter."},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "default": 5},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}}
}

type contextHistorySearchArgs struct {
	Query string   `json:"query"`
	Roles []string `json:"roles"`
	Tools []string `json:"tools"`
	Limit int      `json:"limit"`
}

func (s *chatSession) executeContextHistorySearch(canonical []llm.Message, checkpoint *sessions.ContextCheckpoint, arguments json.RawMessage) tools.ExecutionResult {
	fail := func(code, message string) tools.ExecutionResult {
		return tools.ExecutionResult{Tool: contextHistorySearchToolName, Error: &tools.ExecutionError{Code: code, Message: message}}
	}
	if checkpoint == nil || checkpoint.CompactedThrough <= checkpoint.ProtectedHeadIndex+1 || checkpoint.CompactedThrough > len(canonical) {
		return fail("history_unavailable", "no compacted conversation history is available")
	}
	var args contextHistorySearchArgs
	if err := json.Unmarshal(arguments, &args); err != nil {
		return fail("invalid_arguments", err.Error())
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return fail("invalid_query", "query is required")
	}
	if args.Limit == 0 {
		args.Limit = 5
	}
	args.Limit = min(10, max(1, args.Limit))
	roleFilter := make(map[string]bool, len(args.Roles))
	for _, role := range args.Roles {
		roleFilter[strings.ToLower(strings.TrimSpace(role))] = true
	}
	toolFilter := make(map[string]bool, len(args.Tools))
	for _, name := range args.Tools {
		toolFilter[strings.ToLower(strings.TrimSpace(name))] = true
	}
	terms := strings.Fields(strings.ToLower(args.Query))
	type archivedToolCall struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments,omitempty"`
	}
	type match struct {
		MessageIndex int                `json:"messageIndex"`
		Role         string             `json:"role"`
		Tool         string             `json:"tool,omitempty"`
		ToolCallID   string             `json:"toolCallId,omitempty"`
		Excerpt      string             `json:"excerpt"`
		ToolCalls    []archivedToolCall `json:"toolCalls,omitempty"`
	}
	callDetails := map[string]archivedToolCall{}
	for _, message := range canonical[:checkpoint.CompactedThrough] {
		for _, call := range message.ToolCalls {
			callDetails[call.ID] = archivedToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments}
		}
	}
	matches := make([]match, 0, args.Limit)
	for index := checkpoint.CompactedThrough - 1; index > checkpoint.ProtectedHeadIndex && len(matches) < args.Limit; index-- {
		message := canonical[index]
		if len(roleFilter) > 0 && !roleFilter[strings.ToLower(message.Role)] {
			continue
		}
		associatedCall, hasAssociatedCall := callDetails[message.ToolCallID]
		toolName := associatedCall.Name
		calledTools := make([]archivedToolCall, 0, len(message.ToolCalls)+1)
		searchableToolText := make([]string, 0, len(message.ToolCalls)+1)
		if hasAssociatedCall {
			calledTools = append(calledTools, associatedCall)
			searchableToolText = append(searchableToolText, associatedCall.Name+" "+associatedCall.Arguments)
		}
		for _, call := range message.ToolCalls {
			detail := archivedToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments}
			calledTools = append(calledTools, detail)
			searchableToolText = append(searchableToolText, detail.Name+" "+detail.Arguments)
			if toolName == "" {
				toolName = call.Function.Name
			}
		}
		if len(toolFilter) > 0 {
			matchedTool := toolFilter[strings.ToLower(toolName)]
			for _, call := range calledTools {
				matchedTool = matchedTool || toolFilter[strings.ToLower(call.Name)]
			}
			if !matchedTool {
				continue
			}
		}
		haystack := strings.ToLower(message.Content + " " + message.Name + " " + message.ToolCallID + " " + strings.Join(searchableToolText, " "))
		matched := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		excerpt := message.Content
		if strings.TrimSpace(excerpt) == "" && len(searchableToolText) > 0 {
			excerpt = "Tool calls: " + strings.Join(searchableToolText, "; ")
		}
		matches = append(matches, match{
			MessageIndex: index, Role: message.Role, Tool: toolName, ToolCallID: message.ToolCallID,
			Excerpt: truncateContextExcerpt(excerpt, 1600), ToolCalls: calledTools,
		})
	}
	return tools.ExecutionResult{Tool: contextHistorySearchToolName, Success: true, Output: map[string]any{
		"query": args.Query, "matches": matches, "count": len(matches),
		"searchedMessageCount": checkpoint.CompactedThrough - checkpoint.ProtectedHeadIndex - 1,
	}}
}

func truncateContextExcerpt(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}
