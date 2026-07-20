---
name: comfyui-integration
description: 'ComfyUI integration: workflow resolution, template substitution, comfyui_generate tool with imagePath upload pipeline, client API including UploadImage for img2img workflows, separate txt2img/img2img workflow settings, error detection, polling, inline image return, and workspace .comfy/workflows/ convention'
triggers:
    - comfyui
    - comfyui template substitution
    - comfyui workflow defaults
    - comfyui error handling
    - comfyui upload image
    - img2img workflow
    - LoadImage node
    - imagePath parameter
    - comfyui_generate tool
    - txt2img workflow
    - FLUX workflow
    - comfyui img2img
---

## ComfyUI Integration Architecture

### Package Structure
- `internal/comfyui/workflow.go` — Workflow loading, template substitution, default builder
- `internal/comfyui/client.go` — HTTP client for `/prompt`, `/history`, `/view`, `/upload/image`; model/workflow listing; execution error detection
- `internal/comfyui/queue.go` — Polling (`WaitForCompletionPoll`) and WebSocket waiting; `FetchImageBytes`
- `internal/tools/comfyui_generate.go` — Tool registration, imagePath upload pipeline, inline image fetching via `LLMImageContentProvider`

**No bundled workflows.** Users manage their own workflow JSON files on disk, configured via settings. User workflows conventionally live in `.comfy/workflows/`.

### Workflow Settings (Separate txt2img / img2img)
Settings (`internal/llm/settings.go`) expose two independent workflow path fields:
- `ComfyuiTxt2imgWorkflow string` (json key `comfyuiTxt2imgWorkflow`) — absolute path to a txt2img workflow JSON
- `ComfyuiImg2imgWorkflow string` (json key `comfyuiImg2imgWorkflow`) — absolute path to an img2img workflow JSON

Both are trimmed in `Normalized()`. These paths are wired into `ExecutionContext.ComfyuiTxt2imgWorkflow` and `ExecutionContext.ComfyuiImg2imgWorkflow`.

**Migration:** On first load after upgrade, if the old `comfyuiDefaultWorkflow` key exists in state.json and both new fields are empty, the old value is copied to both new fields. The migration code is in `LoadState` in `internal/services/system.go`.

### Workflow Resolution Priority
1. `workflowJSON` parameter → parse inline JSON directly via `ParseWorkflowJSON()`
2. `workflowPath` parameter → resolve via `resolveWorkspaceChildPath()` (workspace-scoped, traversal-safe), then `LoadWorkflowJSON()`
3. Neither explicit workflow given → **select based on `imagePath` presence:**
   - `imagePath` present → load from `ExecutionContext.ComfyuiImg2imgWorkflow`
   - No `imagePath` → load from `ExecutionContext.ComfyuiTxt2imgWorkflow`
   - If the selected field is empty, fall through to next step
4. Neither setting configured → `BuildDefaultWorkflow(params)` generates minimal pipeline

Both `LoadWorkflowJSON` and `ParseWorkflowJSON` enforce 500KB size cap and validate structure (nodes must have `class_type`).

### imagePath Parameter — Workspace Image Upload Pipeline
The `comfyui_generate` tool accepts an optional `imagePath` string parameter (workspace-relative path) to support img2img workflows:

1. **Schema**: `"imagePath"` added to tool JSON schema properties as type `string`
2. **Struct**: `ImagePath string \`json:"imagePath"\`` on `comfyuiArgs`
3. **Handler pipeline** (after workflow load, before `client.Generate`):
   - Resolve via `resolveWorkspaceChildPath(ctx, args.ImagePath)` (workspace-scoped, traversal-safe)
   - Validate: file exists, is regular, under `maxImageFileBytes` (10 MB)
   - Read bytes with `os.ReadFile`
   - Generate filename: `"echo_input_" + uuid.New().String() + filepath.Ext(resolvedPath)`
   - Upload via `client.UploadImage(ctx.context(), serverFilename, imageData)`
   - Set `params.Image = uploadedName` so `{{IMAGE}}` template substitution works in workflows with `LoadImage` nodes
4. **Permission gating**: `"imagePath"` is registered in `isPathArgKey` in `registry.go` so agent mode path permissions apply

If `imagePath` is empty, the upload step is skipped entirely and `params.Image` remains unset (no `{{IMAGE}}` substitution).

### Image Upload for img2img Workflows
ComfyUI's `LoadImage` node requires images to exist in the server's input directory. `Client.UploadImage(ctx, filename, data)` handles this:
- POSTs multipart form to `{BaseURL}/upload/image` with field `image` containing the file bytes
- Uses `filepath.Base(filename)` for the form filename to strip any path traversal
- Parses JSON response `{"name": "filename.png", "subfolder": "", "type": "input"}` and returns the stored filename string
- The returned filename is what goes into a workflow's `LoadImage` node `"image"` input field (or `{{IMAGE}}` template)

### Workspace Workflow Directory
User-created reusable workflows live in `.comfy/workflows/` inside the workspace. These are loaded via `workflowPath` parameter (workspace-relative, traversal-safe).

**Current Workflows:**

| File | Type | Notes |
|---|---|---|
| `flux_schnell_dual_prompt.json` | txt2img | Dual CLIP (clip_l + t5xxl both get `{{PROMPT}}`), template-driven steps/cfg/seed/dims |
| `flux_schnell_full_text_to_image.json` | txt2img | clip_l only, hardcoded 1024×1024, 4 steps, guidance 3.5 |
| `flux_schnell_img2img.json` | img2img | LoadImage → VAEEncode → dual CLIP encode → KSampler (denoise 0.85) → VAEDecode |

**FLUX Schnell Common Pattern:**
- `UNETLoader` → `flux1-schnell.safetensors`
- `DualCLIPLoader` → T5XXL + CLIP-L (type: `flux`)
- `VAELoader` → `ae.safetensors`
- `CLIPTextEncodeFlux` for positive conditioning, `ConditioningZeroOut` for negative
- FLUX Schnell needs ~4–8 steps, CFG ~1 (guidance-distilled)

**img2img Workflow (`flux_schnell_img2img.json`):**
- Adds `LoadImage` node reading `{{IMAGE}}` → `VAEEncode` to get latent from input image
- KSampler uses `denoise: 0.85` (adjustable — lower preserves more original, higher allows more creativity)
- Same dual-prompt architecture as txt2img dual_prompt variant

### Template Variable System
TemplateParams struct holds substitutable values. Variables use `{{VAR}}` syntax:
- `{{PROMPT}}`, `{{NEGATIVE_PROMPT}}` — text prompts
- `{{MODEL}}` — checkpoint name (unused in FLUX workflows that use UNETLoader directly)
- `{{IMAGE}}` — uploaded image filename on ComfyUI server (set via `params.Image`)
- `{{WIDTH}}`, `{{HEIGHT}}` — latent dimensions
- `{{STEPS}}`, `{{CFG_SCALE}}`, `{{SEED}}` — sampling parameters

**Critical: `buildReplaceMap` always applies defaults for numeric params**, so every `{{VAR}}` is substituted even when the caller omits it:
| Variable | Default when unspecified |
|---|---|
| WIDTH | 512 |
| HEIGHT | 512 |
| STEPS | 20 |
| CFG_SCALE | 7.5 |
| SEED | 847291053 (positive random-ish value — ComfyUI rejects -1) |
| MODEL | "checkpoint1" (always set; prevents unsubstituted `{{MODEL}}` reaching ComfyUI) |

This prevents literal `{{STEPS}}` strings from reaching ComfyUI, which would cause execution errors since numeric nodes expect numbers not template placeholders.

Substitution is recursive — works in nested maps, arrays, and any JSON depth. Non-matching strings pass through unchanged.

### Default Workflow Nodes
`BuildDefaultWorkflow` generates: CheckpointLoaderSimple → 2× CLIPTextEncode → EmptyLatentImage → KSampler → VAEDecode → SaveImage. Node IDs are sequential integers starting at 1. This is the fallback when no workflow is configured — it uses SD-style nodes, not FLUX-specific ones.

### Generation Flow
1. `Client.Generate()` POSTs workflow to `/prompt`, gets `prompt_id`
2. `WaitForCompletionPoll()` polls `/history/{id}` every second
3. On success, history returns image list; tool fetches first image via `FetchImageBytes()` from `/view`
4. Image bytes are base64-encoded into a `data:` URL in `comfyuiOutput.dataURL`
5. `comfyuiOutput` implements `LLMImageContentProvider` so chat renders it inline

### Execution Error Detection
`GetHistory()` checks for errors before returning images (priority order):
1. **Status-level `messages[]`** — ComfyUI 0.24+ puts execution errors here as `[event_type, data]` tuples with `node_id`, `exception_type`, `exception_message`
2. **Status-level `error` map/object** — extracts `message`, falls back to first `traceback` line
3. **Status-level `error` string** — direct error string
4. **Node-level** — `outputs[nodeID]["error"]` + `"error_message"` → returns `*ExecutionError`
5. **`status_str == "error"`** — only a signal; use node-level error if found, otherwise generic fallback

`WaitForCompletionPoll` treats `*ExecutionError` as fatal (stops polling immediately). Other errors (e.g., "not found in history") are retried — the prompt is still running.

### Key Functions
- `SubstituteTemplateVariables(workflow, params)` — template replacement
- `ValidateWorkflow(workflow)` — checks nodes have class_type
- `BuildDefaultWorkflow(params)` — generates fallback workflow
- `Client.Generate(ctx, params, workflow)` — full generate-and-wait flow
- `Client.ListModels(ctx)` — checkpoint listing with Manager/native fallback
- `Client.UploadImage(ctx, filename, data)` — upload image to ComfyUI input directory; returns stored filename for `LoadImage` node

### Frontend Integration
- Settings UI (`frontend/src/app/settings/index.ts`) shows two text inputs: "Txt2img Workflow" and "Img2img Workflow"
- No bundled preset buttons or workflow dropdowns
- Generated bindings in `frontend/wailsjs/go/models.ts` reflect `comfyuiTxt2imgWorkflow` / `comfyuiImg2imgWorkflow` fields on `Settings`

### Pitfalls
- Template variables in JSON **must be quoted strings** (`"{{WIDTH}}"`), never bare values (`{{WIDTH}}`), or the JSON is invalid before substitution
- `resolveWorkspaceChildPath` rejects the workspace root itself — workflow files must be inside a subdirectory
- Polling loop must distinguish between "still running" (retry) and execution errors (fail fast)
- Mock tests must handle all relevant endpoints: `/prompt`, `/history/{id}`, `/view`, and `/upload/image` when testing imagePath
- Workflow settings are **absolute file paths**, not workspace-relative — they bypass workspace scoping
- **Seed must be ≥ 0** — ComfyUI rejects `-1`. `buildReplaceMap` uses `847291053` as the positive fallback.
- **MODEL always gets a value** — `buildReplaceMap` sets it to `"checkpoint1"` when unspecified.
- **Unspecified numeric params get defaults in `buildReplaceMap`** — custom workflows relying on user-provided values for Steps/CfgScale/Width/Height will silently use defaults if the tool caller doesn't pass them.
- **UploadImage uses `filepath.Base()`** on the filename to strip path traversal attempts.
- **imagePath goes through `isPathArgKey`** in `registry.go` — agent mode path permission checks apply.
- **No bundled workflows** — all workflow JSON files are user-managed on disk.
- **Migration is one-shot** — old `comfyuiDefaultWorkflow` values in state.json are copied to both new fields on first load.
