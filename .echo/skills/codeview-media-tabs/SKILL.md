---
name: codeview-media-tabs
description: How media files (images and videos) are rendered as dedicated tabs in the codeView with appropriate viewers, MIME routing, and styling.
triggers:
    - media tab
    - image tab
    - video tab
    - codeView media
    - filesystem_read_image
    - ReadWorkspaceMediaFile
    - isMediaFile
    - isVideoFile
---

## Media Tab Architecture

Media files (images and videos) open as read-only tabs in the code view, bypassing the CodeMirror text editor. The flow:

### File detection (`utils.ts`)
- `IMAGE_EXTENSIONS` set: jpg, jpeg, png, gif, bmp, webp, svg, ico, tiff, tif
- `VIDEO_EXTENSIONS` set: mp4, webm, ogv, mov, avi, mkv, flv, wmv
- `isImageFile(path)` / `isVideoFile(path)` / `isMediaFile(path)` — extension-based checks
- `mediaKind(path)` returns `"image"`, `"video"`, or `""`

### Opening flow (`tabs.ts`)
- `openCodeFile` checks `isMediaFile(path)` before attempting text read
- Delegates to `openCodeMediaFile()` which calls `ReadWorkspaceMediaFile(workspaceID, path)`
- Backend reads file, detects MIME via magic bytes + extension fallback, returns base64 data URL
- Tab created with `isMedia: true`, `mediaMimeType`, `mediaDataUrl`

### Rendering (`editor.ts`)
- `mountActiveCodeEditor` checks `tab.isMedia && tab.mediaDataUrl` before mounting CodeMirror
- Video MIME types (`video/*`) → `renderVideoViewer(tab)` — renders `<video controls autoplay playsinline>` with data URL source
- Image MIME types → `renderImageViewer(tab)` + `bindImageViewerEvents()` — renders zoomable `<img>` with toolbar
- Both skip CodeMirror entirely; the mount div is populated with innerHTML

### Tab state (`types.ts`)
`CodeFileTab` media fields:
- `isMedia?: boolean` — true for image/video tabs
- `mediaMimeType?: string` — detected MIME type (e.g., `"video/mp4"`, `"image/png"`)
- `mediaDataUrl?: string` — base64 data URL from backend
- `mediaLoading?: boolean` — loading state flag
- `mediaError?: string` — error message on load failure
- `zoomLevel?: number` — zoom level for image viewer (not used by video)

### Backend support (`workspace_files.go`)
- `ReadWorkspaceMediaFile` reads file, detects MIME via `detectMediaType()` (magic bytes first, then extension)
- Supports both `image/` and `video/` prefixes; rejects non-media types
- 50 MB size limit (`maxWorkspaceMediaFileBytes`)
- Returns `WorkspaceMediaFile{Path, MimeType, DataURL, Bytes}`

### Styling (`styles.css`)
- `.code-image-viewer` — grid layout with toolbar + canvas, zoom controls
- `.code-video-viewer` — full-height black background container
- `.code-video-container video` — max-width/max-height 100%, native controls
- WebKit pseudo-element overrides for light-colored controls on dark theme

### Media tabs excluded from:
- Disk file watch reload (`reloadWorkspaceOpenCodeTabsFromDisk` skips `tab.isMedia`)
- CodeMirror editor mounting, LSP, inline chat
- Save operations (read-only)
