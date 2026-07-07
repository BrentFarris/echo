---
name: backlog-management
description: "Full sprint lifecycle: marking backlog items done, selecting eligible items for sprints, decomposing into Kanban cards with acceptance criteria, boarding cards via Wails bindings, and closing out completed items."
triggers:
    - backlog
    - sprint
    - mark done
    - planning
    - card creation
    - decompose
    - board items
    - kanban cards
    - acceptance criteria
---

# Sprint Planning Workflow

Backlog lives in `.echo/backlog.md`. Items tracked with columns: ID, Title, Priority, Status, Kind, Tasks, Dependencies, Source, Description.

## Marking Items Done

Use `filesystem_edit_text` with narrow match on row header only (avoid full row matching due to encoding issues):

```
oldText: B-NNN | <title prefix> | P<N> | backlog | -
newText: B-NNN | <title prefix> | P<N> | done | story|epic|-
```

Always verify with `filesystem_read_text` after editing.

## Sprint Eligibility Rules

- Only items with status "backlog" and satisfied dependencies qualify for scheduling.
- P0/P1 items take priority over P2+.
- CSS-only fixes (styles.css) are typically single-story tasks.
- Backend Go-only changes touching one file are stories; cross-subsystem changes may be epics.
- Items with dependencies must have all listed dependency IDs resolved (status "done") before inclusion.

## Creating Cards from Backlog (Sprint Kickoff)

When asked to "plan a sprint" or "board backlog items", follow this sequence:

### Step 1 - Select Eligible Items

Read `.echo/backlog.md` and filter rows where:
- Status column equals "backlog"
- All entries in Dependencies column correspond to items marked "done"
- Prioritize by P0 > P1 > P2 order within eligible set

Present selected items to user with titles and priorities before proceeding. Ask if any should be excluded.

### Step 2 - Decompose Each Item into Kanban Cards

For each backlog item representing work beyond a single atomic change, break into focused Kanban cards. Use structural rules from decomposition engine (`internal/services/decomposition.go`):

Each Kanban card must contain:
- **id**: Unique identifier like "card-1", "card-2", etc.
- **title**: Short imperative phrase describing the programming task (e.g., "Update mobile nav z-index for modals").
- **description**: One or two concrete sentences about what code or behavior changes. Include specific selectors, function names, or files when known.
- **acceptanceCriteria**: Array of observable outcomes (not process steps). Examples: "Modal content no longer overlaps bottom nav at <=720px".
- **dependencies**: Array of other card ids from this same batch that must complete first (omit if none).

Card creation constraints:
- A card must represent isolated programming work completable by an agent using workspace tools.
- Prefer fewer, larger cards - split only for independently useful changes or true ordering requirements.
- Do NOT create cards for: opening/navigating/read-only inspection, setup/planning/context-gathering, review, summary, build, test, or verify-only steps (these happen automatically).
- Do NOT include markdown formatting, commentary, or extra keys in the JSON output.

### Step 3 - Board the Cards

Call `CreateReadyKanbanCard(workspaceID, title, description, acceptanceCriteria)` for each card, passing the workspace ID from the backlog item. This creates a card in the Ready lane.

For cards with dependencies, call `MoveKanbanCard(workspaceID, cardID, "Blocked")` initially if dependencies are not yet met, or leave in Ready if ready to go.

Set the direction field via `UpdateKanbanCardDirection(workspaceID, cardID, "<direction>")` to indicate implementation approach (e.g., "edit existing", "add new", "refactor").

### Step 4 - Record Progress

As agents execute cards, monitor kanban events. When a card moves through InProgress to Done, note the completion in chat. Once ALL cards for a backlog item are done, mark the backlog item as done per the "Marking Items Done" procedure above.

## Card Acceptance Criteria Guidelines

Good acceptance criteria are:
- **Observable**: Describe verifiable state, not actions ("button renders at 44x44px minimum" not "test the button").
- **Specific**: Reference exact selectors, breakpoints, thresholds, or behaviors.
- **Self-contained**: Stand alone - someone reading only the criterion should understand what success looks like.

Bad examples:
- "Fix the issue" (vague, no observable outcome)
- "Make sure it works on mobile" (unmeasurable)
- "Check the styling" (process step, not outcome)

## Full Sprint Flow Summary

1. Read `.echo/backlog.md` to identify eligible items (status=backlog, deps satisfied)
2. Present selections to user for approval
3. Decompose each item into Kanban cards (id/title/description/criteria/dependencies)
4. Call `CreateReadyKanbanCard()` for each card
5. Set directions via `UpdateKanbanCardDirection()`
6. Move dependent cards to Blocked until prerequisites finish
7. Monitor kanban events as agents execute
8. When all cards for an item complete, mark backlog item done via `filesystem_edit_text`
9. Report sprint summary to user

## Hazard Notes

- Never persist chat history or Kanban cards unless product behavior explicitly changes.
- Kanban state persists to AppData Echo state.json; interrupted runs restore blocked/in-progress cards.
- Regenerate Wails bindings (`wails generate`) if backend service method signatures change before deploying frontend changes.
- Keep workspace paths normalized and do not bypass workspace path guards.
