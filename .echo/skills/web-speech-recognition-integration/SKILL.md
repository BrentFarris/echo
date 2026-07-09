---
name: web-speech-recognition-integration
description: 'Speech recognition lifecycle management in the chat composer: Web Speech API TypeScript interfaces, init, pointer-based hold-to-speak, start/stop instance management, cursor-preserving text insertion, and error handling.'
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
    - SpeechRecognitionInstance
---

## Speech Recognition Lifecycle Architecture

### Location
`frontend/src/app/chat/index.ts` — interfaces near top, functions before `bindChatEvents`.

### Web Speech API TypeScript Interfaces
TypeScript's `lib.dom.d.ts` does not include `SpeechRecognition` types. Duck-typed interfaces are defined at the top of `frontend/src/app/chat/index.ts`:

- **`SpeechRecognitionInstance`** (exported) — main interface extending `EventTarget`; covers `continuous`, `interimResults`, `lang`, `maxAlternatives`, `start()`, `stop()`, `abort()`, `setProperty()`, and event handlers (`onresult`, `onerror`, `onend`, `onstart`).
- Supporting: `SpeechRecognitionEvent`, `SpeechRecognitionErrorEvent`, `SpeechRecognitionResultList`, `SpeechRecognitionResult`, `SpeechRecognitionAlternative`, `SpeechGrammar`, `SpeechGrammarList`.

At runtime the constructor is accessed via `(window as any).SpeechRecognition || (window as any).webkitSpeechRecognition`.

### Module-level state
- `_activeRecognition: SpeechRecognitionInstance | null = null` — tracks the current instance (typed, not `any`).
- `speechRecognitionBound: boolean = false` — prevents re-init across render cycles.

Both are declared near `initSpeechRecognition`, not in the constants section.

### Function flow

1. **`initSpeechRecognition(root)`** — Guards: web-only (`!isWailsRuntime()`), one-time via `speechRecognitionBound`, API availability, button existence. Delegates to `patchSpeechMicButton`.

2. **`patchSpeechMicButton(button)`** — Pointer events with 200ms hold threshold. Sets `button.dataset.speechRecogBound = 'true'` before binding to prevent duplicate handlers on re-renders.

3. **`startSpeechRecognition(inputEl)`** — Calls `stopSpeechRecognition()` first. Creates new instance with `continuous: true`, `interimResults: true`. `onresult` inserts transcript at `inputEl.selectionStart`, preserves text after cursor, fires synthetic `input` event. Button gets `.is-listening` class.

4. **`stopSpeechRecognition()`** — Aborts `_activeRecognition`, nulls it, removes `.is-listening`. Safe to call when no active recognition.

### Error handling
- `onerror`: toast for `permission-denied`/`not-allowed`; console.warn for others.
- `onend`: always cleans up button state and nulls `_activeRecognition`.
- `start()` catch: same cleanup as onend.

### CSS
`.chat-speech-recognition.is-listening svg` applies pulse animation and danger color in `styles.css`.

### Button rendering
Rendered conditionally with `${!isWailsRuntime()}` check in chat toolbar HTML. Has `data-chat-speech-recognition` attribute, disabled when session is busy/executing.
