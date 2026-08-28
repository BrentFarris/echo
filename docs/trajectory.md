# Trajectory view

Echo records each chat as an append-only event stream in addition to the resumable transcript snapshot. In Main Chat, use the **Chat / Trajectory** switcher at the top of a conversation or right-click a visible chat tab and choose **Open trajectory**.

The Trajectory view provides:

- a duration-scaled timing overview split into Input, Model, Tools, and System lanes, plus an expanded-by-default Research group with one concurrent swimlane per child agent;
- turn-aligned paging, source filters, and full-log server-side search;
- live updates while the selected chat is running;
- virtualized event rows and a Summary, Payload, Result, Schema, and Timing inspector;
- an optional raw-chunk view and raw JSONL export.

The overview is a compact activity projection rather than a wall-clock chart. Idle time between recorded operations is collapsed, turn changes remain visible as vertical boundaries, and paired lifecycle records (model request/response and tool call/result) render as one interval. The header, overview, search, filters, and status occupy a fixed control region while the ledger and inspector scroll independently below it. Selecting an interval scrolls its corresponding ledger row into view and exposes its start payload, completion result, and combined timing in the inspector. Turn start/end records remain available in the ledger but are not shown as System activity.

Research jobs retain the same projected time axis as their parent turn, so overlapping agents remain visibly concurrent. Each agent lane contains a subdued whole-job band with its model rounds, tool calls, context compression, status changes, and report delivery layered above it. The Research group can be collapsed to one aggregate lane for the current view. Historical logs that contain research orchestration tools but predate agent-level capture show an informational partial-history notice rather than inventing missing events.

## Storage format

Each chat has one versioned log:

```text
<workspace>/.echo/trajectories/<chat-id>.jsonl
```

The first line is a format header. Every later line is an immutable event with a monotonically increasing `sequence`, UTC `timestamp`, event `type`, optional `turnId` and assistant `step`, and a type-specific `data` payload. A torn final line is ignored on read and repaired before the next append. Earlier malformed records are treated as corruption instead of being silently skipped.

Echo records these semantic boundaries:

- `turn/start`, `user/message`, and `turn/end`;
- `context/injection` records identifying agent-mode, editor, and conversation-history context by source;
- `context/compression_queued`, `context/compression_start`, `context/compression_complete`, `context/compression_skipped`, and `context/compression_error`, including the trigger, phase, endpoint/model, token metrics, duration, recovery status, and classified failure details;
- `request/start` with the exact serialized `llm.ChatRequest` sent to the provider;
- batched `assistant/chunk` records containing every raw provider stream event and its receive time;
- `assistant/message` with assembled content, reasoning, tool calls, finish reason, token usage, total duration, and time to first token;
- `tool/call` and `tool/result`, including arguments, results, success, and duration;
- `research/agent_created`, `research/job_queued`, `research/job_start`, `research/job_end`, and deduplicated `research/status` transitions;
- `research/request_start`, batched `research/chunk`, and `research/assistant_message` records containing the exact child request, every provider event/raw frame, assembled response and reasoning, tool calls, usage, finish/error state, duration, and time to first token;
- `research/tool_call` and `research/tool_result` with exact child arguments/results and timing, plus `research/report_delivered` when a bounded report is handed to the parent model;
- transcript rewind, edit, and delete mutations;
- auxiliary skill-synthesis model requests and results.

The request type does not contain endpoint credentials or custom HTTP headers, so those values never enter the trajectory. Parent and research-agent requests can still contain system/user prompts, attached media data, workspace context, reasoning, raw provider frames, tool results, and other sensitive conversation material. Research transcripts remain private from the parent model except for explicitly delivered bounded reports; their full audit data is available to the user through Trajectory inspection, search, and export. Generated context summaries are retained only in the Trajectory compression-completion payload and the private context checkpoint; normal chat activity exposes metrics only. Treat trajectory files as private workspace data.

Parent and research-agent provider chunks remain grouped into JSONL records of up to 16 events. Those records are buffered and appended with one file write at semantic stream boundaries (reasoning, visible content, and tool calls), on completion/error/cancellation, or when the buffer reaches 1 MiB or 15 seconds. This bounds crash-loss and memory use without repeatedly opening the log throughout a long thinking phase. Each original `StreamEvent`, raw provider JSON frame, and receive timestamp remains present in the batch, and every JSONL event is still published individually to live Trajectory viewers after persistence.

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
