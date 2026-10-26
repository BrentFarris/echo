---
name: metadata-only-snapshots
description: 'How Echo reduces mobile wire data: session snapshots are metadata-only (truncated tool results, omitted reasoning, payload-free media) with fetch-on-expand REST endpoints and 384px image previews; live streaming events stay full.'
triggers:
    - session_snapshot
    - dataUrl
    - metadata-only
    - on-demand
    - attachment endpoint
    - resultTruncated
    - reasoningTruncated
    - preview resize
    - mobile data
---

# Metadata-only chat snapshots + on-demand content endpoints

Session snapshots (`session_snapshot`) are **metadata-only**. Live streaming events (`tool_result`, `reasoning`, `turn_started`, goal steering) keep full payloads. Redaction happens only in `sendSnapshot` via `internal/server/chat_snapshot_redaction.go` and never mutates the live transcript (always works on copies — beware shared slice backing arrays).

## Wire contract
- `sessions.ToolActivity.Result` → 2 KB head preview + `resultTruncated: true` (wire-only flag on the persisted struct, never saved to disk).
- `sessions.AssistantTurn.Reasoning` → omitted + `reasoningTruncated: true`.
- `sessions.ResearchReasoning.Reasoning` → 2 KB preview + `Truncated: true` (pre-existing flag).
- All `MediaAttachment.DataURL` (turn images/videos, assistant media, goal steering, `goalSnapshot.PendingInputs`) → stripped; `ID/Name/MediaType/Bytes` kept.

## On-demand endpoints (internal/server/chat_attachments_api.go)
- `GET /api/workspaces/{id}/chats/{chatId}/attachments/{attachmentId}?width=N` — raw bytes; `width` downscales PNG/JPEG via `imaging.Fit` (Lanczos, JPEG q85). GIF (animation), WebP (no encoder), SVG etc. pass through. Small bounded in-memory preview cache. `Cache-Control: private, max-age=86400` (attachments are immutable, unlike fs/media's no-store).
- `GET .../chats/{chatId}/tool-results/{callId}` — full result via JSON envelope `{content}`.
- `GET .../chats/{chatId}/reasoning/{turnId}?assistant=N` or `?agentId=X` — full reasoning via `{reasoning}`.
- Chat lookup: `s.resolveChatTranscript` tries main surface then code chat; scan `transcript.Turns`, `session.active`, and `transcript.Goals[].PendingSteering`.

## Frontend (web/js/chat.js)
- `attachmentSrc(attachment, {full})` — returns `dataUrl` when present (live events/composer drafts), else the attachment endpoint URL; previews use `?width=384` (`mediaPreviewWidth`).
- `buildMediaFigure` uses preview src for `<img>`, full src for `<video>`; `makeImageZoomable(media, { fullSrc })` opens the lightbox at full size (`imageLightbox.ts` `fullSrc` option).
- `installToolResultExpansion` / `fetchFullToolResult` — fetch-on-first-expand for `resultTruncated` tool calls, cached on the item.
- `appendReasoningPlaceholder` / `fetchFullReasoning` — deferred "Thinking" disclosure for `reasoningTruncated` assistant turns.
- `installResearchReasoningExpansion` / `fetchResearchReasoning` — expand fetches full agent thinking (also applies to the live 128 KB cap).

## Testing
- `chat_snapshot_redaction_test.go` — unit redaction test + WS-flow endpoint tests; `chat_tool_media_transport_test.go` / `chat_tool_video_transport_test.go` assert metadata-only snapshots plus endpoint fetch-back.
- Persistence is unchanged: base64 stays in `.echo/chat-workspace.json` (sidecar media files remain a documented non-goal in docs/media_transport_contract.md).
