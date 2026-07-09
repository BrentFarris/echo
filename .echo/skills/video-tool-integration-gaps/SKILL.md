---
name: video-tool-integration-gaps
description: Integration gaps found and fixed for filesystem_read_video tool registration, tests, and agent visibility
triggers:
    - video not working
    - filesystem_read_video missing
    - video tool not in plan mode
    - B-003
---

# Video Tool Integration Gaps (B-003)

## Background

`filesystem_read_video` tool code existed in `internal/tools/filesystem_read_video.go` with full registration via init(), but had integration gaps that prevented agents from using it.

## Gaps Found and Fixed

### 1. Missing from readOnlyToolNames (registry.go)
- Plan mode uses `ReadOnlyLLMSchema()` which filters to only tools in the `readOnlyToolNames` map
- `filesystem_read_video` was missing, so plan-mode agents couldn't see or use it
- **Fix**: Added `"filesystem_read_video": true` to `readOnlyToolNames`

### 2. Missing from AGENTS.md tool list
- Line 112 lists all filesystem tools for agent reference but omitted `filesystem_read_video`
- Agents get this injected into their system prompt and won't know about the tool
- **Fix**: Added `filesystem_read_video` to the listed gated tools

### 3. No test coverage
- Registry tests checked `filesystem_read_image` but not `filesystem_read_video`
- Filesystem tests had image tests but no video tests
- **Fix**: Added `filesystem_read_video` to registry test expectations, added labeled workspace path test entry, updated read-only count from 11 to 12, added two new filesystem tests:
  - `TestFilesystemReadVideoReturnsLLMVideoContent` - verifies MP4 reading, metadata, data URL, and LLMVideoContentProvider interface
  - `TestFilesystemReadVideoRejectsUnsupportedFormat` - verifies AVI rejection with proper error code

### 4. System test tool name lists
- `internal/services/system_test.go` had hardcoded expected tool name lists that didn't include video
- **Fix**: Added `filesystem_read_video` to both non-plan mode and plan mode test expectations

## Verification

```powershell
go test ./...  # All pass
cd frontend; npm run build  # Builds successfully
```

## Files Changed

- `internal/tools/registry.go` - Added video to readOnlyToolNames
- `internal/tools/registry_test.go` - Updated all tool name expectations and counts
- `internal/tools/filesystem_test.go` - Added 2 video tests
- `internal/services/system_test.go` - Updated plan/non-plan mode tool lists
- `AGENTS.md` - Added video to gated tools list

## Note on Frontend Upload Buttons

Image/video upload buttons in the chat composer are intentionally only shown in web-access mode (`!isWailsRuntime()`). Desktop Wails users attach media via drag-and-drop and paste. This is correct behavior.
