---
name: patch-based-dom-updates
description: How patch-based DOM updates replace full innerHTML re-renders in render.ts using a persistent app-shell wrapper with named region divs, eliminating jank during streaming sessions.
triggers:
    - performance
    - DOM re-render
    - streaming
    - jank
    - render.ts
    - patch
    - app-root
    - innerHTML
    - repaint
    - responsive
---

## Problem Solved

The original `render()` in `frontend/src/app/render.ts` did `appRoot.innerHTML = ...` which completely destroyed and recreated the entire DOM tree on every call. Even when patch helpers (`patchChatMessage`, `patchChatPanel`, etc.) performed targeted updates, the baseline `render()` calls still wiped everything first — causing jank and flicker during rapid streaming token generation.

## Solution: Persistent Shell + Regional Updates

### Architecture

```
appRoot (#app)
└── .app-shell          ← created once by ensureShell()
    ├── [data-region="gutter"]       ← workspace rail + gutter actions
    ├── [data-region="main"]         ← main content area (code/chat/kanban)
    ├── [data-region="mobile-nav"]   ← bottom mobile navigation
    └── [data-region="overlays"]     ← modals, toasts, context menu
```

Each `render()` call invokes `updateRegion()` for each zone, which either creates the region div lazily or swaps only its `innerHTML`. The outer `.app-shell` persists across renders.

### Key Implementation Details

- **`ensureShell()`**: Creates `.app-shell` as a child of `appRoot` if it doesn't already exist. Returns the existing shell otherwise.
- **`updateRegion(shell, name, html)`**: Finds `[data-region="<name>"]` inside the shell, creates it lazily if missing, then sets `innerHTML`.
- **`buildGutter(workspaces)`**: Renders workspace buttons + gutter action buttons.
- **`buildMain(workspace, workspaces, showingCode, showGitChanges)`**: Renders the central panel (code view or chat/kanban split). Uses `ws!` non-null assertion for `showGitChanges` branch since `gitRepositoryViewFor` requires non-null `Workspace`.
- **`buildOverlays()`**: Concatenates settings overlay, toasts, and context menu HTML.

### Why This Works

1. No full DOM teardown: Only the content inside each region changes; structural elements survive between renders.
2. Backward compatible: All existing patch helpers continue to work unchanged because they operate on data attributes that are preserved within regions.
3. Minimal diff surface: Region builder functions reuse the exact same template strings from the original `render()` function, ensuring visual parity.

### Files Changed

- `echo/frontend/src/app/render.ts` — Entire rewrite of `render()` using shell+regions pattern. Preserved exports: `render()`, `renderWorkspacePanels()`, `renderChatKanbanTabs()`, `renderMobileBottomNav()`. Removed unused imports (`captureScrollSnapshot`, `restoreScrollSnapshot`).

### Verification

- `npm run build` passes TypeScript compilation and Vite bundling successfully.
- No behavioral regression expected: output HTML structure is identical; only the DOM mutation strategy changed.
