---
name: mobile-dropdown-surface-color
description: 'Mobile workspace dropdown background color should use var(--color-surface) instead of var(--color-surface-elevated, #ffffff) for proper theming.'
triggers:
    - mobile nav
    - dropdown
    - workspace selector
    - CSS variables
    - dark mode
    - theming
    - surface color
---

## Mobile Workspace Dropdown Background Color

The .mobile-nav-workspace-dropdown rule inside the @media (max-width: 720px) block in frontend/src/styles.css should use var(--color-surface) for its background property.

Wrong: background: var(--color-surface-elevated, #ffffff);
Right: background: var(--color-surface);

Using var(--color-surface) ensures the dropdown respects the current theme (light/dark mode), whereas #ffffff hardcodes white which breaks dark mode visibility.

### Location
- File: frontend/src/styles.css
- Selector: .mobile-nav-workspace-dropdown (inside @media (max-width: 720px))
- Property: background
