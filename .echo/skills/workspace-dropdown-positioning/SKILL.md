---
name: workspace-dropdown-positioning
description: Workspace dropdown CSS positioning anchored to right edge of sidebar for inward opening.
triggers:
    - workspace dropdown
---

## Workspace Dropdown Positioning

The workspace selection dropdown (`.workspace-dropdown` in `frontend/src/styles.css`) is anchored to the **right edge** of its parent sidebar element using `right: 0`.

### Why right-anchored
The sidebar is a narrow left panel. A centered dropdown (`left: 50%; transform: translateX(-50%)`) can extend past the right side of the viewport on small screens or when many workspaces are listed. Anchoring to `right: 0` keeps the popup opening inward toward the main content area, ensuring it stays fully visible.

### Current rules (as of B-032)
```css
.workspace-dropdown {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  /* no transform */
}
```

### Related
- Mobile-specific dropdown fixes: see skills `mobile-workspace-dropdown-bugfix`, `mobile-workspace-dropdown-pointer-fix`, `mobile-dropdown-surface-color`.
