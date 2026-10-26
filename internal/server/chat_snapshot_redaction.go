package server

import (
	"unicode/utf8"

	"github.com/brent/echo/internal/sessions"
)

// Snapshot redaction projects the durable transcript into a metadata-only wire
// view for session_snapshot payloads. Heavy content — full tool results,
// reasoning text, and base64 media payloads — is stripped so clients only
// download what they actually render or expand. Live streaming events keep
// their full payloads; only snapshots (restores, reconnects, tab switches)
// are redacted. Redaction always works on copies and never mutates the live
// transcript, and the truncated flags it sets are wire-only (never persisted).

const (
	// snapshotToolResultPreviewBytes caps tool_result strings in snapshots,
	// mirroring the research-agent result cap.
	snapshotToolResultPreviewBytes = 2048
	// snapshotResearchReasoningPreviewBytes keeps a short head of research
	// reasoning so the "Show work" disclosure still renders attributed
	// entries; the full text is fetchable per agent.
	snapshotResearchReasoningPreviewBytes = 2048
)

// redactTurnsForSnapshot returns redacted copies of the given turns.
func redactTurnsForSnapshot(turns []sessions.Turn) []sessions.Turn {
	if len(turns) == 0 {
		return turns
	}
	redacted := make([]sessions.Turn, len(turns))
	for index := range turns {
		redacted[index] = redactTurnForSnapshot(turns[index])
	}
	return redacted
}

// redactActiveTurnForSnapshot returns a redacted copy of the active turn, or
// nil when there is none. The copy is essential: the active turn is a pointer
// into live session state and must never be serialized (or redacted) in place.
func redactActiveTurnForSnapshot(active *sessions.Turn) *sessions.Turn {
	if active == nil {
		return nil
	}
	redacted := redactTurnForSnapshot(*active)
	return &redacted
}

// redactTurnForSnapshot returns a redacted copy of one turn.
func redactTurnForSnapshot(turn sessions.Turn) sessions.Turn {
	turn.Images = redactMediaAttachments(turn.Images)
	turn.Videos = redactMediaAttachments(turn.Videos)
	if len(turn.GoalSteering) > 0 {
		steering := make([]sessions.GoalSteering, len(turn.GoalSteering))
		for index := range turn.GoalSteering {
			item := turn.GoalSteering[index]
			item.Images = redactMediaAttachments(item.Images)
			item.Videos = redactMediaAttachments(item.Videos)
			steering[index] = item
		}
		turn.GoalSteering = steering
	}
	if len(turn.AssistantTurns) > 0 {
		assistants := make([]sessions.AssistantTurn, len(turn.AssistantTurns))
		for index := range turn.AssistantTurns {
			assistants[index] = redactAssistantTurnForSnapshot(turn.AssistantTurns[index])
		}
		turn.AssistantTurns = assistants
	}
	turn.ResearchReasoning = redactResearchReasoningForSnapshot(turn.ResearchReasoning)
	turn.ResearchTools = redactToolActivitiesForSnapshot(turn.ResearchTools)
	return turn
}

func redactAssistantTurnForSnapshot(assistant sessions.AssistantTurn) sessions.AssistantTurn {
	if assistant.Reasoning != "" {
		assistant.Reasoning = ""
		assistant.ReasoningTruncated = true
	}
	assistant.Tools = redactToolActivitiesForSnapshot(assistant.Tools)
	assistant.Images = redactMediaAttachments(assistant.Images)
	assistant.Videos = redactMediaAttachments(assistant.Videos)
	return assistant
}

func redactToolActivitiesForSnapshot(tools []sessions.ToolActivity) []sessions.ToolActivity {
	if len(tools) == 0 {
		return tools
	}
	redacted := make([]sessions.ToolActivity, len(tools))
	for index := range tools {
		tool := tools[index]
		if len(tool.Result) > snapshotToolResultPreviewBytes {
			tool.Result = truncateUTF8Prefix(tool.Result, snapshotToolResultPreviewBytes)
			tool.ResultTruncated = true
		}
		redacted[index] = tool
	}
	return redacted
}

func redactResearchReasoningForSnapshot(reasoning []sessions.ResearchReasoning) []sessions.ResearchReasoning {
	if len(reasoning) == 0 {
		return reasoning
	}
	redacted := make([]sessions.ResearchReasoning, len(reasoning))
	for index := range reasoning {
		item := reasoning[index]
		if len(item.Reasoning) > snapshotResearchReasoningPreviewBytes {
			item.Reasoning = truncateUTF8Prefix(item.Reasoning, snapshotResearchReasoningPreviewBytes)
			item.Truncated = true
		}
		redacted[index] = item
	}
	return redacted
}

// redactMediaAttachments strips base64 payloads while keeping the metadata
// clients need to fetch the bytes on demand. The input slice is left
// untouched; the result is always a fresh slice.
func redactMediaAttachments(attachments []sessions.MediaAttachment) []sessions.MediaAttachment {
	if len(attachments) == 0 {
		return attachments
	}
	redacted := make([]sessions.MediaAttachment, len(attachments))
	for index := range attachments {
		attachment := attachments[index]
		attachment.DataURL = ""
		redacted[index] = attachment
	}
	return redacted
}

// truncateUTF8Prefix cuts value to at most limit bytes without splitting a
// rune.
func truncateUTF8Prefix(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
