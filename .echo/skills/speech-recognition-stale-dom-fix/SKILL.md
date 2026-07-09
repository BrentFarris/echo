---
name: speech-recognition-stale-dom-fix
description: How to prevent stale DOM element references in speech recognition and other long-lived callbacks after innerHTML re-renders.
triggers:
    - speech recognition
    - stale DOM
    - closure bug
    - innerHTML re-render
    - detached element
    - textarea ghost node
    - onresult callback
    - appRoot.querySelector
---

## Problem

Echo does full DOM re-renders via `innerHTML` replacement on every app state change (chat messages, kanban events, settings changes, etc). Any DOM element reference captured in a closure becomes a **detached ghost node** after the next render.

This is particularly dangerous for long-lived async callbacks like Web Speech API's `onresult`, which can fire seconds after initial setup — well past multiple re-renders.

## Pattern: Resolve DOM elements fresh inside callbacks

**Wrong** (captures at bind/start time):
```ts
function startSpeechRecognition() {
  const inputEl = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  // ... later in callback, inputEl is detached
  recognition.onresult = (event) => {
    inputEl.value = transcript; // writes to ghost node — invisible to user
  };
}
```

**Right** (resolves fresh per callback invocation):
```ts
function startSpeechRecognition() {
  recognition.onresult = (event) => {
    // Fresh DOM lookup on each result event
    const inputEl = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
    if (!inputEl) return; // safety: nothing to write to
    inputEl.value = transcript;
  };
}
```

## Key files

- `frontend/src/app/chat/index.ts` — speech recognition module (`startSpeechRecognition`, `onresult` handler)
- `frontend/src/app/dom.ts` — exports `appRoot` used for live lookups

## Verification

1. `tsc --noEmit` shows no errors in the affected file
2. `cd frontend; npm run build` passes
3. Manual test: speak into mic while triggering re-renders (send chat, change settings) — transcript still appears in visible textarea
