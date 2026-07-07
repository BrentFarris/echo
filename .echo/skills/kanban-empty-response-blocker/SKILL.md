---
name: kanban-empty-response-blocker
description: Root cause, fix, and regression test for Kanban agent blocking when LLM returns empty response without tool calls (B-021)
triggers:
    - kanban blocked
    - empty assistant message
    - 400 Bad Request tool_calls
    - agent error
    - B-021
    - empty kanban turn
---

# Kanban Agent Empty Response Bug (B-021)

## Symptom

Kanban card blocks with error: `llm endpoint returned 400 Bad Request: "Assistant message must contain either 'content' or 'tool_calls'"`

The "Agent continuing" progress entry may appear just before the block, indicating the no-tool continuation path was reached.

## Root Cause

In `kanban_scheduler.go`, `runKanbanAgent` unconditionally appends the assistant response to the message history after streaming completes:

```go
assistantMessage := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
messages = append(messages, assistantMessage)
```

When the model returns a turn with empty `content` AND no `tool_calls`, this creates an invalid assistant message. The OpenAI-compatible API spec requires assistant messages to have at least one of `content` or `tool_calls`. On the next request cycle, the API rejects the full message array with 400.

## Fix Applied

File: `internal/services/kanban_scheduler.go`

An early-guard check was inserted **before** the assistant message is constructed and appended. When both content (trimmed) and tool calls are empty, the loop continues immediately — retrying the stream without polluting history:

```go
if len(toolCalls) == 0 && strings.TrimSpace(content) == "" {
    continue
}

assistantMessage := llm.Message{Role: llm.RoleAssistant, Content: content, ToolCalls: toolCalls}
messages = append(messages, assistantMessage)
```

This is preferred over conditionally skipping the append because:
1. No empty message enters the conversation history at any point
2. The no-tool continuation logic (`shouldContinueKanbanNoToolTurn`) never sees the degenerate case
3. Variables like `noToolContinuationAttempts` are unaffected since they're reset on successful tool-call turns

## Why Models Return Empty Responses

Some LLM endpoints/models return empty turns when:
- They get confused by complex tool-call contexts
- The continuation prompt triggers a degenerate response
- The model's internal state is inconsistent with the conversation history

## Regression Test

`TestKanbanSchedulerSkipsEmptyAssistantTurn` in `kanban_scheduler_test.go`:
- Mock server returns an empty SSE turn (no content, no tool calls, finish_reason "stop") on the first request
- Second request returns a valid completion
- Verifies the card reaches Done (not Blocked), confirming the empty turn was skipped silently and the agent continued normally

## Verification

Run `go test ./internal/services/...` — includes the new regression test alongside existing scheduler tests.
