# Echo architecture and API v1

`internal/domain` defines Bot, Chat, Message, Run, ToolCall, ExecutionScope, PluginManifest and Event. The browser equivalents are in `web/src/types.ts`. Stable bot IDs are separate from unique case-insensitive handles. Messages persist structured text, mention and file-reference segments, recipients and historical author labels. Every event has a monotonic sequence and optional chat/run/bot IDs.

The Echo name is independent of storage identifiers. The default `%LOCALAPPDATA%\Echo2` profile, `echo2.sqlite` database, `Echo2/` credential targets, session cookie, saved window-layout keys and sandbox identities remain compatible with existing installations. Rebranding does not copy, reset or migrate user data. The `ECHO2_*` test environment switches also remain supported.

## Boundaries

- `store`: SQLite (modernc), WAL, compressed individual messages/chunks, transactional acceptance, retry deduplication, crash recovery and incremental reclamation. A data-directory lock prevents two servers performing recovery against each other. Streaming chunks flush every two seconds or 64 KiB and at completion/shutdown. Neither model streaming nor shell streaming rewrites the transcript on each token.
- `model`: compatible HTTP/SSE transport, fragmented tool calls, reasoning, connection deadlines, credential redaction and local-destination enforcement. Context summaries are model input only; `chat_search` can retrieve the original transcript.
- `coordinator`: fixed dispatch participants, deduplicated correlations, explicit send/reply/wait, circular-wait detection, failed-recipient notification, cancellation and a 32-request ceiling. Public responses never implicitly wake another bot.
- `broker`: root-relative file operations, bounded checked HTTP, and native Windows process isolation. Scope changes block new dispatches while existing runs terminate. ACL cleanup targets only the unique Echo SID. Required runtime/toolchain access is read-only; workspaces and private temporary/cache directories are writable.
- `plugins`: wazero runtime with 64 MiB memory, a five-minute invocation deadline, cancellation, bounded output, immutable package digests, capability validation and previous-package rollback. Each invocation is a fresh WASI module. Built-in tools enter the same guest/host protocol. Native privilege remains in explicitly checked host operations.
- `calendar`: durable schedules and stable occurrence identities, server-side ticking, local-time recurrence and persisted missed-work reviews. Selection/skipping of a missed batch is transactional.
- `server`: owner authentication, API, WebSocket subscriptions, per-bot model loops and host-operation dispatch. Each bot has its own model history. A scheduled prompt cannot add recipients by embedding a mention.
- `web`: independent Vite/TypeScript UI using Echo's design tokens and card/composer concepts. No imports from legacy modules. No editor, terminal dock, source-control pane, modes or desktop automation are included.

## HTTP surface

All paths below are under `/api/v1`. Local bootstrap requires a loopback connection and a local Host; remote bootstrap requires the configured password. Mutating browser requests reject foreign or opaque Origins.

| Route | Behavior |
|---|---|
| `POST /session` | Local owner bootstrap or remote password login; HttpOnly session cookie |
| `GET /state` | Bots, all chat metadata, model connections, plugins, schedules, occurrences and runs |
| `POST /bots`, `DELETE /bots/{id}` | Bot management; Echo is permanent |
| `POST /endpoints`, `DELETE /endpoints/{id}`, `GET /endpoints/{id}/models` | Compatible model connections and discovery |
| `POST /chats`, `DELETE /chats/{id}` | Create/delete chats |
| `POST /chats/{id}/messages` | `{content, segments, generation, requestId}`; validates all recipients before accepting |
| `POST /chats/{id}/stop` | Cancel the entire dispatch |
| `GET /chats/{id}/messages` | Complete stored transcript plus active stream buffers |
| `GET /chats/{id}/snapshot` | Transcript plus sequence watermark for reconnect recovery |
| `POST /references`, `POST /references/validate` | Lazy file suggestions and recipient-specific revalidation |
| `PATCH /plugins/{id}` | Owner settings and activation |
| `POST /plugins/{id}/approve` | Approve `{digest}`; stale package reviews are rejected |
| `POST /plugins/{id}/rollback` | Restore the previous package |
| `GET /plugins/{id}/views/{view}`, `POST /plugins/{id}/bridge` | View source and restricted, namespaced UI storage |
| `POST /hosting/password` | Replace password and invalidate previous sessions |
| `POST /schedules`, `DELETE /schedules/{id}`, `POST /missed/review` | Calendar management and explicit catch-up selection |
| `GET /events` | Authenticated WebSocket; reset/snapshot recovery, message/delta/run/handoff/plugin-open/changed events |

Clients buffer incoming events during recovery, apply a snapshot, then replay events newer than its watermark. Slow subscribers disconnect and recover from persisted state and active buffers. The event log itself is not written per token.

## Plugin ABI and package sources

Manifests declare API version `1`, ID/version/scope, capabilities, tools with argument schemas, model-provider/renderer identifiers, views and settings. Compatible HTTP model connections and Markdown View are the built-in provider/renderer implementations. Backend source templates and UI bridge examples live in `internal/plugins/templates` and are also available through `plugin_template` in chat.

Custom providers name their backend entry tool in `providers`. Select that provider in the connection editor. Echo validates availability for every recipient and supplies `{messages,tools,generation}`; providers return `{content,reasoning,toolCalls,finishReason}`. `model.complete` delegates to the compatible streaming client. Other protocols use `model.request` (bounded GET/POST with an endpoint-relative path and host-injected credential) and `model.emit` (content/reasoning deltas). These capabilities exist only inside the selected provider invocation, are pinned to that connection, and require owner approval. Provider entries are excluded from the model's ordinary tools. Model discovery remains the optional compatible `/models` request; manual identifiers work for other protocols.

Custom renderers name a `kind: "renderer"` view in `renderers`. An isolated frame sends `renderer-ready`, receives `init` with nonce/theme, and answers `render` with `{type:"rendered",nonce,id,html}`. Echo accepts only outstanding requests owned by that frame, only for assistant messages, and sanitizes output. Enabled custom renderers take precedence over Markdown, ordered by plugin ID. Bot toggles apply before sending text. With every renderer disabled, core output stays plain text. The `provider` and `renderer` authoring templates demonstrate both contracts.

A backend receives one JSON object on stdin: `{tool, method, params}`. It writes exactly one `{result: ...}` or `{error: "..."}` object to stdout. The `echo.call(ptr, length, out, capacity)` WASM import brokers privileged operations. A zero-capacity call executes the request and returns the output size; the second call copies that response. Tools may implement pure computation without capabilities.

Broker methods include `file.read/write/list/search/delete/move/mkdir`, `web.get`, `process.run`, and `storage.get/set/delete`; the built-in packages additionally use their management/calendar operations. Files/processes use the invoking bot's effective scope. Storage is keyed by plugin ID and bot ID, with 1 MiB values and an 8 MiB namespace quota. A plugin cannot select another namespace. Model credentials are host-owned Windows credentials and never enter WASI or iframe environments.

UI views run with `sandbox="allow-scripts"` and a CSP denying network, framing, forms and external scripts. A view sends `echo-plugin-ready`; Echo returns `echo-plugin-init` with a nonce, identity and theme. Requests include `{type:"echo-plugin-request", nonce, pluginId, viewId, id, method, params}`. Only `window.close` and declared namespaced storage are bridged. Responses echo `id` and the nonce. Echo alone owns geometry and persists window placement.
