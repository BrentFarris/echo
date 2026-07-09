---
name: chat-composer-mic-button-integration
description: Mic button integration in chat composer toolbar for web-mode speech recognition, including icon addition, HTML placement, runtime guard, and event wiring.
triggers:
    - mic button
    - speech recognition
    - voice input
    - chat composer toolbar
    - web speech API
    - hold to speak
    - webkitSpeechRecognition
---

## Mic Button in Chat Composer Toolbar

The mic button provides voice-to-text input via Web Speech API in web-access mode only. It is hidden in Wails desktop mode.

### Key Files
- `frontend/src/app/icons.ts` — Added `mic` SVG icon (Lucide-style microphone)
- `frontend/src/app/chat/index.ts` — Button HTML in toolbar-left, `initSpeechRecognition()` wiring
- `frontend/src/styles.css` — `.chat-speech-recognition.is-listening` pulse animation (pre-existing)

### Placement
Button inserted in `.chat-composer-toolbar-left`, after the attachment menu (`</div>` closing `.chat-attachment-menu`) and before the model selector `<button>`.

### Runtime Guard
```ts
${!isWailsRuntime() ? `
<button class="chat-toolbar-icon chat-speech-recognition" type="button" title="Hold to speak" aria-label="Voice input" data-chat-speech-recognition ${session.busy || executing ? "disabled" : ""}>
  ${icons.mic}
</button>
` : ''}
```

### Event Wiring
`initSpeechRecognition(root)` is called at the end of `bindChatEvents()`. It:
- Returns early if `isWailsRuntime()` is true
- Binds `mousedown`/`mouseup`/`mouseleave` on `[data-chat-speech-recognition]` buttons
- Uses `(window as any).SpeechRecognition || (window as any).webkitSpeechRecognition` for browser compatibility
- Inserts transcript at the textarea cursor position and dispatches an `input` event

### TypeScript Notes
Web Speech API types are not in the project's lib target. Cast through `any`:
```ts
const SpeechRecognition: any = (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition;
recognition.onresult = (event: any) => { ... };
```

### Verification
Run `cd frontend; npm run build` to confirm TypeScript compiles cleanly.
