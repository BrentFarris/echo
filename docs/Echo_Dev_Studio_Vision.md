# Echo Dev Studio — Autonomous Project Orchestrator

> **Status**: Implementation Planning — Phase 1 scoped with Backlog & Design Docs
> **Date**: 2026-06-29
> **Goal**: Define the architecture for an AI-orchestrated development studio desktop app with web UI access, built on top of Echo. Engine-agnostic via pluggable Stack Profiles loaded from user-editable JSON and Markdown files.

---

## 1. Core Concept — You're the CEO / Director

You don't write boilerplate. You don't chase build errors. You **direct**. Every other role in the studio is an AI agent with specialized prompts, tools, and authority boundaries defined by a **Stack Profile**:

| Role | What it does |
|------|-------------|
| **CEO / You** | Sets vision, approves milestones, reviews deliverables, makes creative/architectural calls |
| **Executive Producer (AI)** | Breaks high-level goals into epics → stories → tasks; manages backlog, sprints, and kanban board; handles dependencies, prioritization, and scheduling autonomously |
| **Lead Engineer (AI)** | Owns architecture decisions, code reviews, build pipeline, framework integration |
| **Engineers (AI)** | Implement features against specs from Lead; write tests; fix bugs; self-heal compiler/linter errors |
| **QA Lead (AI)** | Designs test plans, writes automated test harnesses, runs regression suites |
| **QA Testers (AI)** | Execute tests via profile-specific runners (HTSP for games, Playwright/Cypress/AutomationTool for others); file bug reports with repro steps |
| **Domain Specialists (AI)** | Art Directors, Level Designers, Sound Engineers, UX Designers — dynamically loaded based on the active Stack Profile |
| **DevOps (AI)** | CI/CD, build matrix, packaging, deployment targets |

---

## 2. What You Already Have

### Echo — The AI Assistant Foundation
- **Wails v2 desktop app** (Go backend + TypeScript/Vite frontend) with web access server
- **Chat orchestration** — OpenAI-compatible chat completions, streaming SSE, tool execution loop
- **Kanban board** — Cards with lanes (ready/inProgress/blocked/done), dependencies, progress tracking
- **Agent execution** — Concurrent card agents that execute tasks with registered tools
- **Tool registry** — Filesystem CRUD, shell commands, LSP queries, web search, workspace context building
- **State persistence** — Chat sessions, kanban cards, settings persisted to config dir

### Existing Workspaces (Examples)
- **gs_core**: C-based game engine framework with headless test harness and HTSP protocol
- **Unreal / React / .NET Projects**: Any codebase that can be pointed to as a workspace folder

---

## 3. Design Decisions (Resolved)

These constraints shape every implementation detail below:

| Decision | Resolution | Rationale |
|----------|-----------|-----------|
| **Profile loading strategy** | Embedded defaults + user `profiles/` directory | Users extend the system by dropping JSON + `.md` files — no recompilation needed |
| **Role prompts** | Referenced as `.md` files, not inline JSON strings | Prompts are long; markdown is easier to edit and version than embedded JSON |
| **Web UI framework** | Keep frameworkless TypeScript; mobile-first responsive CSS | Existing codebase is plain TS. Mobile support via CSS grid/flexbox with breakpoints, no drag-and-drop — button-based actions work on touch and mouse |
| **Desktop vs web server** | Keep Wails for desktop embedding; same UI served to web access server | `web_access.go` already exists. Responsive CSS handles phone and desktop from one code path |
| **Agent memory / context** | Workspace `AGENTS.md` + profile-level prompt files + card progress transcript | Rolling context via transcript entries avoids separate memory files per role |
| **Escalation mechanics** | Both: chat notification badge + blocked lane entry with explanation | CEO sees the badge in chat, clicks through to the blocked card for full context |
| **Profile extensibility** | Users write custom profiles as JSON manifests + markdown prompt files | No Go code changes needed for new stacks — just data files |
| **Backlog staging** | All work items land in Backlog first; EP commits to Sprints, then boards cards | Prevents unmanaged scope creep; CEO controls sprint commitment |
| **Design doc storage** | Workspace-embedded `.echo/design_docs/` directory with markdown files | Version-controlled with the project; agents read/write via filesystem tools; studio indexes and surfaces in UI |
| **Agent design context** | Relevant design docs injected into agent system prompt when starting a card | Engineers understand intended architecture before touching code; QA reads specs for test plans |

---

## 4. Architecture

### High-Level System Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                  Echo Dev Studio                                │
│              Desktop App + Web UI (phone/laptop access)          │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────────┐   │
│  │  STUDIO      │  │  CEO CHAT    │  │   BACKLOG & SPRINTS │   │
│  │  DASHBOARD   │  │              │  │   (Unprioritized →   │   │
│  │              │  │  (Your       │  │    Committed → Board)│   │
│  │              │  │   console)   │  │                     │   │
│  └──────────────┘  └──────────────┘  └─────────────────────┘   │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │              AI AGENT ORCHESTRATOR                      │    │
│  │  (Profile-driven prompts + scoped tool access per role) │    │
│  └─────────────────────────────────────────────────────────┘    │
│         │          │           │            │                   │
│    ┌────▼───┐  ┌───▼────┐  ┌─▼──────┐  ┌▼──────────┐          │
│    │ENG     │  │SPEC     │  │QA      │  │DEVOPS     │          │
│    │AGENTS  │  │AGENTS   │  │AGENTS  │  │AGENT      │          │
│    └────────┘  └─────────┘  └────────┘  └───────────┘          │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │           PROJECT WORKSPACES                            │    │
│  │  (Any codebase: gs_core, Unreal, React, .NET, Rust)     │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │           EXECUTION LAYER (Profile-Driven)              │    │
│  │  (Build systems, test runners, headless instances)      │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Process Architecture

```
Desktop Process (Go server with embedded web UI)
├── System Service (project management, agent orchestration, kanban)
│   ├── Inherits from Echo's SystemService architecture
│   ├── Enhanced kanban: epics → stories → tasks hierarchy
│   ├── Backlog system: unprioritized items → sprint commitment → boarded cards
│   └── Multi-project support with role-based assignment
├── Profile Loader (NEW)
│   ├── Loads stack-specific manifest (build commands, roles, prompts)
│   ├── Wires up role registries and tool scopes dynamically
│   └── Validates workspace compatibility with active profile
├── LLM Client (OpenAI-compatible chat completions)
│   ├── Inherits from Echo's internal/llm
│   └── Role-specific prompt templates per agent type
├── Build & Test Pipeline Service (NEW)
│   ├── Triggers profile-defined build commands
│   ├── Manages build artifacts and tracks history/status
│   └── Handles build variants: debug, release, test-targets
├── Agent Execution Engine (enhanced from Echo's kanban_scheduler)
│   ├── Role registry: defines roles with system prompts + allowed tools
│   ├── Tool scoping per role (engineers get full FS+shell, QA gets runners)
│   ├── Concurrency controls: max parallel agents, per-role limits
│   ├── Self-healing loops: retry failures autonomously up to N times
│   └── Escalation path: blocked agents ping CEO for decisions
├── Design Doc Indexer (NEW)
│   ├── Scans workspace .echo/design_docs/ directory
│   ├── Resolves relevant docs per kanban card (by epic/story linkage)
│   └── Injects doc content into agent system prompts at execution time
└── Web Server (enhanced from Echo's web_access)
    ├── Studio Dashboard (project overview, team status, build health)
    ├── CEO Chat Console (your main interaction point)
    ├── Backlog & Sprint View (prioritized work items, sprint planning)
    ├── Kanban Board View (multi-project, role-filtered)
    ├── Design Docs Panel (file tree, markdown preview, cross-links)
    ├── Test Runner View (live session monitoring per profile)
    ├── Build Pipeline View (CI/CD status, artifact browser)
    └── WebSocket/SSE event streaming for real-time updates
```

### Access Patterns
- **Desktop**: Full studio experience on your dev machine
- **Phone**: Monitor progress, approve decisions, review test results on-the-go
- **Other computers**: Full web access via LAN or tunnel

---

## 5. The Profile System (Engine-Agnostic Core)

The studio doesn't hardcode stack assumptions. A **Profile** is a configuration manifest that tells the orchestrator how to build, test, and staff a project. Everything else (EP loop, self-healing, escalation, web UI) stays identical.

Profiles are JSON manifests that reference markdown files for role system prompts. Users create custom profiles by dropping a `.json` file into their `profiles/` directory alongside any referenced `.md` files.

```json
{
  "id": "unreal-engine-5",
  "displayName": "Unreal Engine 5 Project",
  "buildSystem": {
    "command": "RunUAT BuildGraph -target=Development_Editor -script=BuildProgram.xml",
    "successPatterns": ["BUILD SUCCESSFULLY COMPLETED"],
    "failPatterns": ["ERROR:", "FAILED:"],
    "artifactPath": "{ProjectDir}/Binaries/Win64/{ProjectName}.exe"
  },
  "testSystem": {
    "type": "unreal-automation",
    "command": "{EngineDir}/AutomationTool RunTests -Project={ProjectName} -TestTags=Smoke",
    "resultParser": "unreal_log_parser"
  },
  "roles": [
    {
      "name": "Unreal Engineer",
      "systemPromptFile": "roles/unreal_engineer.md",
      "allowedTools": ["filesystem_*", "shell_command", "lsp_query"],
      "authorityLevel": "high",
      "maxRetries": 3
    },
    {
      "name": "QA Tester",
      "systemPromptFile": "roles/qa_tester.md",
      "allowedTools": ["shell_command", "filesystem_read_text", "filesystem_read_video"],
      "authorityLevel": "medium",
      "maxRetries": 2
    }
  ]
}
```

**Role prompt files are separate `.md` documents.** Example: `profiles/roles/unreal_engineer.md`:

```markdown
You are a C++/Blueprint engineer working in Unreal Engine 5. Follow UE coding standards,
use UHT annotations correctly, and prefer engine-provided patterns over custom solutions...
```

**Profile loading sources (highest priority first):**

1. **User `profiles/` directory** — user-created JSON manifests + markdown prompt files. Shadows embedded defaults by profile ID.
2. **Embedded defaults** — shipped with Echo via `//go:embed`. Fallback when no user profile matches.
3. **Workspace-level overrides** — `.md` files placed in a workspace folder that override system prompts without editing the base profile JSON.

### Built-in Profiles (Examples)
| Profile | Build Command | Test Runner | Specialized Roles |
|---------|--------------|-------------|------------------|
| `gs-core-fps` | `build.bat {ProjectName}` | HTSP headless driver | Art Director, Level Designer, Sound Engineer |
| `unreal-engine-5` | `RunUAT / UBT` | AutomationTool | Blueprint Engineer, VFX Artist, Animation Tech |
| `react-vite-web` | `npm run build` | Playwright / Cypress | UX Designer, Frontend Architect, QA Automation |

**The orchestrator loop is identical across all profiles.** It reads the manifest, loads roles, wires up build/test commands, and runs the exact same EP decomposition, self-healing retry logic, and escalation gates.

---

## 6. Key Features

### A. Studio Dashboard
- **Project overview**: Current project(s), sprint progress, burn-down chart
- **Team status**: Which agents are active, what they're working on, queue depth
- **Build health**: Last build status, test pass rate, known blockers
- **Budget/Token tracker**: LLM spend per project (cost awareness)

### B. Backlog System *(new)*

A **Backlog** sits between the CEO's raw intent and the Kanban board. It's where tasks accumulate before they're committed to a sprint. Think of it as a staging area with three zones:

```
CEO Chat / Agent Output  →  Backlog (unprioritized)  →  Sprint N (committed)  →  Kanban Lanes
```

#### Backlog Item Model

```go
type BacklogItem struct {
    ID            string    `json:"id"`
    WorkspaceID   string    `json:"workspaceId"`
    Title         string    `json:"title"`
    Description   string    `json:"description"`
    Level         string    `json:"level"`       // "epic", "story", or "task"
    Source        string    `json:"source"`      // "ceo", "ep", "qa_bug", "agent"
    Priority      int       `json:"priority"`    // 0 = unranked, higher = more important
    Role          string    `json:"role"`        // suggested role assignment
    SprintID      string    `json:"sprintId"`    // empty = in backlog, set = committed
    Dependencies  []string  `json:"dependencies"`// other backlog/card IDs
    CreatedAt     time.Time `json:"createdAt"`
    Status        string    `json:"status"`      // "backlog", "sprint_committed", "boarded"
}
```

#### How items enter the backlog

| Source | Mechanism |
|--------|-----------|
| **CEO types in chat** | EP parses vision messages and creates backlog items directly |
| **Agents request work** | An engineer agent discovers needed refactoring → files a backlog item with source `"agent"` |
| **QA finds bugs** | QA tester auto-files bug cards as backlog items with source `"qa_bug"` |
| **EP decomposition** | EP breaks an epic into stories/tasks → they land in backlog first, not directly on the board |

#### Prioritization flow

1. **Initial ranking**: EP assigns a default priority based on context (bugs = high, new features = medium, nice-to-haves = low). CEO can override by typing `@EP prioritize <item> high` or similar natural language.
2. **Dependency resolution**: EP detects that item B depends on item A and won't allow B to enter a sprint before A is done.
3. **Sprint planning**: When the CEO says "start sprint" or sets a milestone, EP pulls the highest-priority uncommitted backlog items that fit within estimated capacity. Items move from `status: "backlog"` → `"sprint_committed"` → then to Kanban lanes as `"boarded"`.

#### Sprint Model

```go
type Sprint struct {
    ID             string    `json:"id"`
    WorkspaceID    string    `json:"workspaceId"`
    Name           string    `json:"name"`          // e.g. "Sprint 3", "Vertical Slice"
    Milestone      string    `json:"milestone"`     // ties to a milestone like "Alpha"
    BacklogItemIDs []string  `json:"backlogItemIds"`
    Status         string    `json:"status"`        // "planned", "active", "completed", "abandoned"
    StartedAt      time.Time `json:"startedAt"`
    CompletedAt    time.Time `json:"completedAt"`
}
```

#### Frontend view (mobile-first)

- **Desktop**: Three-column layout — Backlog | Active Sprint | Kanban Board. Items in backlog show priority badge, source icon, and role tag. Button-based "Add to Sprint" / "Promote Priority".
- **Mobile (<768px)**: Tab bar at top: `Backlog` · `Sprint` · `Board`. Backlog tab shows a vertical list grouped by priority with tap-to-expand details. A floating "Plan Sprint" button triggers EP to auto-select top items.

#### Key interactions

| Action | Who | How |
|--------|-----|-----|
| Add to backlog | CEO / EP / Agent | Chat command or agent API call |
| Set priority | CEO / EP | Natural language in chat or button click |
| Commit to sprint | CEO / EP | "Start sprint" triggers EP to pull top items |
| Move backlog → board | EP (automatic) | When sprint starts, committed items become Kanban cards in Ready lane |
| Re-prioritize mid-sprint | CEO only | EP can suggest; CEO approves scope changes |

### C. Project Management (extends Echo's kanban)
- **Multi-board support**: One board per project, plus a master backlog
- **Epic → Story → Task hierarchy** (Echo has flat cards; add parent/child relationships)
- **Sprint planning mode**: EP pulls from backlog into sprints, auto-decomposes to tasks
- **Milestone tracking**: "Vertical Slice," "Alpha," "Beta," "Release Candidate"
- **Role assignment**: Each card tagged with the responsible role
- **Backlog linkage**: Cards track their source `BacklogItemID` for traceability

### D. AI Agent System (extends Echo's kanban scheduler)
- **Role registry**: Define roles with system prompts, allowed tools, and authority level
- **Self-healing execution loops**: Agents catch build/test errors, diagnose via LLM, fix code, and retry autonomously up to `N` times before escalating
- **Agent concurrency controls**: Max parallel agents, per-role limits
- **Escalation path**: Blocked agents ping the CEO (you) for decisions only when retries are exhausted or authority boundaries are hit

### E. Build & Test Pipeline
- **Integrated build system**: Triggers profile-defined commands from within the studio
- **Build variants**: Debug, release, test-targets — each with different configs
- **Artifact management**: Store built executables/packages, track versions, manage history
- **Hot reload support**: For rapid iteration on gameplay or UI changes

### F. AI QA Testing (The Killer Feature)
The studio drives profile-specific test runners autonomously:

```
1. QA Lead Agent receives a feature spec from Kanban
   "Player movement system — verify WASD controls work correctly"

2. QA Lead generates test plan:
   - Test case: "Player moves forward on W input"
   - Test case: "Player strafes left on A input"
   - Expected outcomes defined for each

3. QA Tester Agent picks up test cases and launches runner:
   [GS_CORE] test_fps.exe --htsp-mode --test-session <id>
   [UNREAL] AutomationTool RunTests -TestTags=Movement
   [WEB]    npx playwright test movement.spec.ts

4. Execution Loop begins (Profile-Driven):
   ┌─────────────────────────────────────────────┐
   │ Runner executes at real-time speed          │
   │ QA Agent receives state snapshots/logs      │
   │                                             │
   │ QA Agent (LLM) observes outcomes:           │
   │ - Current player position / DOM state       │
   │ - Screenshot or video frame                 │
   │ - Rolling context of recent actions         │
   │                                             │
   │ QA Agent determines PASS/FAIL based on      │
   │ understanding — not just hardcoded asserts. │
   └─────────────────────────────────────────────┘

5. Test completes → QA Agent writes:
   - summary.json with PASS/FAIL assertions
   - Observations about behavior
   - Screenshots/video of the session
   - Bug report if failures detected (auto-filed to Backlog)
```

**The magic**: The AI isn't just running pre-written tests. It's **executing intelligently**, observing outcomes, and determining pass/fail based on context — auto-filing bugs with repro steps when things break.

### G. CEO Console (Chat Interface)
Your main interaction point:

> *"I want a top-down shooter with wave-based enemy spawns. Think Hotline Miami meets Enter the Matrix. Neon aesthetic. Let's start with a vertical slice."*

The Executive Producer agent then:
1. Creates backlog items from the vision (epics → stories → tasks)
2. Assigns priorities and suggested roles to each item
3. Proposes a sprint plan for your approval
4. On approval, boards committed items into Kanban lanes with role assignments
5. Keeps you informed of progress and asks for creative decisions when needed

### H. Design Doc Spaces *(new)*

Agents need a **shared knowledge area** where they can read and write structured design documents — architecture decisions, API specs, component designs, etc. Unlike chat (ephemeral) or code files (too granular), design docs are the *intent layer* between vision and implementation.

#### Storage location

Workspace-embedded `.echo/design_docs/` directory inside each workspace folder. Agents use filesystem tools to read/write markdown files here. The studio indexes them and surfaces them in the UI. Pros: version-controlled with the project, visible in IDE.

#### Structure

```
{workspace}/.echo/design_docs/
├── index.md                    # Table of contents, auto-generated by EP
├── architecture/
│   ├── overview.md             # High-level system design
│   └── decisions/              # ADRs (Architecture Decision Records)
│       └── 001-auth-strategy.md
├── specs/
│   ├── api/                    # API specifications
│   │   └── auth_endpoints.md
│   └── components/             # Component-level designs
│       └── player_controller.md
└── epics/
    └── epic-001-player-movement.md  # Tied to a kanban epic
```

#### How agents use design docs

| Agent Role | Read | Write |
|------------|------|-------|
| **EP** | Reads all docs for context when decomposing tasks | Creates epic-level docs, updates `index.md` |
| **Lead Engineer** | Reads architecture and spec docs before planning implementation | Writes ADRs, creates component specs |
| **Engineers** | Reads specs relevant to their assigned task | Can propose changes (PR-style) that Lead Engineer reviews |
| **QA Lead** | Reads specs to generate test plans from acceptance criteria | Writes test plan docs |

#### Design doc lifecycle

```
1. CEO or EP creates an epic → EP scaffolds a design doc in .echo/design_docs/epics/
2. Lead Engineer expands it with technical details, ADRs, and spec references
3. Engineers read the doc as part of their task context (injected into system prompt)
4. QA reads acceptance criteria from the doc to generate test plans
5. When implementation is complete, EP marks the doc status as "Implemented"
6. If scope changes, Lead Engineer updates the doc and flags affected kanban cards
```

#### Frontend integration

- **Dedicated panel** in the studio UI: `Design Docs` tab alongside Backlog/Sprint/Board.
- Shows a file tree of `.echo/design_docs/` with status badges (`Draft`, `Reviewed`, `Implemented`).
- Markdown preview with syntax highlighting; edit mode opens a simple markdown editor.
- **Cross-linking**: When viewing a kanban card, show a "Related Design Doc" link if one exists. When viewing a design doc, show linked epics/stories/tasks.

#### Context injection for agents

When an agent starts working on a card, the system prompt includes:
```
Relevant design documents:
- .echo/design_docs/epics/epic-001-player-movement.md (Implemented)
- .echo/design_docs/specs/components/player_controller.md (Reviewed)
```

The agent reads these files as part of its initial context, so it understands the intended design before touching code.

### I. Live Session Viewer
Watch agents execute tests or run builds in real-time:
- Live log feed / video stream from test runner
- Action log showing what the AI decided and why
- State overlay (entity positions, DOM snapshots, health values)
- Ability to pause, step-through, or inject manual inputs

---

## 7. What This Means for Echo

### Capability Comparison

| Capability | Echo (today) | Echo Dev Studio |
|-----------|-------------|------------------|
| Chat with LLM | ✅ | ✅ Enhanced with role context |
| Kanban board | ✅ Flat cards | Epics → stories → tasks, multi-board |
| Agent execution | ✅ Generic agents | Role-specialized agents with tool scoping |
| Filesystem tools | ✅ | ✅ + asset pipeline / build tools |
| Shell commands | ✅ | ✅ + build system integration |
| Self-healing loops | ❌ | ✅ Retry/repair on build/test failures |
| Escalation gates | ❌ | ✅ Authority boundaries per role |
| Profile system | ❌ | New — pluggable stack manifests |
| Build pipeline | ❌ | New — profile-driven triggers |
| QA execution | ❌ | New — autonomous test runners |
| Screenshot diffing | ❌ | New — visual regression testing |
| Sprint/milestone tracking | ❌ | New |
| Backlog system | ❌ | New — prioritized staging area with sprint commitment |
| Design doc spaces | ❌ | New — workspace-embedded markdown docs with agent context injection |
| Studio dashboard | ❌ | New |

### Strategic Options

1. **Extend Echo → Echo Dev Studio** (RECOMMENDED)
   - Keep the solid architecture: Wails, service layer, kanban system, agent orchestration
   - Specialize it for autonomous dev: add Profile Loader, Build Pipeline, self-healing scheduler
   - Enhance what exists: kanban hierarchy, web UI complexity

2. **Build alongside** → Studio uses Echo as a library/service for chat/kanban, adds stack layers on top
   - Pros: Clean separation, can use Echo independently
   - Cons: Duplication, integration complexity

3. **Replace entirely** → New app from scratch with studio DNA
   - Pros: No legacy baggage
   - Cons: Rebuild everything, lose proven patterns

---

## 8. Phased Approach & Tasks

### Implementation Order

Tasks are ordered by dependency — earlier tasks unblock later ones. All backend code lives in `internal/services/`. Frontend changes extend the existing frameworkless TypeScript modules under `frontend/src/app/`.

---

### Phase 1 — Foundation (Roles, Hierarchy, Backlog, Design Docs)

**Goal**: Transform Echo's flat kanban and generic agents into a role-aware, hierarchical task system with self-healing execution loops, backlog staging, sprint commitment, and design doc context injection. This is the core autonomy layer — everything else builds on it.

#### Task 1.1 — Epic → Story → Task Hierarchy *(start here — structural change to cards)*

**Modified files:** `kanban.go`, `kanban_scheduler.go`, `state_persistence.go`, `frontend/src/app/kanban/index.ts`, `frontend/src/styles.css`

- Add `ParentID string` and `Level string` (`epic`/`story`/`task`) fields to `KanbanCard`
  - `ParentID == ""` → epic (top-level)
  - Points to epic ID → story
  - Points to story ID → task
- Add `BacklogItemID string` field to `KanbanCard` — tracks source backlog item for traceability
- Update `boardForWorkspace()` to return nested structure:
  ```go
  type KanbanBoard struct {
      WorkspaceID string       `json:"workspaceId"`
      Epics       []KanbanEpic `json:"epics"` // contains Stories, which contain Tasks
  }
  ```
- Add dependency validation: stories can't move to Done until all child tasks are Done; epics can't move to Done until all child stories are Done
- **Frontend — mobile-first kanban rendering:**
  - Desktop (≥1024px): horizontal lanes with collapsible tree inside each lane
  - Tablet (768–1023px): narrower lanes, compact card layout
  - Mobile (<768px): single-column vertical list grouped by lane, tap to expand epic → story → task
  - No drag-and-drop; use `data-action` buttons for Move/Promote/Demote (touch-friendly)
- Add `ParentID`, `Level`, and `BacklogItemID` to persistence format in `state_persistence.go`

**Deliverable:** Multi-level kanban board. Epics track goals, stories break them down, tasks are the executable units. Responsive on phone and desktop. Cards trace back to source backlog items.

#### Task 1.2 — Backlog System & Sprint Management *(new — data model + service)*

**New file:** `backlog.go`
**Modified files:** `state_persistence.go`, `frontend/src/app/kanban/index.ts`, `frontend/src/styles.css`

- Define `BacklogItem` struct (see section 6B) with fields for title, description, level, source, priority, role, sprintID, dependencies, status
- CRUD operations: `AddBacklogItem`, `UpdateBacklogItem`, `DeleteBacklogItem`, `GetBacklogItems(workspaceID)`
- Priority sorting: items sorted by priority descending, then creation time ascending
- Status transitions: `"backlog"` → `"sprint_committed"` → `"boarded"` (auto-transition when EP creates kanban card)
- Define `Sprint` struct (see section 6B) with fields for name, milestone, backlog item IDs, status, timestamps
- Sprint CRUD: `CreateSprint`, `CommitToSprint(workspaceID, sprintID, backlogItemIDs)`, `StartSprint(sprintID)`, `CompleteSprint(spersist sprints in `state.json`

**Deliverable:** Backlog items accumulate from CEO chat, agent requests, and QA bugs. EP commits them to sprints; when a sprint starts, committed items become Kanban cards. Full traceability from backlog → sprint → board.

#### Task 1.3 — Role Registry & Tool Scoping *(data-driven)*

**New file:** `roles.go`
**Modified files:** `kanban_scheduler.go`, `state_persistence.go`

- Define `Role` struct loaded from JSON:
  ```json
  {
    "name": "Engineer",
    "systemPromptFile": "roles/engineer.md",
    "allowedTools": ["filesystem_*", "shell_command", "lsp_query"],
    "authorityLevel": "high",
    "maxRetries": 3
  }
  ```
- `RoleRegistry` service reads role definitions from:
  - Embedded defaults (shipped with Echo via `//go:embed`)
  - User `profiles/` directory (user-editable JSON + `.md` prompt files)
  - Workspace-level overrides in workspace folders
- Provides `LLMSchema(roleName)` that returns tool schema filtered to the role's whitelist
- Add `RoleID string` field to `KanbanCard` — scheduler uses it when starting the agent
- Refactor `runKanbanAgent()` to accept a `Role`; use role's system prompt (loaded from `.md` file) and scoped tool schema instead of global defaults
- Persist role assignments with cards in `state.json`

**Deliverable:** Agents execute with different prompts and tool access based on assigned role. Data is user-editable JSON + markdown files.

#### Task 1.4 — Design Doc Indexer & Agent Context Injection *(new)*

**New file:** `design_docs.go`
**Modified files:** `kanban_scheduler.go`, `frontend/src/app/kanban/index.ts`

- `DesignDocIndexer` service scans `{workspace}/.echo/design_docs/` on workspace load and on filesystem events
- Builds an index mapping: epic ID → relevant doc paths, story ID → relevant doc paths
- `ResolveDocsForCard(cardID)` returns the list of design doc file paths relevant to a card
- EP scaffolds initial design docs when creating epics: `.echo/design_docs/epics/epic-{id}.md` with title and description from backlog item
- **Agent context injection**: When `runKanbanAgent()` starts, it calls `ResolveDocsForCard()`, reads the relevant markdown files, and appends their content to the agent's system prompt under a "Relevant Design Documents" section (capped at reasonable token budget)
- **Frontend**: `Design Docs` panel showing file tree of `.echo/design_docs/` with status badges (`Draft`, `Reviewed`, `Implemented`). Markdown preview. Cross-link: viewing a card shows "Related Design Doc" link if one exists.

**Deliverable:** Agents receive design context before touching code. EP creates epic docs automatically. Lead Engineers expand them with specs and ADRs. QA reads acceptance criteria from docs for test plans.

#### Task 1.5 — Self-Healing Execution Loops

**Modified files:** `kanban_scheduler.go`

- Generalize the existing verification-repair pattern to **all tool call failures**: when a tool returns `!Success`, feed error output back to LLM with repair prompt instead of blocking the card immediately
- Add retry counter per agent run; compare against role's `maxRetries` before escalating to Blocked
- Log each retry attempt in `ProgressTranscript` with type `"repair_attempt"` showing the error and the agent's proposed fix
- After max retries exhausted, move card to Blocked with escalation entry explaining what was tried
- Key change: currently tool failures → immediate block. New flow: tool failure → LLM repair attempt → retry up to N times → block only if exhausted

**Deliverable:** Agents autonomously fix build errors, lint violations, and test failures up to N times before escalating.

#### Task 1.6 — CEO Escalation Mechanism

**Modified files:** `kanban_scheduler.go`, `chat.go`, `frontend/src/app/kanban/index.ts`, `frontend/src/styles.css`

- When a card is blocked by escalation (not user-cancelled), post a notification message to the CEO chat session with card ID, role, error summary, and suggested options
- Frontend: show red notification badge on Kanban view when escalations are pending
- Add `ResolveEscalation(cardID, decision)` method: CEO types response in chat, system appends it as direction to the blocked card and moves it back to Ready
- Mobile-friendly: escalation cards shown at top of Blocked lane with a prominent "Resolve" button

**Deliverable:** Blocked agents ping you in chat with context; you respond with a decision and the agent resumes.

#### Task 1.7 — Executive Producer (EP) Orchestration Loop *(updated to use backlog)*

**New file:** `ep_orchestrator.go`
**Modified files:** `chat.go`, `decomposition.go`, `backlog.go`

- New goroutine that runs independently of card execution scheduler
- EP watches for CEO messages tagged as "vision" or "milestone"; parses them via LLM into structured backlog items (epics → stories → tasks) with suggested priorities and role assignments
- **EP creates backlog items, not kanban cards directly**: items land in the Backlog with status `"backlog"`
- EP proposes sprint plans: groups highest-priority uncommitted items respecting dependencies; CEO approves via chat
- On sprint start: EP commits approved items (`status → "sprint_committed"`) then creates kanban cards from them (`status → "boarded"`, placed in Ready lane)
- EP monitors board state: detects stuck patterns (cards blocked >24h), reassigns tasks between roles if a role is overloaded
- Add `StartEP(workspaceID)` and `StopEP(workspaceID)` methods; EP runs as long as the workspace is active
- Reuse existing `decomposition.go` pattern but extend output to include hierarchy level, role assignment, priority, and backlog item metadata

**Deliverable:** CEO types vision → EP decomposes into backlog items with priorities → EP proposes sprint plan → CEO approves → cards board autonomously with role assignments. No manual card creation needed.

**Phase 1 Exit Criteria**: You type a feature request in CEO Chat. EP breaks it down into prioritized backlog items, proposes a sprint, and on your approval boards the work with role assignments. Agents execute autonomously with self-healing retries and design doc context. You only interact when an escalation badge appears.

---

### Phase 2 — Profile System & Build Pipeline *(user-extendable data)*

**Goal:** Make the stack pluggable via JSON manifests and markdown prompt files users can create themselves.

#### Task 2.1 — Profile Manifest Schema & Loader

**New file:** `profiles.go`
**Data directory:** User's config dir gets a `profiles/` folder for custom profiles

- Define `Profile` struct loaded from JSON:
  ```json
  {
    "id": "my-custom-stack",
    "displayName": "My Custom Stack",
    "buildSystem": { ... },
    "testSystem": { ... },
    "roles": [
      { "name": "...", "systemPromptFile": "roles/...", "allowedTools": [...], ... }
    ]
  }
  ```
- `ProfileLoader` service:
  - Reads JSON manifests from embedded assets (shipped defaults) + user `profiles/` directory
  - Validates schema at load time
  - Caches loaded profiles in memory
  - Supports `.md` files referenced by roles for system prompts (e.g., `systemPromptFile: "roles/engineer.md"`)
- Add `SetActiveProfile(workspaceID, profileID)` method; stores mapping in persisted state
- Profile discovery: user drops a `.json` file into `profiles/`, Echo picks it up on next load

**Deliverable:** Profiles load from JSON files. Users create custom profiles by dropping JSON + markdown files into a directory.

#### Task 2.2 — Built-in Profiles (shipped as embedded data)

**New directory:** `internal/services/embedded_profiles/`

- Write three starter profiles as JSON + markdown role prompt files:
  - `generic-code.json` — roles (Engineer, QA Tester), generic build/test commands
  - `go-project.json` — roles (Go Engineer, QA Tester), `go build`, `go test`
  - `web-vite.json` — roles (Frontend Engineer, QA Automation), `npm run build`, Playwright config
- Embed all in binary as fallback defaults via `//go:embed`
- Allow user overrides in local `profiles/` directory that shadow embedded profiles by ID

**Deliverable:** Working out-of-the-box profiles. Users extend by writing their own JSON + `.md` files.

#### Task 2.3 — Build Pipeline Service

**New file:** `build_pipeline.go`
**Modified files:** `kanban_scheduler.go`, `frontend/src/app/kanban/index.ts`

- Manages build lifecycle: trigger → execute → parse output → record result
- Parses build output against profile's success/fail patterns; extracts error lines for agent repair prompts
- Tracks build history: timestamp, variant, status, duration, artifact path; persists last 50 builds per workspace
- Add `TriggerBuild(workspaceID, variant)` method callable by agents via tool or EP autonomously after code changes
- Frontend: "Build Pipeline" section in kanban view showing recent builds with status badges and expandable logs (collapsible for mobile)

**Deliverable:** Agents trigger builds; studio parses results, tracks history, feeds errors back to self-healing loops.

#### Task 2.4 — Profile-Driven Role Loading

**Modified files:** `profiles.go`, `roles.go`, `kanban_scheduler.go`

- When a workspace loads, read its active profile and register all defined roles into the `RoleRegistry`
- EP uses profile's role definitions when decomposing tasks (assigns cards to correct role names)
- Allow per-workspace role overrides: user places `.md` files in workspace folder that override system prompts without editing the base profile JSON

**Deliverable:** Switching a workspace profile automatically changes available roles, build commands, and test runners.

**Phase 2 Exit Criteria**: You can point the studio at any project folder, select a profile (or write your own), and the studio knows how to build it, test it, and staff it with the right agents.

---

### Phase 3 — QA Framework & Autonomous Testing

**Goal:** Close the loop — features are not just built, they're tested. QA agents generate plans, execute runners, and file bugs autonomously.

#### Task 3.1 — Test Plan Generation (QA Lead Agent)
- When a task card moves to Done, EP triggers QA Lead role to evaluate whether testing is needed
- QA Lead reads the card's description, acceptance criteria, and changed files; generates structured test plan via LLM
- Creates child "Test" cards under the original task; each assigned to QA Tester role

#### Task 3.2 — Profile-Specific Test Runner Integration

**New file:** `qa_runner.go`

- Abstract `Runner` interface: `Launch`, `Execute`, `GetSnapshot`, `Kill`
- Implement `GenericCmdRunner`: runs any shell command, parses stdout/stderr against profile-defined patterns (Phase 1 deliverable)
- HTSP and Playwright runners deferred to later iterations; generic runner covers most stacks

#### Task 3.3 — QA Tester Agent Execution Loop
- QA Tester agent picks up test cards; launches appropriate runner via profile config
- On failure: auto-files a Bug backlog item with source `"qa_bug"`, repro steps, logs, and assigned role
- On success: marks test card Done; if all tests for a feature pass, EP marks the feature as "Verified"

#### Task 3.4 — Screenshot & Visual Regression *(deferred)*
- Add `screenshot_capture` tool and LLM visual analysis capability when multi-modal models are available

**Phase 3 Exit Criteria**: You ship a feature request → EP decomposes it → Engineers build it → QA tests it → bugs get fixed autonomously → you approve the final result. Zero manual test execution or bug filing.

---

### Phase 4 — Studio Polish & Operations

#### Task 4.1 — Mobile-First Studio Dashboard

**Modified files:** `frontend/src/app/kanban/index.ts`, `frontend/src/styles.css`, `system.go`

- Single-pane overview visible on desktop and phone browser
- Sections: project progress, team status (active agents), build health, token budget tracker
- CSS grid layout that collapses to single column on mobile (<768px)
- Token budget: track LLM spend per workspace, configurable daily limits

#### Task 4.2 — Operational Safeguards
- Token budget enforcement: pause agent execution when limit reached; notify CEO
- Agent circuit breaker: if >3 agents blocked simultaneously, EP pauses all and requests CEO review
- Audit log: persistent log of agent actions, build results, test outcomes, escalations

**Phase 4 Exit Criteria**: Production-grade studio with dashboard, cost controls, and cascading failure protection. Ready for sustained autonomous operation.

---

### File Change Summary

| Category | New Files | Modified Files |
|---|---|---|
| **Backend — Roles & Profiles** | `roles.go`, `profiles.go`, `ep_orchestrator.go`, `build_pipeline.go`, `qa_runner.go` | `kanban_scheduler.go`, `kanban.go`, `state_persistence.go`, `chat.go`, `decomposition.go`, `system.go` |
| **Backend — Backlog & Sprints** | `backlog.go` | `ep_orchestrator.go`, `kanban.go`, `state_persistence.go`, `chat.go` |
| **Backend — Design Docs** | `design_docs.go` | `kanban_scheduler.go`, `ep_orchestrator.go` |
| **Data — Embedded profiles** | `embedded_profiles/*.json`, `embedded_profiles/roles/*.md` | — |
| **Frontend — Kanban, Backlog, Dashboard** | (extends existing) | `app/kanban/index.ts`, `styles.css`, `app/state.ts`, `app/events.ts` |
| **Tests** | `roles_test.go`, `profiles_test.go`, `ep_orchestrator_test.go`, `build_pipeline_test.go`, `kanban_hierarchy_test.go`, `backlog_test.go`, `design_docs_test.go` | `kanban_scheduler_test.go`, `kanban_test.go` |

---

### Phase Summary

| Phase | Core Capability | Lines (est.) | Key Files |
|-------|---------------|-------------|-----------|
| **1** | Roles, hierarchy, backlog, sprints, design docs, self-healing, EP loop | ~1,800 | `roles.go`, `backlog.go`, `design_docs.go`, `ep_orchestrator.go`, scheduler refactor, kanban model + frontend |
| **2** | Pluggable profiles, build pipeline, multi-stack support | ~800 | `profiles.go`, `build_pipeline.go`, embedded profile manifests + role `.md` files, role wiring |
| **3** | QA test generation, runner integration, bug auto-filing | ~1,000 | `qa_runner.go`, generic runner implementation, QA agent loop |
| **4** | Dashboard, cost controls, operational safeguards | ~600 | Dashboard frontend, budget enforcement, circuit breaker |
| **Total** | Full autonomous studio | **~4,200** | New files + extensions to existing Echo services |

---

## 9. Open Questions & Decisions Needed

### Architecture Decisions (Remaining)
1. **Test runner communication** — How do QA agents receive state from different runners? Structured JSON streams (HTSP), log parsing (Unreal), or DOM snapshots (Web)?

2. **Multi-project concurrency** — Can multiple projects build/test simultaneously? Resource management strategy for parallel headless instances or heavy compilers?

3. **Build-test feedback loop** — When a build fails, does the studio auto-rollback and notify engineering agents? Or just flag it on Kanban?

4. **Your role in the loop** — How hands-on do you want to be? Full autonomous operation with periodic check-ins, or real-time approval gates on major decisions?

### Agent Design Decisions (Remaining)
5. **Human-in-the-loop gates** — Which decisions require your approval before an agent proceeds? Art direction approval? Architecture changes? Scope additions? Build promotion?

6. **Token budget management** — How do you track and limit LLM spend per project? Per agent? Per day?

7. **Backlog item size limits** — Should EP enforce a minimum granularity (e.g., stories must decompose to at least 2 tasks)? Or trust the model's judgment?

8. **Design doc review process** — When Lead Engineer writes an ADR or spec, does it need CEO approval before Engineers can act on it? Or is Lead Engineer trusted to publish directly?

---

## 10. Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| LLM cost spirals with concurrent agents | Financial | Token budget tracking, per-agent limits, cache common responses |
| Agent hallucination in code | Bugs that are hard to find | Code review gates, automated test validation, build failures catch most issues |
| Test runner latency still too slow for complex QA | Limited test coverage | Adaptive timeouts, batch multiple test cases per session, parallel runners |
| Context window limits for complex projects | Agents lose track of project context | Memory files per role, periodic summarization, focused task scoping |
| Concurrent builds/testers exhausting resources | System instability | Resource limits per session, graceful degradation, queue management |
| Backlog bloat | EP overwhelmed with uncommitted items | Auto-archive stale items; EP summarizes backlog for CEO review periodically |

---

## 11. Glossary

| Term | Definition |
|------|-----------|
| **Backlog** | A prioritized staging area where work items accumulate before being committed to a sprint and boarded as Kanban cards |
| **Backlog Item** | A unit of work (epic, story, or task) that lives in the Backlog until committed to a sprint |
| **Design Doc Space** | A workspace-embedded `.echo/design_docs/` directory containing markdown files for architecture decisions, specs, and epic documentation |
| **Echo Dev Studio** | The autonomous AI project orchestrator built on top of Echo's core architecture |
| **EP (Executive Producer)** | AI agent that manages project planning, backlog, sprints, kanban board, dependencies, and sprint scheduling autonomously |
| **Escalation Gate** | Authority boundary where an agent stops retrying and pings the CEO for a creative or architectural decision |
| **QA Runner** | Profile-specific test execution layer (HTSP for games, AutomationTool for Unreal, Playwright for Web) |
| **Self-Healing Loop** | Agent behavior that catches build/test errors, diagnoses via LLM, fixes code, and retries autonomously before escalating |
| **Sprint** | A time-boxed commitment of backlog items to be completed. EP pulls from backlog; CEO approves; committed items become Kanban cards when sprint starts |
| **Stack Profile** | A configuration manifest defining roles, build commands, test runners, and prompts for a specific tech stack |

---

*This document is a living brainstorm. Update sections as decisions are made and features are scoped.*
