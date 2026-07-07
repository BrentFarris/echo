---
name: composer-single-bar-layout
description: Single-row flexbar layout for the chat composer including HTML structure, CSS rules, data attributes, and event bindings.
triggers:
    - chat composer
    - single row
    - flexbar
    - layout
    - desktop
    - mobile
    - responsive
---

## Chat Composer Single-Line Flexbar Layout

### Structure
The chat composer (frontend/src/app/chat/index.ts) uses a single-row flexbox form:
- form.chat-composer[data-chat-form] contains three parts:
  - div.chat-composer-main[data-chat-input-wrap]: inline attachment, model selector, textarea, drafts, mention picker
  - div.chat-composer-actions: execute plan and clear chat buttons
  - button.send-button[data-action="send-stop"]: send/stop button

### Key CSS Classes
- .chat-composer: Desktop = display:flex; Mobile = flex-direction:column
- .chat-composer-main: flex:1 1 auto, holds inline controls relative positioned
- .chat-composer-actions: fixed-width secondary actions (replaces old .chat-composer-controls)
- .send-button: primary send/stop at far right

### Data Attributes (must be preserved for JS selectors)
- [data-chat-form]: form submit binding
- [data-chat-input-wrap]: mention picker injection target (patchChatMentionPicker)
- [data-chat-input]: textarea input/change/bindings
- [data-action="send-stop"]: send/stop click handler
- [data-action="execute-plan"]: execute plan button
- [data-action="clear-chat"]: clear chat button
- [data-model-selector] / [data-model-dropdown]: model selector dropdown
- [data-chat-attachment-toggle] / [data-chat-attachment-menu]: media attachment menu

### Patch Functions
- patchChatPanel(): full re-render of composer HTML
- patchChatControls(): updates busy/disabled states
- patchChatMentionPicker(): inserts mention picker into [data-chat-input-wrap]

### Media Draft Rendering
Image and video draft inner content rendered directly inside chat-composer-main (outer div stripped) to keep them inline.

### Styling Notes
- Textarea resize:vertical allowing growth beyond single row, max-height 150px, min-height 38px
- Icon buttons maintain 44x44 touch targets
- Old .chat-input-wrap CSS class still exists but unused (JS uses data attributes)
