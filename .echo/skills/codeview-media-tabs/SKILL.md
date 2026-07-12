---
name: codeview-media-tabs
description: How media files (images, videos, and audio) are rendered as dedicated tabs in the codeView with appropriate viewers, MIME routing, and styling.
triggers:
    - media tab
    - image tab
    - video tab
    - audio tab
    - codeView media
    - filesystem_read_image
    - ReadWorkspaceMediaFile
    - isMediaFile
    - isVideoFile
    - isAudioFile
---

## Media Tab Architecture

Media files (images, videos, and audio) open as read-only tabs in the code view, bypassing the CodeMirror text editor. The flow:

### File detection (`utils.ts`)
- `IMAGE_EXTENSIONS` set: jpg, jpeg, png, gif, bmp, webp, svg, ico, tiff, tif
- `VIDEO_EXTENSIONS` set: mp4, webm, ogv, mov, avi, mkv, flv, wmv
- `AUDIO_EXTENSIONS` set: mp3, wav, ogg, flac, aac, m4a, opus, wma
- `isImageFile(path)` / `isVideoFile(path)` / `isAudioFile(path)` / `isMediaFile(path)` — extension-based checks
- `mediaKind(path)` returns `"image"`, `"video"`, `"audio"`, or `""`

### Opening flow (`tabs.ts`)
- `openCodeFile` checks `isMediaFile(path)` before attempting text read
- Delegates to `openCodeMediaFile()` which calls `ReadWorkspaceMediaFile(workspaceID, path)`
- Backend reads file, detects MIME via magic bytes + extension fallback, returns base64 data URL
- Tab created with `isMedia: true`, `mediaMimeType`, `mediaDataUrl`

### Rendering (`editor.ts`)
- `mountActiveCodeEditor` checks `tab.isMedia && tab.mediaDataUrl` before mounting CodeMirror
- Video MIME types (`video/*`) → `renderVideoViewer(tab)` — renders `<video controls autoplay playsinline>` with data URL source
- Audio MIME types (`audio/*`) → `renderAudioViewer(tab)` — renders `<audio controls>` with data URL source
- Image MIME types → `renderImageViewer(tab)` + `bindImageViewerEvents()` — renders zoomable `<img>` with toolbar
- All three skip CodeMirror entirely; the mount div is populated with innerHTML

### Tab state (`types.ts`)
`CodeFileTab` media fields:
- `isMedia?: boolean` — true for image/video/audio tabs
- `mediaMimeType?: string` — detected MIME type (e.g., `"video/mp4"`, `"image/png"`, `"audio/mpeg"`)
- `mediaDataUrl?: string` — base64 data URL from backend
- `mediaLoading?: boolean` — loading state flag
- `mediaError?: string` — error message on load failure
- `zoomLevel?: number` — zoom level for image viewer (not used by video/audio)

### Backend support (`workspace_files.go`)
- `ReadWorkspaceMediaFile` reads file, detects MIME via `detectMediaType()` (magic bytes first, then extension)
- Supports `image/`, `video/`, and `audio/` prefixes; rejects non-media types
- 50 MB size limit (`maxWorkspaceMediaFileBytes`)
- Returns `WorkspaceMediaFile{Path, MimeType, DataURL, Bytes}`
- Magic byte detection includes: PNG, JPEG, GIF, WebP, MP4/M4A, WebM, MP3 (ID3 + sync frames), WAV (RIFF/WAVE), Ogg, FLAC, AAC (ADTS), Opus
- Extension fallback covers: .png, .jpeg/.jpg, .gif, .webp, .svg, .bmp, .ico, .tiff/.tif, .mp4, .webm, .avi, .mov, .mp3, .wav, .ogg, .flac, .aac, .m4a, .opus, .wma

### Styling (`styles.css`)
- `.code-image-viewer` — grid layout with toolbar + canvas, zoom controls
- `.code-video-viewer` — full-height black background container
- `.code-video-container video` — max-width/max-height 100%, native controls
- `.code-audio-viewer` — centered layout with audio player
- WebKit pseudo-element overrides for light-colored controls on dark theme

### Media tabs excluded from:
- Disk file watch reload (`reloadWorkspaceOpenCodeTabsFromDisk` skips `tab.isMedia`)
- CodeMirror editor mounting, LSP, inline chat
- Save operations (read-only)
