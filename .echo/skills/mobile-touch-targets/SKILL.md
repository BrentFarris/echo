---
name: mobile-touch-targets
description: 'Enforcing 44px minimum touch targets for interactive elements on mobile screens via CSS in @media (max-width: 720px).'
triggers:
    - mobile UI
    - touch target
    - accessibility
    - responsive design
    - CSS media query
    - icon-button
    - status-button
---

## Mobile Touch Target Enforcement

### What was done
Added CSS rules inside @media (max-width: 720px) in frontend/src/styles.css to enforce 44x44px minimum hit area on mobile screens:

.icon-button {
  min-width: 44px;
  min-height: 44px;
  touch-action: manipulation;
}

.status-button {
  min-height: 44px;
  touch-action: manipulation;
}

### Key details
- Placed just before closing } of the @media (max-width: 720px) block (line ~5448).
- Uses min-width/min-height so existing larger dimensions are preserved while preventing undersized taps.
- touch-action: manipulation disables double-tap-to-zoom on buttons.
- Separate @media (hover: none), (pointer: coarse) block already forces .chat-message header .icon-button to 44px for always-visible chat action buttons.
- No horizontal scroll risk since min-* only enlarges.

### Verification
npm run build passes cleanly.

### Invariant
Rules only apply at <=720px viewport width, leaving desktop layouts untouched.
