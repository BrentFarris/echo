---
name: codeview-image-tabs
description: How image files are rendered as dedicated tabs in the codeView with zoom controls, toolbar, and proper tab styling.
triggers:
    - image tab
    - codeView image
    - media file viewer
    - zoom controls
    - image rendering
---

## Image Tab Rendering in codeView

Image files (png, jpg, jpeg, gif, bmp, webp, svg, ico, tiff, tif) open in dedicated tabs with a built-in image viewer instead of the CodeMirror text editor.

### Flow

1. `openCodeFile` in `tabs.ts` checks `isImageFile(path)` and delegates to `openCodeImageFile`
2. `openCodeImageFile` calls `ReadWorkspaceMediaFile` (backend) which returns a data URL + MIME type
3. Creates a `CodeFileTab` with `isMedia: true`, `mediaDataUrl`, `mediaMimeType`, `zoomLevel: 1`
4. On render, `mountActiveCodeEditor` in `editor.ts` detects `tab.isMedia && tab.mediaDataUrl` and renders the image viewer instead of CodeMirror

### Key Files

- **`frontend/src/codeView/editor.ts`** - `renderImageViewer()` creates the HTML with toolbar (zoom in/out/fit buttons + percentage display) and canvas with `<img>`. `bindImageViewerEvents()` wires up click handlers for zoom buttons and Ctrl+wheel on canvas.
- **`frontend/src/codeView/render.ts`** - Tab rendering shows image icon (`codeIcons.image`) and "Image" badge for media tabs. Save button disabled for `isMedia`. Inline chat toggle disabled for `isMedia`. Status line shows MIME type for images.
- **`frontend/src/styles.css`** - `.code-image-viewer` (grid layout: toolbar + canvas), `.code-image-toolbar`, `.code-image-canvas` (centered, scrollable), `.code-image-zoom-level`, `.code-tab-icon`
- **`frontend/src/codeView/tabs.ts`** - `openCodeImageFile()` reads media, creates tab. File watch skips `tab.isMedia` tabs from disk reload.

### Zoom Controls

- Buttons: Zoom out (-25%), Zoom in (+25%), Fit (reset to 1x)
- Mouse: Ctrl/Cmd + scroll wheel for fine-grained zoom (±0.1)
- Range clamped: 0.1 (10%) to 5 (500%)
- Zoom level stored on `tab.zoomLevel` and applied via CSS `transform: scale(N)`

### Tab Styling

Media tabs get class `is-media`, show image icon in tab, and display "Image" badge similar to "External" badge. Save is always disabled for media tabs.

### Hazards

- Image tabs have `content: ""` so never mount CodeMirror - always check `tab.isMedia` before editor operations
- `patchImageZoomUI` patches the DOM directly without full re-render for zoom changes, then calls `callbacks.render()` to update tab state
- Data URLs can be large; backend caps media files at 50MB (`maxWorkspaceMediaFileBytes`)
