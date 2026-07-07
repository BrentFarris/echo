---
name: chat-image-upload-web
description: How image file upload button is rendered in the chat composer for web-access mode, integrated with the draft attachment flow, and styled.
triggers:
    - chat image upload
    - web-access image picker
    - file picker chat composer
    - chat-image-upload button
    - addPastedChatImages
    - image draft attachment web mode
---

## Image Upload Button in Web-Access Chat Composer

### Overview

In web-access mode (browser, not Wails desktop), the chat composer shows an image upload button that opens a native file picker. Selected images are added as draft attachments via the same `addPastedChatImages` flow used for paste-based insertion.

### Key Files

- `frontend/src/app/chat/index.ts` — composer rendering, handler, event binding
- `frontend/src/styles.css` — `.chat-image-upload` styling
- `frontend/src/backend/web.ts` — `isWailsRuntime()` detection

### Rendering

The upload button is conditionally rendered inside the `chat-input-wrap` div, before the textarea:

```typescript
${!isWailsRuntime() ? `
  <button class="icon-button chat-image-upload" type="button" title="Attach image" aria-label="Attach image" data-chat-image-upload
    ${session.busy || executing ? "disabled" : ""}
  >
    ${icons.image}
  </button>
` : ""}
```

- Uses `icons.image` SVG from `frontend/src/app/icons.ts`
- Disabled during busy/executing states
- Only shown when `!isWailsRuntime()` (i.e., web-access browser mode)

### Handler Flow

1. `handleChatImageUpload(event)` — validates workspace exists, session not busy, button not disabled
2. `selectChatImageFiles(workspaceID)` — creates a hidden `<input type="file" multiple accept="image/png,image/jpeg,image/gif,image/webp">`, appends to body, clicks it
3. On change: passes selected files to `addPastedChatImages(workspaceID, files)`

### Draft Attachment Integration

`addPastedChatImages` (already existing) handles:
- Type validation via `isSupportedChatImageType()` (PNG/JPEG/WEBP/GIF only)
- Per-image size limit: `maxChatImageBytes` (10 MB)
- Total message size limit: `maxChatImageMessageBytes` (20 MB)
- Attachment count limit: `maxChatImageDrafts` (4 images)
- Converts each file to data URL via `fileToDataURL()`
- Stores as `ChatImageDraft[]` in `state.chatImageDrafts` Map keyed by workspace ID

### Preview

Existing `renderChatImageDrafts(workspaceID, disabled)` renders thumbnail chips showing image preview, name, size, and remove button. Called in the composer template after the textarea.

### Event Binding

In `bindChatEvents()`:
```typescript
root.querySelectorAll<HTMLButtonElement>("[data-chat-image-upload]")
  .forEach((button) => button.addEventListener("click", handleChatImageUpload));
```

### CSS Styling

`.chat-image-upload` is positioned absolutely at `top: 10px; left: 4px` inside the relatively-positioned `.chat-input-wrap`. It's a 32x32 icon button with transparent background, muted color, and accent-colored hover state. Disabled state uses opacity 0.45.

### Pattern for Adding More Upload Buttons

To add another media type upload button (e.g., video):
1. Add the button HTML in the same conditional block alongside the image button
2. Use a distinct `data-*` attribute (e.g., `data-chat-video-upload`)
3. Add a corresponding handler and file selection function
4. Bind the event in `bindChatEvents`
5. Add CSS styling for the new button class
6. Ensure the draft type, state helpers, and rendering functions exist before wiring them in — incomplete infrastructure will cause TypeScript build failures
