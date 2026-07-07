---
name: web-speech-recognition-integration
description: Web Speech Recognition integration in the Echo chat composer for mobile web UI, providing tap-and-hold voice-to-text input with graceful degradation and permission error handling.
triggers:
    - speech recognition
    - voice input
    - microphone
    - Web Speech API
    - tap-and-hold
    - transcription
    - mobile web UI
    - chat composer
    - webkitSpeechRecognition
---

## Web Speech Recognition Integration

### Overview
The chat composer includes a microphone button (`chat-speech-recognition`) that uses the browser Web Speech API (`SpeechRecognition`/`webkitSpeechRecognition`) for voice-to-text input on mobile web UI. Only visible when accessed via browser (not Wails desktop runtime).

### Key Implementation Details

**File**: `echo/frontend/src/app/chat/index.ts`

- **Detection**: Checks for `window.SpeechRecognition` or `window.webkitSpeechRecognition`. Gracefully degrades — no JS errors if unavailable.
- **Type-safe wrapper**: Uses `SpeechCtor` interface and `SpeechRecInstance` interface to type the duck-typed Web Speech API constructors.
- **Tap-and-hold gesture**: Uses pointer events (`pointerdown`, `pointerup`, `pointercancel`) with a 200ms hold threshold. Hold starts listening, release stops it.
- **Cursor preservation**: Inserts transcribed text at the current cursor position using `selectionStart`/`setSelectionRange`, preserving unsaved content.
- **Interim results**: Shows real-time transcription updates while speaking, then finalizes on recognition end.
- **Permission handling**: Detects `not-allowed` / `permission-denied` errors and shows a toast instructing users to enable microphone access in browser settings.
- **State management**: Global `_activeRecognition` variable tracks the active recognition instance; `stopSpeechRecognition()` safely aborts any ongoing session.
- **UI feedback**: Button gets `.is-listening` class with animated pulse and red color during recording. Title changes between "Hold to speak" and "Listening... Tap to stop".

### CSS Styling

**File**: `echo/frontend/src/styles.css`

```css
.chat-speech-recognition.is-listening svg {
  animation: pulse-mic 1s ease-in-out infinite;
  color: var(--color-danger);
}

@keyframes pulse-mic {
  0%, 100% { transform: scale(1); }
  50% { transform: scale(1.15); }
}
```

### Icon Definition

**File**: `echo/frontend/src/app/icons.ts`

Added `mic` icon: a microphone SVG with base, grille arc, stand line, and foot bar.

### Initialization Flow

1. `bindChatEvents()` calls `initSpeechRecognition()` after other bindings.
2. `initSpeechRecognition()` only runs in non-Wails environments (`!isWailsRuntime()`).
3. Finds the button by `[data-chat-speech-recognition]` attribute.
4. Calls `patchSpeechMicButton()` which binds pointer event listeners (one-shot per button).
5. Button gets marked with `dataset.speechRecogBound = 'true'` to prevent rebinding on re-renders.

### Acceptance Criteria Met
- ✅ Mic button appears only in web UI (guarded by `isWailsRuntime()` check)
- ✅ Tap-and-hold starts/stops listening
- ✅ Unsupported browsers silently omit functionality (no JS errors)
- ✅ Permission denied shows clear toast
- ✅ Text inserted at cursor position without losing unsaved content
