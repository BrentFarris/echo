---
name: comfyui-integration
description: 'ComfyUI integration: workflow resolution, template substitution (incl. duration-driven MiniMax H3 variables), txt2img/video tools, client API, flat API prompt format requirement, output collection with extension routing + media-type tracking, /view type matching, seed randomization bounds, cold-start timing overhead, and history/ffprobe verification procedures.'
triggers:
    - comfyui
    - comfyui template substitution
    - img2img workflow
    - FLUX workflow
    - comfyui video workflow
    - minimax
    - flat API JSON
    - inline video rendering
    - /view type parameter
    - duration aspectRatio megapixels
    - DURATION template
    - choppy video frame count mod 17
---

## ComfyUI Integration Architecture

### Package Structure
- `internal/comfyui/workflow.go` — Workflow loading, template substitution, default builder
- `internal/comfyui/client.go` — HTTP client for `/prompt`, `/history`, `/view`, `/upload/image`; execution error detection, output collection (tracks media types)
- `internal/comfyui/queue.go` — Polling (`WaitForCompletionPoll`) and WebSocket waiting; `FetchImageBytes`, `FetchVideoBytes` (shared `fetchMediaBytes` with 4xx retry backoff)
- `internal/tools/comfyui_generate.go` — Image tool registration, imagePath upload pipeline, inline image fetching via `LLMImageContentProvider`
- `internal/tools/comfyui_generate_video.go` — Video tool registration, video output collection, inline video fetching via `LLMVideoContentProvider`

**No bundled workflows.** Users manage their own workflow JSON files on disk, configured via settings. User workflows conventionally live in `.comfy/workflows/`.

### Workflow Format: Flat API Prompt JSON (Not Frontend DOM)
Workflow JSON files must be in the **flat API prompt format** that ComfyUI's `/prompt` endpoint accepts — a simple map of string node IDs to objects with `class_type` and `inputs`. **Not** the ComfyUI frontend DOM format.

Reference example: `.comfy/workflows/flux_schnell_dual_prompt.json` shows the correct flat format.

### Workflow Settings (Separate txt2img / img2img / video)
Settings (`internal/llm/settings.go`) expose three independent workflow path fields:
- `ComfyuiTxt2imgWorkflow string` — absolute path to a txt2img workflow JSON
- `ComfyuiImg2imgWorkflow string` — absolute path to an img2img workflow JSON
- `ComfyuiVideoWorkflow string` — absolute path to a video generation workflow JSON

All are trimmed in `Normalized()`, cloned in `Clone()`. These paths are wired into `ExecutionContext`.

### Workflow Resolution Priority
1. `workflowJSON` parameter → parse inline JSON directly via `ParseWorkflowJSON()`
2. `workflowPath` parameter → resolve via `resolveWorkspaceChildPath()`, then `LoadWorkflowJSON()` (path must be labeled, e.g. `echo/.comfy/workflows/x.json`)
3. Neither explicit workflow given → select based on context (image tool uses txt2img/img2img; video tool uses ComfyuiVideoWorkflow)
4. If empty, image tool falls back to `BuildDefaultWorkflow(params)`, video tool returns error `missing_video_workflow`

### Template Variable System
TemplateParams struct holds substitutable values using `{{VAR}}` syntax:
- Core: `{{PROMPT}}`, `{{NEGATIVE_PROMPT}}`, `{{MODEL}}`, `{{IMAGE}}`, `{{WIDTH}}`, `{{HEIGHT}}`, `{{STEPS}}`, `{{CFG_SCALE}}`, `{{SEED}}`, `{{FRAMES}}` (default 16), `{{FPS}}` (default **8.0** — a source of choppy video if a workflow relies on it), `{{FORMAT}}`
- Duration-driven video: `{{DURATION}}` (seconds, default 5), `{{ASPECT_RATIO}}` (default "16:9 (Widescreen)"), `{{MEGAPIXELS}}` (default 0.4). Exposed as `comfyui_generate_video` args `duration`, `aspectRatio`, `megapixels`. Use these — not frames/fps — when the workflow computes frame count from duration and resolves resolution via a ResolutionSelector node.

Template variables in JSON **must be quoted strings** (`"{{WIDTH}}"`), never bare values. Hardcode model-specific names (VAE, CLIP) directly in the workflow.

**`SubstituteTemplateVariables` mutates the workflow map in place.** Tests must build a fresh workflow per call; production parses a fresh one per tool invocation so this is only a test trap.

### Seed Handling
Negative seed → randomized via `rand.Int63n(2^53)` (bounded so the value survives JSON float64 round-tripping). Explicit positive seeds pass through unchanged. (Historic bug: negative seeds mapped to constant 847291053, making every "random" run identical.)

### Output Collection (GetHistory)
Two-pass approach to handle varying node output conventions:

**Pass 1 — "images" key with extension routing:** Some SaveVideo nodes emit under `"images"` instead of a dedicated video key. The code checks file extensions (`mp4`, `webm`, `avi`, `mov`, `mkv`, `gif`) to route video files to `OutputVideos` and everything else to `OutputImages`.

**Pass 2 — Dedicated video keys:** Collects from `["videos", "video", "gifs", "animated_gifs"]`. Accepts entries where `typ == "output"`, `"custom"`, or `""`.

**Both passes record the reported storage type into `GenerateResult.MediaTypes` (path → "output"/"temp"/"custom").** This is critical: ComfyUI's `/view` endpoint only finds files under the directory matching their reported `type`, and custom extensions like MiniMax H3's Save Video report `"custom"` for mp4 — guessing by extension will get HTTP 400.

### Fetching Media via `/view`
**The `type` query parameter must match exactly what was stored in history.** Use the type from `GenerateResult.MediaTypes[path]`; only fall back to guessing (`.gif` → custom, else output) when the map has no entry.

`fetchMediaBytes` retries 4xx/5xx responses up to 6 times with 500ms×attempt backoff to absorb ComfyUI's write-visibility race (history marked complete before file flushes).

### Completion Detection
Both `WaitForCompletionPoll` and the WebSocket path treat a prompt complete when history shows **images OR videos** output — video-only workflows must not require images.

### Inline Media Rendering (Chat → SSE → Frontend)
1. **Backend attachment** — `attachChatImage`/`attachChatVideo` in `chat.go` mutate the message and emit SSE events via `mutateChatMessage` (events are dropped if the message isn't found under the workspace)
2. **Frontend rendering** — `frontend/src/app/chat/index.ts` patches `.chat-message-videos` with `<video src="dataUrl" controls autoplay loop muted playsinline>`

### LLM Context Overflow Prevention
`toolResultVideoMessage` returns a **text-only placeholder** — no base64 dataURL embedded. Inline UI rendering works independently via `attachChatVideo`.

### Current Workflows

| File | Type | Notes |
|---|---|---|
| `flux_schnell_dual_prompt.json` | txt2img | Dual CLIP, template-driven params |
| `minimax_t2v.json` | text-to-video | Legacy hand-assembled graph with explicit `{{FRAMES}}`/`{{FPS}}`. Frame count is NOT validated against the model's constraints and fps defaults to 8 → choppy. Do not use for MiniMax H3 duration runs |
| `minimax_h3_video.json` | text-to-video | The user's manual ComfyUI graph, verbatim, with template slots: prompt, duration, aspect ratio, megapixels, steps, seed. Use this for MiniMax H3 |

### Minimax H3 Duration-Driven Pattern (`minimax_h3_video.json`)
- `PrimitiveFloat` ("value" = duration seconds) → `ComfyMathExpression` computes frame count: `max(5, round(a * 24)) + (5 - (max(5, round(a * 24)) % 17)) % 17`. **MiniMax H3 requires frame counts ≡ 5 (mod 17)** — 5s → 124 frames (not 120), 12s → 294. The model generates natively at 24fps; `CreateVideo` fps is fixed at 24.
- `ResolutionSelector`: `aspect_ratio` must be the **exact combo label**: `1:1 (Square)`, `2:3 (Portrait Photo)`, `3:2 (Photo)`, `3:4 (Portrait Standard)`, `4:3 (Standard)`, `9:16 (Portrait Widescreen)`, `16:9 (Widescreen)`, `21:9 (Ultrawide)`. `megapixels` float, `multiple` 32.
- Scheduler `simple`, sampler `res_multistep`; user runs at **8 steps**.
- **Never hand-compute frames/fps for this model** — pass duration and let the math node resolve it. A hand-computed length (e.g., 5s×24=120) violates the mod-17 constraint → frame-count/playback mismatch (choppy). This was the root cause of Echo runs being choppier and slower than manual ComfyUI runs.

### Performance: Cold-Start Overhead & Timing Verification
- **Cold vs warm matters a lot.** First MiniMax H3 run after server idle/app restart pays ~50–60s of model loading (UNET/CLIP/VAEs → VRAM) on top of generation. Measured on the dev server: same 5s / 3:4 / 0.2MP / 8-step prompt ran **110.8s cold** vs **56.0s warm**. Manual ComfyUI runs are always warm (weights stay resident), so compare Echo-vs-manual only warm-to-warm, or the first Echo run will look "twice as slow" for no workflow reason.
- **Verify a generation by querying history directly:** `GET /history/{prompt_id}` → entry's `prompt` is an **array `[number, id, {nodes}]`** — the substituted node inputs are at index 2 (PowerShell: `$e.prompt[2]`; indexing `.prompt['nodeId']` fails). Execution timing = message timestamps in `status.messages`: queued, execution_start, execution_success.
- **Verify output specs with ffprobe:** download via `GET /view?filename=...&subfolder=...&type=output` (the ComfyUI server is remote — files are NOT on the local disk), then check width/height/nb_frames/avg_frame_rate/duration. Confirmed parity numbers: 384×544, 124 frames, 24fps, 5.167s for 5s/3:4/0.2MP.

### Workflow Conversion Gotchas (DOM → flat API)
Invisible required fields: CLIPLoader `type`, UNETLoader `weight_dtype: "default"`, BasicScheduler extras, SaveVideo `codec: "auto"`. Use ComfyUI's `GET /object_info/NodeClassName` to discover.

### Pitfalls
- **Backend changes require an app restart** (rebuild via wails build). The running process keeps the old binary; unsubstituted `{{VAR}}` strings then reach ComfyUI → 400 "could not convert string to float: '{{DURATION}}'". Symptom looks like a workflow bug but is a stale-binary problem.
- **Workflow format must be flat API JSON** — not ComfyUI frontend DOM format
- **"images" key can contain video files** — route by extension, not output key name
- **`/view?type=` must match stored type** — mismatch returns HTTP 400. Always use the type reported in history (`MediaTypes`); custom-node Save Video (MiniMax H3) reports `custom` even for mp4
- Custom node `type` values vary: `"custom"` for extensions, `"output"` for standard, `""` for some custom nodes
- **Video-only workflows must not trip "no output images"** — completion checks accept images OR videos
- Workflow settings are **absolute file paths**, bypass workspace scoping
- Seed must be ≥ 0 and ≤ 2^53 (JSON float64 round-trip); MODEL always gets a fallback value
- ComfyUI server used for dev testing: `100.119.145.9:8188` (LAN, per user settings)
