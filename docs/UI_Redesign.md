# UI Redesign — Design Document

> **Status**: Proposed  
> **Date**: 2026-07-01  
> **Scope**: Frontend CSS + TypeScript render functions only; no backend or Go changes required.

---

## 1. Goals

1. Make the mobile experience genuinely usable — thumb-first navigation, full-screen drawers, readable typography.
2. Establish clear visual hierarchy so the eye knows where to land on every screen.
3. Standardize spacing, radii, and touch targets across all components.
4. Improve empty states and first-launch onboarding so new users aren't dropped into a blank screen.
5. Keep existing color tokens, modular render architecture, and backend services untouched.

---

## 2. Design Principles

| Principle | What it means in practice |
|---|---|
| **Thumb-first on mobile** | Primary navigation lives at the bottom of the viewport (≤720px). All interactive targets ≥44×44px. |
| **Progressive disclosure** | Show what matters now; hide complexity behind accordions, sheets, or modals. Tool calls collapse by default. |
| **Hierarchy through space, not borders** | Reduce border clutter. Use whitespace, typography scale, background tints, and subtle accent strokes to create structure. |
| **One primary action per screen** | Every view has an obvious "what should I do here?" — a single prominent button or input. |
| **4px spacing rhythm** | All margins, paddings, and gaps snap to multiples of 4: `4, 8, 12, 16, 24, 32, 48`. No arbitrary values like 10, 11, 14, 18, or 22. |
| **Border radius cascade** | Cards = 12px, inputs/buttons = 8px, small elements = 6px, badges/pills = 999px. Consistent visual language. |

---

## 3. Current State — Pain Points

### 3.1 Mobile Layout (≤720px)

| Problem | Details |
|---|---|
| **Workspace rail is a horizontal scroll strip** | 40×40px icon buttons in a cramped top row. Thumb-unfriendly for the primary navigation pattern. |
| **Chat/Kanban tabs are thin text buttons** | No icons, no pill shape, low contrast against background. Easy to miss or mistap. |
| **Kanban board collapses to unusable single column** | 4 lanes → 1 column of tiny cards with minimal info. Loses all spatial context of status distribution. |
| **No bottom navigation bar** | Primary actions at top or require scrolling down to reach composer. Fights natural thumb position. |
| **Chat composer buried under sticky controls** | `.chat-mobile-controls` sits between log and input with `position: sticky; top: 0`. Visual noise where you want to type. |
| **Card detail drawer is desktop-optimized** | 560px side panel becomes cramped on mobile without adapting internal layout or going full-screen. |
| **Git/Changes drawers don't adapt** | Side panels squeeze into viewport width instead of expanding to full-screen on small devices. |

### 3.2 Desktop Layout (≥721px)

| Problem | Details |
|---|---|
| **No visual hierarchy** | Chat, kanban, actions, headings all compete equally. No clear entry point for the eye. |
| **Rigid split-panel columns** | `minmax(300px, 0.65fr) / minmax(560px, 1.55fr)` is fixed. No drag-to-resize gutter (code view already has this pattern). |
| **Workspace heading row is crowded** | Workspace name and action buttons (Git, Code) compete on the same line. |
| **Tool calls always expanded** | Every tool invocation takes up screen space even when the user only cares about the final result. |

### 3.3 Visual Consistency

| Problem | Details |
|---|---|
| **Inconsistent spacing** | Mixed values: 8, 9, 10, 11, 12, 14, 16, 18, 22px used across components. No unified rhythm. |
| **Uniform border radius** | Everything is 8px. Cards, buttons, badges, inputs — no visual differentiation through shape. |
| **Touch targets too small on mobile** | Some icon buttons are 30×30px or smaller. Below the 44×44px minimum for touch. |
| **Empty states are functional, not inviting** | Dashed borders and muted text don't guide users toward their first action with confidence. |
| **No onboarding** | Launch Echo with no workspaces → blank screen + "Add workspace" icon in a corner. |

---

## 4. Detailed Specifications

### 4.1 Global Layout — Mobile Bottom Navigation Bar

**Trigger**: `≤720px` viewport width. Replaces the current top workspace rail layout.

#### Structure

```
┌──────────────────────────────┐
│ ← [Workspace Name]   ⚙      │  ← .mobile-top-bar (56px tall, fixed)
├──────────────────────────────┤
│                              │
│     Main content area        │  ← .main-content (flex: 1, overflow-y: auto)
│     fills remaining space    │
│                              │
├──────────────────────────────┤
│  [💬]   [▦]   [</>]         │  ← .bottom-nav (56px tall, fixed to bottom)
│  Chat   Board Code           │
└──────────────────────────────┘
```

#### CSS Changes

New selectors added inside `@media (max-width: 720px)`:

```css
/* ── Mobile top bar ───────────────────────────────── */
.mobile-top-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  height: 56px;
  padding: 0 16px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-surface);
}

.mobile-top-bar-title {
  min-width: 0;
  overflow: hidden;
  font-size: 1rem;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;          /* Taps → workspace switcher sheet */
}

/* ── Bottom navigation bar ────────────────────────── */
.bottom-nav {
  display: flex;
  align-items: center;
  justify-content: space-around;
  height: 56px;
  padding: 0 8px;
  border-top: 1px solid var(--color-border);
  background: var(--color-surface);
}

.bottom-nav-tab {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  min-width: 64px;
  min-height: 44px;
  padding: 8px 12px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  font: inherit;
  font-size: 0.7rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  transition: color 120ms ease, background 120ms ease;
}

.bottom-nav-tab svg {
  width: 22px;
  height: 22px;
  fill: none;
  stroke: currentColor;
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 1.8;
}

.bottom-nav-tab:hover {
  color: var(--color-text);
}

.bottom-nav-tab.is-active {
  color: var(--color-accent);
  background: color-mix(in srgb, var(--color-accent) 8%, transparent);
}

/* ── Hide desktop gutter on mobile ────────────────── */
.gutter { display: none; }

/* ── Repurpose .chat-kanban-tabs → hidden (bottom nav replaces it) ── */
.chat-kanban-tabs { display: none !important; }
```

#### State Changes (`state.ts`)

```typescript
// Add to state object:
mobileNavTab: new Map<string, "chat" | "kanban" | "code">(),
workspaceSheetOpen: false,

// New helper:
export function getMobileNavTab(workspaceID: string): "chat" | "kanban" | "code" {
  return state.mobileNavTab.get(workspaceID) ?? "chat";
}
```

#### Render Changes (`render.ts`)

Inside `@media (max-width: 720px)` layout, the app shell becomes:

```html
<div class="app-shell">
  <!-- Mobile top bar replaces gutter -->
  <div class="mobile-top-bar">
    <strong class="mobile-top-bar-title" data-action="open-workspace-sheet">
      ${workspace ? workspace.displayName : "Echo"}
    </strong>
    <button class="icon-button" data-action="open-settings" title="Settings">
      ${icons.settings}
    </button>
  </div>

  <main class="main-content">
    <!-- Content area: chat, kanban, or code based on active mobileNavTab -->
    ${renderMobileContent(workspace)}
  </main>

  <!-- Bottom nav bar -->
  <nav class="bottom-nav" aria-label="Primary navigation">
    <button class="bottom-nav-tab ${active === 'chat' ? 'is-active' : ''}" 
            data-action="set-mobile-nav-tab" data-tab="chat">
      ${icons.message}
      Chat
    </button>
    <button class="bottom-nav-tab ${active === 'kanban' ? 'is-active' : ''}" 
            data-action="set-mobile-nav-tab" data-tab="kanban">
      ${icons.kanban}
      Board
    </button>
    <button class="bottom-nav-tab ${active === 'code' ? 'is-active' : ''}" 
            data-action="set-mobile-nav-tab" data-tab="code">
      ${icons.code}
      Code
    </button>
  </nav>

  <!-- Workspace switcher sheet -->
  ${state.workspaceSheetOpen ? renderWorkspaceSheet(workspaces) : ""}
</div>
```

#### Action Handler (`actions.ts`)

```typescript
case "set-mobile-nav-tab": {
  const tab = target.dataset.tab as "chat" | "kanban" | "code";
  if (!tab || !["chat", "kanban", "code"].includes(tab)) break;
  const ws = activeWorkspace();
  if (ws) state.mobileNavTab.set(ws.id, tab);
  // Sync with existing expanded states for consistency
  if (tab === "code" && ws) {
    state.appMode = "code";
  } else {
    state.appMode = "chat";
  }
  render();
  break;
}

case "open-workspace-sheet": {
  state.workspaceSheetOpen = true;
  patchWorkspaceSheet();  // Lightweight DOM patch, not full re-render
  break;
}
```

**Acceptance criteria:**
- [ ] Bottom nav bar is visible at ≤720px with three tabs: Chat, Board, Code
- [ ] Active tab shows accent color + subtle background pill
- [ ] Tapping workspace name in top bar opens slide-up sheet with all workspaces
- [ ] All interactive targets in bottom nav are ≥44×44px
- [ ] Desktop layout (≥721px) is unchanged

---

### 4.2 Workspace Switcher Sheet

**Trigger**: Tap workspace name in mobile top bar. Slide-up from bottom, dimmed backdrop.

#### Structure

```
┌──────────────────────────────┐
│ ← [Workspace Name]   ⚙      │
├──────────────────────────────┤
│        (dimmed backdrop)     │
│                              │
│  ┌────────────────────────┐  │
│  │ ← Workspaces           │  │  ← Sheet header with back button
│  ├────────────────────────┤  │
│  │ [A] Alpha Project      │  │  ← Full-width workspace rows
│  │   /path/to/alpha       │  │     with icon, name, path
│  ├────────────────────────┤  │
│  │ [B] Beta Repo          │  │
│  │   /path/to/beta        │  │
│  ├────────────────────────┤  │
│  │ + Add workspace        │  │  ← Sticky add button at bottom
│  └────────────────────────┘  │
└──────────────────────────────┘
```

#### CSS

```css
.workspace-sheet-backdrop {
  position: fixed;
  inset: 0;
  z-index: 25;
  display: grid;
  justify-items: end;
  align-items: end;
  padding: 0;
  background: rgba(18, 18, 20, 0.42);
  backdrop-filter: blur(6px);
}

.workspace-sheet {
  width: 100%;
  max-height: 70vh;
  display: grid;
  grid-template-rows: auto minmax(0, 1fr) auto;
  border-top-left-radius: 12px;
  border-top-right-radius: 12px;
  background: var(--color-surface);
  box-shadow: var(--shadow-modal);
}

.workspace-sheet-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px;
  border-bottom: 1px solid var(--color-border);
}

.workspace-sheet-header strong {
  font-size: 1rem;
  font-weight: 700;
}

.workspace-sheet-list {
  display: grid;
  gap: 2px;
  overflow-y: auto;
  padding: 8px;
}

.workspace-sheet-item {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  min-height: 56px;
  padding: 12px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text);
  cursor: pointer;
  text-align: left;
}

.workspace-sheet-item:hover,
.workspace-sheet-item.is-active {
  background: color-mix(in srgb, var(--color-accent) 8%, transparent);
  border-color: color-mix(in srgb, var(--color-accent) 24%, transparent);
}

.workspace-sheet-item-icon {
  flex: 0 0 auto;
  width: 36px;
  height: 36px;
  border-radius: 8px;
  overflow: hidden;
  border: 1px solid var(--color-border);
  background: var(--color-bg);
  color: var(--color-text-muted);
  display: grid;
  place-items: center;
  font-size: 0.72rem;
  font-weight: 800;
}

.workspace-sheet-item-main {
  min-width: 0;
  display: grid;
  gap: 2px;
}

.workspace-sheet-item-main strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 0.92rem;
}

.workspace-sheet-item-main span {
  overflow: hidden;
  color: var(--color-text-muted);
  font-size: 0.78rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workspace-sheet-add {
  padding: 12px 16px;
  border-top: 1px solid var(--color-border);
}
```

**Acceptance criteria:**
- [ ] Sheet slides up from bottom with dimmed backdrop
- [ ] Each workspace shows icon, name, and path preview in a ≥56px row
- [ ] Active workspace is highlighted with accent background
- [ ] "Add workspace" button at bottom of sheet
- [ ] Tapping backdrop or back button dismisses sheet

---

### 4.3 Kanban Board — Mobile Accordion Layout

**Trigger**: `≤720px` viewport width, kanban tab active. Replaces single-column card list.

#### Structure

```
┌──────────────────────────────┐
│ ▾ Ready (3)                 │  ← Lane header: tap to expand/collapse
│   ┌───────────────────────┐  │
│   │ Refactor auth module  │  │  ← Card: title + status badge
│   │ [dependency]          │  │     Tap card → full-screen detail
│   └───────────────────────┘  │
│   ┌───────────────────────┐  │
│   │ Add rate limiting     │  │
│   └───────────────────────┘  │
│ ▸ In Progress (1)            │  ← Collapsed lane: just header + count
│ ▸ Blocked (0)                │
│ ▾ Done (5)                   │
├──────────────────────────────┤
│  [💬]   [▦]   [</>]         │
└──────────────────────────────┘
```

#### CSS Changes

Inside `@media (max-width: 720px)`:

```css
/* ── Kanban board: accordion mode on mobile ─────── */
.kanban-board {
  display: grid;
  gap: 8px;
  grid-template-columns: 1fr;   /* Override 4-column → single column of lanes */
}

.kanban-lane {
  border: 0;
  border-radius: 12px;
  background: transparent;
  padding: 0;
  min-height: auto;
}

.kanban-lane > header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  min-height: 48px;
  padding: 12px 16px;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-bg);
  cursor: pointer;
}

/* Lane color coding via left accent bar */
.kanban-lane[data-lane="ready"] > header {
  border-left: 3px solid var(--color-accent);
}
.kanban-lane[data-lane="inProgress"] > header {
  border-left: 3px solid var(--color-warning);
}
.kanban-lane[data-lane="blocked"] > header {
  border-left: 3px solid var(--color-danger);
}
.kanban-lane[data-lane="done"] > header {
  border-left: 3px solid var(--color-success);
}

.kanban-lane > header strong {
  font-size: 0.86rem;
}

/* Chevron indicator for expand/collapse */
.kanban-lane > header .lane-chevron {
  display: grid;
  place-items: center;
  width: 20px;
  height: 20px;
  color: var(--color-text-muted);
  transition: transform 120ms ease;
}

.kanban-lane.is-expanded > header .lane-chevron {
  transform: rotate(90deg);
}

/* Cards inside expanded lane */
.kanban-cards {
  gap: 6px;
  padding: 8px 0 0;
}

.kanban-lane:not(.is-expanded) .kanban-cards {
  display: none;
}

/* Card styling on mobile */
.kanban-card {
  border-radius: 10px;
  padding: 0;
}

.kanban-card-open {
  padding: 14px 16px;
}

.kanban-card header strong {
  font-size: 0.92rem;
  line-height: 1.35;
}
```

#### Render Changes (`kanban/index.ts`)

Each lane gets `data-lane="${laneKey}"` and a chevron in the header:

```html
<div class="kanban-lane ${isExpanded ? 'is-expanded' : ''}" data-lane="${laneKey}">
  <header data-action="toggle-lane" data-lane="${laneKey}">
    <div>
      <strong>${laneLabel(laneKey)}</strong>
      <span>${cards.length}</span>
    </div>
    <span class="lane-chevron">${icons.chevronRight}</span>
  </header>
  <div class="kanban-cards">
    ${cards.map(renderKanbanCard).join("")}
  </div>
</div>
```

State tracks expanded lanes per workspace: `expandedKanbanLanes: Map<string, Set<string>>()`.

**Acceptance criteria:**
- [ ] Lanes appear as accordion headers with left color-coded accent bar
- [ ] Tap lane header to expand/collapse cards within
- [ ] Each card is ≥56px tall with title and metadata visible
- [ ] Lane count badge shows number of cards
- [ ] Chevron rotates 90° when expanded

---

### 4.4 Chat Panel Improvements

#### 4.4.1 Message Visual Hierarchy

**Current**: User messages get `margin-left: 26px` + blue-tinted background. Assistant messages get `margin-right: 18px`. Distinction is subtle.

**Proposed**:

```css
.chat-message.from-user {
  margin-left: 32px;
  border-color: color-mix(in srgb, var(--color-info) 40%, var(--color-border));
  background: color-mix(in srgb, var(--color-info) 10%, var(--color-surface));
  border-left: 3px solid color-mix(in srgb, var(--color-info) 50%, transparent);
}

.chat-message.from-assistant {
  margin-right: 24px;
  border-left: 3px solid color-mix(in srgb, var(--color-text-muted) 20%, transparent);
}
```

The left accent bar makes user vs assistant immediately scannable without reading the header.

#### 4.4.2 Tool Call Accordion

**Current**: Every tool call renders expanded with parameters and output visible.

**Proposed**: Tool calls render as collapsible accordions, collapsed by default after streaming completes. During streaming they stay open.

```html
<div class="tool-call ${completed ? 'is-collapsed' : ''}">
  <div data-action="toggle-tool-call" data-tool-id="${toolId}">
    <strong>${toolName}</strong>
    <span>${statusLabel}</span>
    <span class="lane-chevron">${icons.chevronRight}</span>
  </div>
  <div class="tool-call-body">
    <!-- parameters and output -->
  </div>
</div>
```

```css
.tool-call.is-collapsed .tool-call-body { display: none; }
.tool-call:not(.is-collapsed) .lane-chevron { transform: rotate(90deg); }
```

#### 4.4.3 Streaming Indicator

**Current**: Spinner in the chat heading area. Subtle and easy to miss when scrolling.

**Proposed**: Add a streaming indicator bar at the bottom of the last assistant message content:

```css
.streaming-indicator {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  color: var(--color-accent);
  font-size: 0.78rem;
  font-weight: 700;
}

.streaming-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--color-accent);
  animation: echo-pulse 1.2s ease-in-out infinite;
}

@keyframes echo-pulse {
  0%, 100% { opacity: 0.3; transform: scale(0.85); }
  50% { opacity: 1; transform: scale(1); }
}
```

#### 4.4.4 Mobile Message Actions

**Current**: Hover-reveal buttons (copy, retry, edit, prune) — inaccessible on touch.

**Proposed**: On mobile (`≤720px`), message action buttons are always visible at reduced opacity:

```css
@media (max-width: 720px) {
  .chat-message header .icon-button {
    opacity: 0.5;
    width: 36px;
    height: 36px;
  }

  .chat-message:hover header .icon-button,
  .chat-message header .icon-button:focus-visible {
    opacity: 1;
  }
}
```

#### 4.4.5 Mobile Composer Bar

**Current**: `.chat-mobile-controls` is a sticky bar between log and composer. Separated visually from the input.

**Proposed**: Integrate controls into the composer area as a compact top bar within the same container:

```css
@media (max-width: 720px) {
  .chat-composer {
    display: grid;
    grid-template-rows: auto 1fr;
    gap: 8px;
    padding-top: 8px;
    border-top: 1px solid var(--color-border);
    background: var(--color-surface);
  }

  .chat-mobile-controls {
    position: static;     /* Remove sticky */
    border-bottom: 0;
    justify-content: flex-end;
  }
}
```

**Acceptance criteria:**
- [ ] User messages have visible left accent bar distinguishing them from assistant
- [ ] Tool calls are collapsed by default after streaming completes
- [ ] Streaming indicator shows pulsing dot at bottom of active message
- [ ] Message action buttons always visible on mobile (not hover-only)
- [ ] Mobile composer controls integrated into composer container, not separate sticky bar

---

### 4.5 Card Detail — Full-Screen on Mobile

**Current**: 560px side drawer with backdrop blur. On mobile this squeezes content.

**Proposed**: On `≤720px`, card detail goes full-screen with a top bar containing back button:

```css
@media (max-width: 720px) {
  .card-detail-backdrop {
    padding: 0;
    background: transparent;
    backdrop-filter: none;
  }

  .card-detail {
    width: 100%;
    max-height: 100dvh;
    border: 0;
    border-radius: 0;
    padding-top: 56px;     /* Space for top bar */
  }

  .card-detail-header {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 12;
    margin: 0;
    padding: 12px 16px;
    border-bottom: 1px solid var(--color-border);
    background: var(--color-surface);
  }
}
```

Same pattern applies to `.change-review-backdrop` / `.change-review` and `.git-repository` drawers.

**Acceptance criteria:**
- [ ] Card detail fills entire viewport on mobile
- [ ] Back button in fixed top bar, not floating
- [ ] Content scrolls naturally within the full-screen container
- [ ] Same pattern for Git changes and change review drawers

---

### 4.6 Visual Polish — Spacing & Radius Cascade

#### Border Radius Map

| Element type | Current | Proposed | CSS variable |
|---|---|---|---|
| Cards, panels, modals | 8px | 12px | `--radius-lg: 12px` |
| Inputs, buttons, lanes | 8px | 8px | `--radius-md: 8px` |
| Small elements, tree rows | 8px / 7px | 6px | `--radius-sm: 6px` |
| Badges, pills, status dots | 999px | 999px | (unchanged) |

Add to `:root`:

```css
:root {
  --radius-lg: 12px;
  --radius-md: 8px;
  --radius-sm: 6px;
}
```

Then replace hardcoded `border-radius: 8px` values with the appropriate variable based on element type.

#### Spacing Rhythm Audit

Replace all non-4-multiple spacing values:

| Current value | Replace with | Where used |
|---|---|---|
| 9px | 8px | `.chat-message > gap`, `.kanban-card-open > gap` |
| 10px | 8px or 12px | Various gaps, padding |
| 11px | 12px | Input padding, textarea padding |
| 14px | 12px or 16px | Various gaps |
| 18px | 16px or 24px | Gutter padding, work-panel padding |
| 22px | 24px | `.work-panel > gap` |

#### Touch Target Minimums (Mobile)

```css
@media (max-width: 720px) {
  .icon-button {
    min-width: 44px;
    min-height: 44px;
  }

  .gutter-button {
    width: 44px;
    height: 44px;
  }

  .status-button,
  .primary-button,
  .secondary-button,
  .compact-button {
    min-height: 44px;
    padding-block: 8px;
  }
}
```

#### Typography Scale (Mobile)

```css
@media (max-width: 720px) {
  body {
    font-size: 1rem;        /* Up from default inherited */
    line-height: 1.5;
  }

  .markdown-body,
  .chat-message p,
  .detail-section p {
    font-size: 1rem;        /* Up from 0.92rem */
    line-height: 1.5;
  }

  h1 { font-size: 1.75rem; }  /* Up from 1.55rem → but only on mobile where space allows */
  h2 { font-size: 1.35rem; }
  h3 { font-size: 1.05rem; }
}
```

**Acceptance criteria:**
- [ ] Three border-radius variables used consistently across all components
- [ ] No spacing values that aren't multiples of 4
- [ ] All interactive elements on mobile are ≥44×44px
- [ ] Body text is 1rem on mobile for readability

---

### 4.7 Empty States & Onboarding

#### 4.7.1 First Launch — No Workspaces

**Current**: Blank `.main-content` with a `+` icon in the gutter. User sees nothing actionable.

**Proposed**: Render an onboarding card when `workspaces.length === 0`:

```html
<div class="onboarding-card">
  <div class="onboarding-icon">${icons.rocket}</div>
  <h1>Welcome to Echo</h1>
  <p>AI-powered development assistant. Get started by adding your first workspace.</p>
  <button class="primary-button" data-action="add-workspace">
    ${icons.plus} Add Workspace
  </button>
  <div class="onboarding-features">
    <div class="onboarding-feature">
      <strong>Chat with AI</strong>
      <span>Ask questions about your codebase and get actionable answers.</span>
    </div>
    <div class="onboarding-feature">
      <strong>Plan & Execute</strong>
      <span>Break plans into Kanban cards. Run agents to do the work.</span>
    </div>
    <div class="onboarding-feature">
      <strong>Code Navigation</strong>
      <span>Browse, edit, and search code with LSP-powered intelligence.</span>
    </div>
  </div>
</div>
```

```css
.onboarding-card {
  display: grid;
  place-items: center;
  gap: 24px;
  width: min(560px, calc(100% - 32px));
  padding: 48px 32px;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
  box-shadow: var(--shadow-soft);
  text-align: center;
}

.onboarding-icon {
  width: 64px;
  height: 64px;
  color: var(--color-accent);
}

.onboarding-icon svg {
  width: 100%;
  height: 100%;
}

.onboarding-features {
  display: grid;
  gap: 16px;
  text-align: left;
}

.onboarding-feature strong {
  display: block;
  font-size: 0.92rem;
  margin-bottom: 4px;
}

.onboarding-feature span {
  color: var(--color-text-muted);
  font-size: 0.86rem;
  line-height: 1.45;
}
```

#### 4.7.2 Empty Chat (Workspace Selected)

**Current**: Generic "Start a conversation" empty state.

**Proposed**: Contextual suggestions that guide the user:

```html
<div class="chat-empty">
  <div class="empty-state board-empty">
    <strong>What would you like to work on?</strong>
    <span>Ask Echo about this workspace, plan a feature, or review recent changes.</span>
  </div>
</div>
```

#### 4.7.3 Empty Kanban Board

**Current**: Dashed border box saying "No cards yet."

**Proposed**: Action-oriented empty state:

```html
<div class="empty-state board-empty">
  <strong>No cards yet</strong>
  <span>Chat with Echo and use "Execute Plan" to decompose visible chat into tasks.</span>
</div>
```

**Acceptance criteria:**
- [ ] First launch shows onboarding card with add workspace CTA and feature highlights
- [ ] Empty chat shows contextual suggestions, not generic placeholder
- [ ] Empty kanban board explains how to create cards from chat
- [ ] All empty states use the `border-radius: 12px` card style

---

### 4.8 Desktop Refinements

#### 4.8.1 Resizable Split Panels

**Current**: Fixed `grid-template-columns: minmax(300px, 0.65fr) minmax(560px, 1.55fr)`.

**Proposed**: Add a drag-to-resize gutter between chat and kanban panels (reusing the code view `.code-resizer` pattern):

```html
<div class="split-panels">
  <section class="work-panel chat-panel">...</section>
  <div class="panel-resizer" data-action="resize-split-panels"></div>
  <section class="work-panel kanban-panel">...</section>
</div>
```

```css
.panel-resizer {
  width: 8px;
  cursor: col-resize;
  background: transparent;
  transition: background 120ms ease;
}

.panel-resizer:hover,
.panel-resizer.is-dragging {
  background: var(--color-accent);
}

.split-panels.is-resizable {
  grid-template-columns: minmax(300px, auto) 8px minmax(400px, 1fr);
}
```

#### 4.8.2 Workspace Heading Reorganization

**Current**: Workspace name and action buttons on same line.

**Proposed**: Two-row layout with workspace name prominent and actions in a secondary toolbar:

```html
<div class="workspace-heading">
  <div class="workspace-heading-main">
    <strong id="workspace-title">${displayName}</strong>
    <span class="heading-path">${path}</span>
  </div>
  <div class="workspace-heading-toolbar">
    <button class="secondary-button icon-text-button" data-action="open-git-changes">
      ${icons.git} <span>Git</span>
    </button>
    <button class="secondary-button icon-text-button" data-action="open-code-view">
      ${icons.code} <span>Code</span>
    </button>
  </div>
</div>
```

```css
.workspace-heading {
  display: grid;
  gap: 8px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--color-border);
}

.workspace-heading-toolbar {
  display: flex;
  gap: 8px;
}
```

#### 4.8.3 Lane Header Color Coding (Desktop)

Same left accent bar pattern as mobile, but applied to the existing horizontal lane layout:

```css
@media (min-width: 721px) {
  .kanban-lane[data-lane="ready"] { border-top: 3px solid var(--color-accent); }
  .kanban-lane[data-lane="inProgress"] { border-top: 3px solid var(--color-warning); }
  .kanban-lane[data-lane="blocked"] { border-top: 3px solid var(--color-danger); }
  .kanban-lane[data-lane="done"] { border-top: 3px solid var(--color-success); }
}
```

**Acceptance criteria:**
- [ ] Split panels have optional drag-to-resize gutter between chat and kanban
- [ ] Workspace heading shows name prominently with action buttons on secondary toolbar below
- [ ] Kanban lanes have color-coded top accent bars for instant status scanning

---

### 4.9 Settings — Mobile Full-Screen

**Current**: Modal overlay with side-nav + content grid. On mobile the nav scrolls horizontally (partially implemented) but content is still cramped.

**Proposed**: On `≤720px`, settings goes full-screen (not a modal). Nav becomes horizontal pill bar at top.

```css
@media (max-width: 720px) {
  .overlay { padding: 0; background: transparent; }

  .settings-panel {
    width: 100%;
    max-height: 100dvh;
    height: 100dvh;
    border: 0;
    border-radius: 0;
    padding-top: 56px;     /* Space for top bar */
    box-shadow: none;
  }

  .settings-header {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    z-index: 22;
    padding: 12px 16px;
    border-bottom: 1px solid var(--color-border);
    background: var(--color-surface);
  }

  .settings-layout {
    grid-template-columns: 1fr;
  }

  .settings-nav {
    position: sticky;
    top: 56px;
    z-index: 2;
    margin: 0;
    padding: 8px 16px;
    overflow-x: auto;
    background: var(--color-surface);
    border-bottom: 1px solid var(--color-border);
  }

  .settings-nav ul {
    display: flex;
    gap: 4px;
    width: max-content;
  }

  .settings-nav button {
    min-height: 36px;
    padding: 8px 14px;
    border-radius: 999px;
    white-space: nowrap;
  }

  .settings-nav button.is-active {
    background: color-mix(in srgb, var(--color-accent) 12%, transparent);
    color: var(--color-accent);
  }
}
```

**Acceptance criteria:**
- [ ] Settings fills entire viewport on mobile (not a modal overlay)
- [ ] Nav is horizontal scrollable pill bar below fixed header
- [ ] Content scrolls naturally within full-screen container
- [ ] Active nav item shows accent-colored pill background

---

## 5. Implementation Phases

### Phase 1 — Mobile Foundation (Highest Impact)

| # | Task | Files | Estimated complexity |
|---|---|---|---|
| 1.1 | Bottom navigation bar + mobile top bar | `styles.css`, `render.ts`, `state.ts`, `actions.ts`, `icons.ts` | Medium |
| 1.2 | Workspace switcher slide-up sheet | `styles.css`, `render.ts`, `state.ts`, `actions.ts` | Medium |
| 1.3 | Kanban accordion layout for mobile | `styles.css`, `kanban/index.ts`, `state.ts`, `actions.ts` | Medium |
| 1.4 | Card detail full-screen on mobile | `styles.css` | Low |
| 1.5 | Chat message actions always-visible on mobile | `styles.css` | Low |

**Exit criteria**: Mobile experience is genuinely usable. Navigation is thumb-friendly, kanban is scannable, card details are readable.

### Phase 2 — Visual Polish

| # | Task | Files | Estimated complexity |
|---|---|---|---|
| 2.1 | Border radius cascade + CSS variables | `styles.css` | Low |
| 2.2 | Spacing rhythm standardization (4px grid) | `styles.css` | Medium (audit-heavy) |
| 2.3 | Touch target minimums on mobile | `styles.css` | Low |
| 2.4 | Typography scale bump on mobile | `styles.css` | Low |
| 2.5 | Lane header color coding (desktop + mobile) | `styles.css`, `kanban/index.ts` | Low |
| 2.6 | Streaming indicator in chat | `styles.css`, `chat/index.ts` | Low |

**Exit criteria**: Visual consistency across all components. No arbitrary spacing values. Touch targets meet minimums.

### Phase 3 — Chat & Desktop Refinements

| # | Task | Files | Estimated complexity |
|---|---|---|---|
| 3.1 | Tool call accordion (collapse by default) | `styles.css`, `chat/index.ts` | Medium |
| 3.2 | Mobile composer integration | `styles.css` | Low |
| 3.3 | Message visual hierarchy (left accent bars) | `styles.css` | Low |
| 3.4 | Resizable split panels on desktop | `styles.css`, `render.ts`, `actions.ts` | Medium |
| 3.5 | Workspace heading reorganization | `styles.css`, `render.ts` | Low |

**Exit criteria**: Chat is cleaner and more scannable. Desktop layout is more flexible.

### Phase 4 — Empty States, Onboarding & Settings

| # | Task | Files | Estimated complexity |
|---|---|---|---|
| 4.1 | First-launch onboarding card | `styles.css`, `render.ts` | Low |
| 4.2 | Contextual empty chat state | `chat/index.ts`, `styles.css` | Low |
| 4.3 | Empty kanban board CTA | `kanban/index.ts`, `styles.css` | Low |
| 4.4 | Settings full-screen on mobile | `styles.css`, `settings/index.ts` | Medium |

**Exit criteria**: No blank screens. Every empty state guides the user toward action. Settings is usable on phone.

---

## 6. File Change Inventory

| File | Phase | Type of change |
|---|---|---|
| `frontend/src/styles.css` | 1–4 | New mobile bottom nav styles, kanban accordion, radius cascade, spacing audit, touch targets, typography, lane colors, streaming indicator, settings full-screen, onboarding card |
| `frontend/src/app/render.ts` | 1, 3, 4 | Mobile top bar + bottom nav structure, workspace sheet render, resizable split panels, heading reorganization, onboarding card |
| `frontend/src/app/state.ts` | 1 | `mobileNavTab` map, `workspaceSheetOpen` boolean, `expandedKanbanLanes` map, helper function |
| `frontend/src/app/actions.ts` | 1 | `set-mobile-nav-tab`, `open-workspace-sheet`, `toggle-lane`, `resize-split-panels` handlers |
| `frontend/src/app/icons.ts` | 1 | Add `message`, `kanban`, `chevronRight`, `rocket` icons if not present |
| `frontend/src/app/chat/index.ts` | 2, 3 | Streaming indicator render, tool call accordion toggle, mobile composer CSS class |
| `frontend/src/app/kanban/index.ts` | 1, 2 | Lane `data-lane` attribute, chevron in header, expanded lane state |
| `frontend/src/app/settings/index.ts` | 4 | Mobile full-screen layout adjustments in render function |

---

## 7. What Stays the Same

- **Color tokens** — Current palette (warm neutrals + blue accent) works well in both light and dark modes. No changes.
- **Modular render architecture** — Frameworkless TypeScript with render functions. This is a strength, not a weakness.
- **Backend services** — Zero Go code changes. All UI state is frontend-managed.
- **Wails bindings** — No new backend methods required; no `wails generate` needed.
- **Event system** — Existing `echo:chat:event` and `echo:kanban:event` SSE patterns remain unchanged.
- **Data model** — Kanban cards, chat sessions, settings persist format is untouched.

---

## 8. Verification Strategy

### Automated
```powershell
# TypeScript compilation catches structural errors
cd frontend; npm run build

# Backend tests unaffected (no Go changes)
go test ./...
```

### Manual (per phase)

| Phase | Test procedure |
|---|---|
| **1** | Resize browser to 375px width. Verify bottom nav is visible with 3 tabs. Tap workspace name → sheet opens. Tap kanban tab → accordion lanes appear. Tap card → full-screen detail. |
| **2** | Visual inspection: all cards have 12px radius, inputs 8px, badges 999px. No spacing values that aren't multiples of 4. All buttons on mobile are ≥44×44px. Body text is readable at 1rem. |
| **3** | Chat: tool calls are collapsed after streaming. Streaming indicator pulses during active response. Desktop: drag split-panel resizer works. Workspace heading has two-row layout. |
| **4** | Launch with no workspaces → onboarding card appears. Empty chat shows suggestions. Empty kanban explains how to create cards. Settings goes full-screen on mobile. |

### Regression checks
- [ ] Desktop layout (≥721px) is visually identical before/after (except for lane colors and heading reorg which are additive improvements)
- [ ] Existing `is-chat-expanded` / `is-kanban-expanded` classes still work on desktop
- [ ] Chat scroll restoration works after mobile nav tab switches
- [ ] Kanban card creation dialog works on both desktop and mobile
- [ ] Git changes drawer works on both desktop and mobile
- [ ] Code view is accessible via bottom nav Code tab on mobile
- [ ] All existing `data-action` handlers continue to fire correctly

---

## 9. Risks & Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| **CSS audit is large and error-prone** | Spacing/radius changes could break visual alignment in unexpected places | Phase 2 is split into small, verifiable sub-tasks. Each values change is committed separately with visual verification. |
| **Mobile state management complexity** | `mobileNavTab`, `workspaceSheetOpen`, `expandedKanbanLanes` add runtime state | All state is transient (not persisted). Simple Maps/booleans with clear lifecycle tied to workspace activation. |
| **Bottom nav breaks existing event handlers** | Some `data-action` handlers assume desktop DOM structure | Handlers use `querySelectorAll` on action selectors; bottom nav uses same `data-action` pattern. Verify all handlers work in both layouts. |
| **Full-screen drawers trap keyboard focus** | Accessibility regression on mobile | Trap focus within full-screen panels. Escape key dismisses. Back button visible and keyboard-focusable. |
| **Wails desktop window vs browser testing** | Wails has a minimum window size; mobile breakpoints may not trigger in desktop app | Test via web access server (`web_access.go`) on actual mobile devices or Chrome DevTools device emulation. Desktop app changes verified at ≥721px. |

---

## 10. Glossary

| Term | Definition |
|---|---|
| **Bottom nav** | Fixed navigation bar at the bottom of the viewport on mobile, replacing the desktop sidebar rail |
| **Workspace sheet** | Slide-up overlay for switching workspaces on mobile, triggered by tapping the workspace name |
| **Accordion lane** | Kanban lane that collapses to a header-only row until tapped, showing cards within when expanded |
| **4px rhythm** | Spacing system where all margins, paddings, and gaps are multiples of 4 pixels |
| **Radius cascade** | Border radius hierarchy: 12px (cards), 8px (inputs/buttons), 6px (small elements), 999px (badges) |
| **Mobile top bar** | Compact 56px header on mobile showing workspace name and settings icon |
