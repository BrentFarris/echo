---
name: comfyui-client-native-api-fallback
description: 'ComfyUI client model/workflow API fallback: Manager endpoints vs native ComfyUI API, and HTTP method requirements.'
triggers:
    - ComfyUI models
---

## ComfyUI API Endpoints

### Model Discovery (Two-Tier Fallback)

`ListAllModels()` tries **Manager API first**, then falls back to **native ComfyUI API**:

1. **Manager** `GET /api/manage/models` — returns rich metadata (filename, format, size, hash). Returns 404 if Manager is not installed or not exposing its API.
2. **Native fallback** `GET /api/models` → list of type strings (`checkpoints`, `loras`, `vae`, etc.), then `GET /api/models/{type}` for each → returns `["filename1.safetensors", ...]`. No hash/size/format metadata.

`ListModels()` filters `ListAllModels()` to include both `checkpoints` and `diffusion_models` types (Flux-style workflows use diffusion_models separately from checkpoints).

### Workflow Discovery

- `GET /api/manage/workflows/list` — Manager-only. Returns 404/405 on most instances. Falls back gracefully with empty slice (no error) when unavailable.
- `GET /api/manage/workflows/get?workflow={name}` — Fetch single workflow JSON by name. Also Manager-only.

### HTTP Methods That Matter

| Endpoint | Method | Notes |
|---|---|---|
| `/api/manage/models` | GET | Was never POST |
| `/api/manage/workflows/list` | **GET** | Previously used POST → 405 Method Not Allowed |
| `/api/manage/workflows/get` | **GET** | Previously used POST → 405 Method Not Allowed |

### Key Pitfall

ComfyUI-Manager is an **optional extension**. Its API endpoints (`/api/manage/*`) do not exist on vanilla ComfyUI instances. Always fall back to native `/api/models` endpoints. The client prints a log line when falling back so the user knows Manager wasn't used.
