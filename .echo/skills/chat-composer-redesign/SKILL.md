---
name: chat-composer-redesign
description: How the unified VS Code Chat-style composer bar is structured, styled, and wired up including merged plus media picker, model selector dropdown, dual-purpose send/stop button, removal of plan toggle, and mobile responsive CSS adjustments.
triggers:
    - chat composer
    - VS Code Chat
    - input bar
    - unified input
    - agent mode
    - model selector
    - attachment button
    - composer-bar
    - media picker
    - send stop button
    - responsive
    - mobile
---

# Chat Composer Unified Bar Layout

## Overview
The chat composer is a single unified VS Code Chat-style input bar. HTML structure was restructured in card-6; CSS styling refined in card-7.

## Key Architecture Decisions

### HTML Structure (renderChatPanel in frontend/src/app/chat/index.ts)
- **composer-bar**: Single-line flex row containing:
  - `composer-add-btn` (+ button): toggles media picker menu via data-action="composer-toggle-media-picker"
  - `composer-model-select`: select listing all endpoint+model combos from settings
  - `textarea[data-chat-input]`: single-row auto-growing textarea
  - `chat-speech-recognition`: mic button (hold-to-speak)
- **composer-drafts**: Container for image/video draft chips below the bar
- **media-picker-menu** (data-media-picker-menu): Absolute-positioned popup with Image/Video options, hidden by default
- **composer-send-btn**: type="button", dual-purpose: sends when idle, stops stream when busy — sits outside the form as a sibling element

### Removed Elements
- chat-input-wrap container div
- chat-image-upload button (separate image picker)
- chat-video-upload button (separate video picker)
- chat-plan-toggle label + checkbox — entire rule block removed from styles.css
- Old .chat-composer textarea block-level styles

### Event Bindings (bindChatEvents in chat/index.ts)
- Form submits via handleChatSubmit
- New bindings: composer-toggle-media-picker, composer-upload-image, composer-upload-video
- Model selector change -> handleComposerModelChange updates state.settingsDraft.endpoint
- Outside click listener (handleComposerMediaPickerOutsideClick) closes media picker menu

### Actions (actions.ts)
- composer-submit: Sends if idle (via form.requestSubmit()), stops stream if busy (calls StopChatStream)

### CSS Classes (frontend/src/styles.css)
- .chat-composer: Flex column layout, gap-based spacing between bar/drafts/send-btn
- .composer-bar: Flex row, bordered rounded container, focus-within accent highlight
- .composer-add-btn: 32px icon button (44px on mobile <=720px)
- .composer-model-select: Truncated inline select, max-width 240px desktop / 160px mobile
- .composer-bar textarea: Inline, no border/resizer, transparent bg
- .chat-speech-recognition: Inside composer-bar, 32px (44px on mobile)
- .composer-drafts: Horizontal scrollable chip area, max-height 120px (96px mobile)
- .media-picker-menu: Absolute popup above "+" button, surface bg, shadow
- .media-picker-option: Icon + text option rows
- .composer-send-btn: primary-button style, 44px square send/stop button

### Mobile Responsive Rules (@media (max-width: 720px))
- .composer-add-btn, .chat-speech-recognition: min 44x44px touch targets
- .composer-model-select: reduced max-width (160px)
- .composer-bar: tighter padding and gaps
- .composer-drafts: shorter max-height (96px vs 120px)
- Existing .icon-button rule enforces 44px minimum across the board

### Data Attributes
- data-composer-bar: main bar container
- data-composer-model-select: model dropdown
- data-composer-drafts: drafts container
- data-media-picker-menu: attachment menu popup
- data-action=composer-toggle-media-picker: toggle button
- data-action=composer-upload-image: upload image option
- data-action=composer-upload-video: upload video option
- data-action=composer-submit: send/stop button (on .composer-send-btn)
