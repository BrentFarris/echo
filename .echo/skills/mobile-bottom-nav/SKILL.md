---
name: mobile-bottom-nav
description: Mobile bottom nav layout, responsive CSS for narrow screens (≤720px and ≤380px), tab structure, and single-active-view invariant.
triggers:
    - mobile nav
    - bottom nav
    - view switching
    - workspace dropdown
    - touch target
    - accessibility
    - single active tab
    - narrow screen
    - overflow
    - responsive CSS
---

Mobile bottom nav has seven icon-only tabs (Chat, Kanban, Code, Tasks, Git, Dashboard, Settings) on screens under 720px, plus a workspace pill and "Echo" brand label in `.mobile-nav-brand`.

## Responsive CSS (inside `@media (max-width: 720px)` in `frontend/src/styles.css`)

### Key selectors
- `.mobile-bottom-nav` — fixed bottom flex container
- `.mobile-nav-brand` — left-side brand section (pill + app name), `flex-shrink: 0`
- `.mobile-nav-pill` — workspace name pill with dropdown trigger
- `.mobile-nav-app-name` — "Echo" label text
- `.mobile-nav-tabs` — flex container for the 7 tab buttons, `flex: 1 1 auto`
- `.mobile-nav-tab` — individual tab button

### Narrow-screen adaptations (applied to prevent overflow at 360px viewport)
| Selector | Property | Value | Purpose |
|---|---|---|---|
| `.mobile-nav-brand` | padding | `0 var(--space-md)` | Reduce horizontal padding |
| `.mobile-nav-brand` | gap | `var(--space-md-lg)` | Tighter spacing between pill and label |
| `.mobile-nav-pill` | max-width | 90px | Shrink from 120px |
| `.mobile-nav-pill` | padding | `var(--space-xs) var(--space-md)` | Compact padding |
| `.mobile-nav-pill` | font-size | 0.7rem | Smaller text |
| `.mobile-nav-app-name` | font-size | 0.65rem | Smaller label |
| `.mobile-nav-tab` | min-width | 36px | Reduced from 48px; touch target minimum |
| `.mobile-nav-tab` | padding horizontal | `var(--space-xs)` | Tighter horizontal padding |
| `.mobile-nav-tab svg` | width/height | 18px | Smaller icons from 20px |

### Ultra-narrow breakpoint (`@media (max-width: 380px)`)
- `.mobile-nav-app-name { display: none; }` — hides the redundant "Echo" label to save ~30–40px

### Width math at 360px viewport
- Brand section: ~100px (pill ~90px + gap + app name hidden)
- 7 tabs × 36px min-width = 252px
- Total ≈ 352px, fits within 360px viewport

### State and rendering
- `state.mobileNavView` enforces single-active-tab invariant
- Tab switching calls bindActionEvents after render
- Key files: `types.ts` (MobileNavView), `state.ts`, `render.ts`, `actions.ts`

### Known bugs
- B-029: workspace dropdown transparent background/unselectable items
- B-030: multiple nav tabs can be active simultaneously
