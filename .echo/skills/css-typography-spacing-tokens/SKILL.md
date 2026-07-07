---
name: css-typography-spacing-tokens
description: CSS typography and spacing design tokens defined in :root of styles.css, replacing all hardcoded font-size and spacing values with semantic variable references.
---

## Design Token System in `echo/frontend/src/styles.css`

### Overview
Typography and spacing values are centralized as CSS custom properties in the `:root` block (both light and dark modes). All hardcoded font-size and padding/margin/gap px values have been replaced with `var(--token)` references, ensuring zero visual regression while enabling future theming changes from a single source of truth.

### Font Scale Tokens (`--text-*`)
Defined in both light and dark `:root`:

| Token | Value | Typical Use |
|-------|-------|-------------|
| `--text-xs` | 0.7rem | Smallest labels, chip subtitles |
| `--text-sm` | 0.72rem | Eyebrows, legal text, timestamps |
| `--text-base-sm` | 0.74rem | Dependencies, badges, git metadata |
| `--text-base-md` | 0.76rem | Tool-call spans, transcript entries |
| `--text-base-lg` | 0.78rem | Table headers, captions, icons |
| `--text-md` | 0.8rem | Debug summaries, code tree sizes |
| `--text-md-lg` | 0.82rem | Button labels, overflow items, links |
| `--text-lg` | 0.84rem | Menu items, commit subjects, search |
| `--text-xl` | 0.86rem | Message bodies, table cells, diffs |
| `--text-xl-lg` | 0.88rem | Rename inputs, inline chat |
| `--text-2xl` | 0.9rem | Panel headings, workspace rows |
| `--text-2xl-lg` | 0.92rem | Editors, markdown body, detail sections |
| `--text-3xl` | 0.98rem | Chat titles, markdown h3-h6 |
| `--text-4xl` | 1rem | h3 elements, dialog headings |
| `--text-4xl-lg` | 1.05rem | Busy status, panel heading strong |
| `--text-5xl` | 1.55rem | h1/h2 headlines |

### Spacing Tokens (`--space-*`)
Applied exclusively within `padding`, `margin`, and `gap` properties:

| Token | Value | Typical Use |
|-------|-------|-------------|
| `--space-xxs` | 2px | Micro gaps, diff line padding |
| `--space-xs` | 3px | Tight sub-elements |
| `--space-sm` | 4px | Icon spacing, checkbox gaps |
| `--space-md-sm` | 5px | Chip padding, subtle offsets |
| `--space-md` | 6px | Default gap for lists, tables |
| `--space-md-lg` | 7px | Tree row gaps, item indents |
| `--space-lg` | 8px | Primary default — cards, messages, panels |
| `--space-lg-sm` | 9px | Kanban card opens, tool calls |
| `--space-lg-md` | 10px | Form grids, rail gaps, gutter actions |
| `--space-lg-lg` | 11px | Compact form padding |
| `--space-xl` | 12px | Section spacing, gutter padding |
| `--space-xl-lg` | 14px | Debug stacks, settings sections |
| `--space-2xl` | 16px | Major layout gaps, backdrops |
| `--space-3xl` | 18px | Gutter main gap, work-panel |
| `--space-4xl` | 20px | Dialog/dialogue padding, settings |
| `--space-5xl` | 22px | Work-panel internal gap |
| `--space-6xl` | 24px | Overlay padding, image canvas |

### Implementation Notes
- **Scope**: Replacements are scoped to specific CSS property names (`font-size`, `padding*`, `margin*`, `gap`). Width, height, border-radius, z-index, box-shadow, transition durations, and other numeric values remain unchanged.
- **Sorting**: Token replacements sort by raw value length descending to prevent partial matches (e.g., `1.55rem` before `1rem`).
- **Dual injection**: Both light and dark `:root` blocks receive identical tokens; color-specific overrides live only in existing color variables.
- **Verification**: `npm run build` passes with no errors or warnings related to the CSS.

### Historical Context
Multiple automated approaches failed due to regex/grouping bugs in Node.js scripts. The successful approach used targeted scoped regex replacements on specific CSS property declarations, verified by confirming zero remaining hardcoded values in those contexts.
