# Trajectory view

Echo records each chat as an append-only event stream in addition to the resumable transcript snapshot. In Main Chat, use the **Chat / Trajectory** switcher at the top of a conversation or right-click a visible chat tab and choose **Open trajectory**.

The Trajectory view provides:

- a duration-scaled timing overview split into Input, Model, Tools, and System lanes;
- turn-aligned paging, source filters, and full-log server-side search;
- live updates while the selected chat is running;
- virtualized event rows and a Summary, Payload, Result, Schema, and Timing inspector;
- an optional raw-chunk view and raw JSONL export.

The overview is a compact activity projection rather than a wall-clock chart. Idle time between recorded operations is collapsed, turn changes remain visible as vertical boundaries, and paired lifecycle records (model request/response and tool call/result) render as one interval. The header, overview, search, filters, and status occupy a fixed control region while the ledger and inspector scroll independently below it. Selecting an interval scrolls its corresponding ledger row into view and exposes its start payload, completion result, and combined timing in the inspector. Turn start/end records remain available in the ledger but are not shown as System activity.

## Storage format

Each chat has one versioned log:

```text
<workspace>/.echo/trajectories/<chat-id>.jsonl
```

The first line is a format header. Every later line is an immutable event with a monotonically increasing `sequence`, UTC `timestamp`, event `type`, optional `turnId` and assistant `step`, and a type-specific `data` payload. A torn final line is ignored on read and repaired before the next append. Earlier malformed records are treated as corruption instead of being silently skipped.

Echo records these semantic boundaries:

- `turn/start`, `user/message`, and `turn/end`;
- `context/injection` records identifying agent-mode, editor, and conversation-history context by source;
- `request/start` with the exact serialized `llm.ChatRequest` sent to the provider;
- batched `assistant/chunk` records containing every raw provider stream event and its receive time;
- `assistant/message` with assembled content, reasoning, tool calls, finish reason, token usage, total duration, and time to first token;
- `tool/call` and `tool/result`, including arguments, results, success, and duration;
- transcript rewind, edit, and delete mutations;
- auxiliary skill-synthesis model requests and results.

The request type does not contain endpoint credentials or custom HTTP headers, so those values never enter the trajectory. Requests can still contain prompts, attached media data, workspace context, tool results, and other sensitive conversation material. Treat trajectory files as private workspace data.

Provider chunks are batched to avoid opening the log once per token. Each original `StreamEvent`, raw provider JSON frame, and receive timestamp remains present in the batch.

## Legacy chats and lifecycle

When Echo opens a pre-Trajectory chat, it creates one `legacy/import` event from the saved transcript. The UI labels this as a partial reconstruction: historical system prompts, raw chunks, exact timing, and context injections were not available in the old snapshot.

Clearing a chat or closing its tab deletes the corresponding JSONL file. Normal transcript persistence is not failed or rolled back when trajectory logging alone encounters an error; subscribed clients receive a `trajectory_error` warning and the viewer marks the audit stream as potentially incomplete.

There is no retention, redaction, size cap, or compaction policy in the first release. Long tool-heavy conversations can produce large logs.

## HTTP and WebSocket interfaces

Authenticated clients use these Main Chat endpoints:

```text
GET /api/workspaces/{workspaceId}/chats/{chatId}/trajectory?beforeSeq=&turnLimit=20
GET /api/workspaces/{workspaceId}/chats/{chatId}/trajectory/search?q=&beforeSeq=&limit=100
GET /api/workspaces/{workspaceId}/chats/{chatId}/trajectory/export
```

Pages are aligned to turns, default to 20 turns, and cap at 100 turns. Search returns at most 100 matching events per request. Export downloads the original JSONL.

Live subscribers receive `trajectory_event` and `trajectory_error` WebSocket messages on the same workspace chat subscription used by the transcript.

## Current limitations

- The visual Trajectory view is available in Main Chat. Code Chat is recorded by the backend but does not yet expose the viewer.
- The first release is inspection-only. It does not resume, fork, replay, or mutate a chat from the event stream.
- Search returns matching raw records rather than reconstructed surrounding turns.
