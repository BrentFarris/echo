# Worker Subagents — Design

Status: Phase 1 implemented (worker kind, advisory partitioning, kind badge). Phase 2 implemented (path-claim registry, `internal/server/worker_path_claims.go`). Phase 3 deferred.

## Problem

Echo has read-only research subagents (`research_agents_spawn` family) that keep
their tool output in a private transcript and return only a bounded report to
the parent chat. That context-isolation model is exactly what long coding tasks
need too, but research agents are hard-constrained against mutation:

- `echo/internal/server/chat_research.go` — `researchAgentSystemPrompt`:
  "Do not edit files, run shell commands, mutate external systems…"
- `echo/internal/tools/registry.go` — `ResearchLLMSchemaForScopes` exposes only
  non-mutating tools.
- `echo/internal/tools/types.go` — `ExecutionContext.ResearchAgents` is nil for
  children, preventing recursive spawning.

There is no way to delegate *implementation* work (edit files, run tests) to a
subagent without polluting the main thread's context with every tool result.

## Goal

A **worker subagent**: a child agent with a private transcript that can edit
workspace files and run commands, reporting a bounded summary back to the
parent. The parent stays clean; the worker's noisy tool output never enters the
main thread.

## Non-goals (v1)

- No nested spawning (workers cannot spawn workers or researchers).
- No per-worker git branches/worktrees (see Phase 3).
- No cross-turn worker persistence; workers live and die with the turn, like
  research agents.

## Reuse vs. new code

The research run machinery in `chat_research.go` already provides everything a
worker needs: per-agent job queues, follow-ups, wait/cancel, concurrency
semaphore, private `messages` transcript, context compression
(`compressAgentContext`), trajectory events, and bounded report delivery
(`waitResultLocked` → `reportMaxBytes` / `aggregateReportMaxBytes`).

**Recommendation: generalize, don't duplicate.** Rename the concept from
"research run" to "subagent run" with a `kind` field (`research` | `worker`),
or keep the existing structs and add a parallel worker path that shares
helpers. Generalizing is less code but touches more call sites; a parallel
`chat_worker.go` is more isolated but copies ~500 lines of lifecycle logic.
The design below assumes generalization; the tool/API surface is the same
either way.

## Tool surface

Two options:

1. **Extend the existing tools** with a `kind` parameter on
   `research_agents_spawn` (default `research`). Smaller API, but mode
   permissioning becomes coarse: a mode that allows research agents would
   implicitly allow workers unless we special-case the parameter.
2. **Separate tools**: `worker_agents_spawn`, `worker_agent_send`,
   `worker_agents_wait`, `worker_agents_cancel`, registered in
   `echo/internal/tools/worker_agents.go`, backed by the same coordinator
   interface (a `SubagentCoordinator` with kind-aware spawn).

**Recommendation: option 2.** Agent modes allowlist by tool name
(`echo/internal/agentmodes/modes.go`), so separate names give explicit,
auditable permissioning: General mode gets workers; Plan mode does not. The
coordinator interface (`tools/types.go:221`) gains a kind parameter or a second
spawn method; `ExecutionContext` gets a `WorkerAgents` field attached only to
parent turns (same nil-for-children rule that blocks recursion today).

## Worker tool scope

Workers get `ChatLLMSchemaForScopes` (the full chat tool set) **minus**:

- `create_agent_mode` — no mode mutation from children.
- Plugin approval/execution tools — staging stays owner-gated.
- `research_agents_spawn` / `worker_agents_spawn` — recursion is already
  blocked by nil coordinator, but excluding the schemas avoids confusing the
  model.
- Remote-mutating source-control operations (`push`, `sync`) — local commits
  are fine; remotes stay a main-thread decision.

Path permissions: the worker's `ToolScopeChecker` should inherit the parent
mode's path globs (`sourceControlProfile.restrictToolScopes` already does this
for research; reuse it).

System prompt (`workerAgentSystemPrompt`): focused implementer, keep raw tool
output private, final response is a bounded handoff (files changed, commands
run, test results, deviations from the task, open questions). Explicitly
instruct: *only touch files within the assigned scope; report if the task
requires touching anything else.*

## Write-conflict strategy

Concurrent workers editing the same files is the core new risk. Phased:

**Phase 1 — advisory partitioning.** The orchestrator guidance text instructs
the parent to assign non-overlapping file sets per worker, and each worker's
task prompt includes its allowed paths. No enforcement. This matches how the
research guidance string works today (`researchOrchestratorGuidance`).

**Phase 2 — path-claim registry (implemented).** A per-turn
`workerPathClaims` map (path → agent ID) on the subagent run, guarded by the
run mutex. Before any worker's mutating filesystem tool call
(`filesystem_create_text`, `filesystem_edit_text`, `filesystem_delete_file`,
`filesystem_move`), the child dispatch loop in `runAgentTurn` checks the claim
via `claimWorkerPaths`; a conflicting claim returns a typed
`path_claimed_by_agent` error (including the owner's display name) so the
worker reports the conflict instead of clobbering. Claims are acquired on
first write, are idempotent for the owner, cover ancestor/descendant paths,
and are released when the worker reaches a terminal state (completion,
failure, cancel, or run close). Implemented in
`internal/server/worker_path_claims.go`. Shell commands bypass the registry
(they run in the sandbox and can't be cheaply intercepted) — documented as a
known gap; the sandbox already constrains blast radius.

**Phase 3 (optional, later) — per-worker git branches.** Each worker commits
to its own branch in a shared repo; the parent merges. High value for
parallel edits to the same files, but a large change to how the editor and
Git UI present state. Defer.

## Safety and control

- **User interrupt**: workers already run under the turn's
  `sandboxRequestContext` / `AdmitAIAction` gate
  (`chat_research.go` `runAgentTurn`); a user control pause interrupts worker
  tool calls the same way it interrupts main-thread calls. Keep this.
- **Sandbox**: `shell_command` from a worker targets the same sandbox as the
  main thread (`session.sandboxRequestContext`). No new sandbox surface.
- **Deadlines**: reuse `researchJobDeadline` semantics (2–15 min per job);
  workers doing builds/tests may need the upper bound more often — consider a
  `WorkerJobDeadlineSeconds` setting.
- **Cost**: workers consume real tokens. Add `WorkerAgentConcurrency`
  (default 2, lower than research's default since workers are heavier) and
  `MaxWorkersPerTurn` (default 4, cap 8) in `llm.Settings.Normalized()`.

## Context budget (the actual point)

- Worker tool results stay in the worker's private `messages` slice — never
  touch the parent transcript. Already true of the research loop; unchanged.
- Worker reports are delivered through `waitResultLocked` with
  `reportMaxBytes` / `aggregateReportMaxBytes` bounding, scaled to the
  *parent's* context window. Reuse as-is.
- Worker context compression reuses `compressAgentContext` (phase
  `"worker"` in trajectory events).

## Trajectory & UI

- New event types `worker/agent_created`, `worker/job_queued`, `worker/job_start`,
  `worker/tool_call`, `worker/tool_result`, `worker/job_end`,
  `worker/status`, `worker/report_delivered` — or reuse `research/*` with a
  `kind: "worker"` field. **Recommend reusing `research/*` + kind field** to
  minimize frontend churn; the trajectory viewer and
  `web/src/trajectory/` tests key on event type.
- `sessions.ResearchAgent` status card gains a `Kind` field; the UI shows
  "Worker" vs "Researcher" badges. Worker tool activity already streams via
  `updateResearchToolActivity` and will render in the existing tool-activity
  list.

## Files touched (generalization path)

| Area | File | Change |
| --- | --- | --- |
| Tool registration | `echo/internal/tools/worker_agents.go` (new) | spawn/send/wait/cancel tools |
| Coordinator iface | `echo/internal/tools/types.go` | `SubagentCoordinator`, `WorkerAgents` ctx field, `WorkerAgentSpec` |
| Run lifecycle | `echo/internal/server/chat_research.go` | kind field, worker system prompt, worker tool scopes, worker schema selection |
| Registry | `echo/internal/tools/registry.go` | `WorkerLLMSchemaForScopes` (chat schema minus denylist) |
| Modes | `echo/internal/agentmodes/modes.go` | grant worker tools to General/Goal; keep Plan read-only |
| Settings | `echo/internal/llm` | concurrency + limits |
| Session/UI state | `echo/internal/sessions`, `echo/web/src` | kind badge on agent cards |
| Tests | `echo/internal/server`, `echo/internal/tools`, `echo/web/src/trajectory` | lifecycle, permission, trajectory tests |

## Test plan

1. **Permission tests** (`agentmodes`): Plan mode cannot spawn workers;
   General/Goal can; a custom mode without the tool cannot.
2. **Recursion test**: a worker's tool context has nil `WorkerAgents` and nil
   `ResearchAgents`; spawning from a worker returns
   `worker_agents_unavailable`.
3. **Scope test**: worker schema excludes `create_agent_mode`, spawn tools,
   remote git mutations; includes `filesystem_edit_text`, `shell_command`.
4. **Lifecycle tests**: spawn → wait(all) → bounded report; follow-up cap;
   cancel mid-job marks pending jobs canceled (mirror existing research
   tests).
5. **Phase 2 claim test**: two workers, overlapping path → second write gets
   `path_claimed_by_agent`; non-overlapping → both succeed.
6. **Trajectory test** (`web/src/trajectory/trajectory.test.ts`): worker
   events render with kind badge.

## Rollout

1. Phase 1: worker kind + advisory partitioning + UI badge. Ship behind
   nothing (it's opt-in per mode permission anyway).
2. Phase 2: path-claim registry once real usage shows conflict pain.
3. Phase 3: per-worker branches only if demanded.
