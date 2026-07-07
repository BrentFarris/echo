---
name: chat-media-attachment-button
description: 'How the merged chat media attachment button works: a single plus toggle reveals a floating menu with Image/Video options, triggering existing file picker flows.'
triggers:
    - chat media attachment
    - attach button
    - popup menu
    - file picker chat
    - attachment toggle
---

## Chat Media Attachment Button (Single Plus Menu)

### Overview
Separate image and video upload buttons replaced with a single circular plus button that toggles a floating popup menu containing Image/Video options. Reduces visual clutter in the chat composer area.

### Key Files
- frontend/src/app/chat/index.ts - render and event handling
- frontend/src/styles.css - styles for toggle, menu, and options

### HTML Structure (in renderChatPanel)
A single button with class "chat-attachment-toggle" and data attribute "data-chat-attachment-toggle" renders inside chat-input-wrap. Below it, a div with class "chat-attachment-menu" and data attribute "data-chat-attachment-menu" contains two option buttons: one with data-attachment-type="image" and one with data-attachment-type="video".

### Event Flow
1. bindChatEvents registers click handlers on [data-chat-attachment-toggle] calling handleChatAttachmentToggle
2. Clicking the toggle opens/closes the menu via aria-expanded attribute and hidden property
3. bindChatAttachmentMenuDismissal adds a document-level click listener that closes the menu when clicking outside either the toggle or the menu itself
4. Clicking an attachment option fires handleChatAttachmentSelect which reads data-attachment-type and calls selectChatImageFiles or selectChatVideoFiles
5. The menu dismisses after selection via dismissChatAttachmentMenu

### State Variable
chatAttachmentMenuOpen boolean tracks whether the popup menu is currently displayed

### Disabled During Streaming
Both the toggle button and menu options respect the existing busy/executing state by disabling the toggle button when session.busy or executing. The menu dismissal handler also checks these states before proceeding with file selection.

### CSS Classes
- .chat-attachment-toggle - positioned absolutely at top-left of input area, 32x32px circular button
- .chat-attachment-menu - floating menu below toggle, z-index 10, shadow-styled dropdown
- .chat-attachment-option - flex row with icon and label, hover highlighting

### Verification
Frontend build succeeds with no errors. No backend changes required.
