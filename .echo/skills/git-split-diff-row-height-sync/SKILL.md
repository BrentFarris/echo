---
name: git-split-diff-row-height-sync
description: 'Split diff row height sync: keeps left/right panes aligned when content wraps differently by pairing rows via data attribute and equalizing heights with ResizeObserver.'
triggers:
    - split diff misalignment
    - git diff split view line spacing
    - side-by-side diff row height
    - split diff scroll sync
    - git diff pane alignment
---

## Split Diff Row Height Sync

The git split (side-by-side) diff renders rows into two independent panes (left "Before", right "After"). When text wraps differently between columns, rows get different heights and lines misalign.

### Architecture

- **`renderGitSplitDiff()`** in `frontend/src/app/changes/index.ts` generates paired rows from `gitSplitDiffRows()`.
- Each row gets a `data-git-split-row-index="${index}"` attribute so corresponding left/right rows can be matched.
- **`syncGitSplitDiffRowHeights(diff)`** in `frontend/src/app/git/index.ts` pairs rows by index, measures both heights via `getBoundingClientRect().height`, and sets each to `max(leftH, rightH)`.
- A `ResizeObserver` on both pane scroll containers (`[data-git-split-scroll="left"]` and `[data-git-split-scroll="right"]`) triggers re-sync when content resizes (font load, window resize).

### Key selectors
- Split diff container: `[data-git-split-diff]`
- Pane scroll: `[data-git-split-scroll="left"]` / `["right"]`
- Rows: `[data-git-split-row-index]` inside `.git-split-diff-pane-rows`

### CSS notes
- `.git-split-diff-row` has `min-height: 1.45em` and `white-space: pre`.
- Explicit `height` style on rows overrides `min-height` — this is intentional for multi-line wrapped content.
- `.git-split-diff-pane-rows` is `display: grid`, so explicit heights create equal-height implicit grid tracks.

### Binding
`bindGitSplitDiffScroll()` sets up the ResizeObserver and initial height sync after each render. Called from `bindActionEvents()` in `app/git/index.ts`.
