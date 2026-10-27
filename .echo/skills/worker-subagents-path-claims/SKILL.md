---
name: worker-subagents-path-claims
description: Architecture of worker/research subagents and the Phase 2 path-claim registry that prevents concurrent workers from editing the same files.
triggers:
    - worker subagent
    - worker_agents_spawn
    - path_claimed_by_agent
    - path claim
    - subagent tool scope
    - workerChildToolNames
---

# Worker subagents and the path-claim registry

Echo has two subagent kinds sharing one run lifecycle in `internal/server/chat_research.go` (`chatResearchRun`, kind field `research` | `worker`):

- **Research agents** (`research_agents_spawn` family): read-only, use `ResearchLLMSchemaForScopes` (non-mutating allowlist `researchWorkerToolNames` in `internal/tools/registry.go`).
- **Worker agents** (`worker_agents_spawn` family, `internal/tools/worker_agents.go`): may edit files and run commands, use `WorkerLLMSchemaForScopes` (explicit allowlist `workerChildToolNames`). Workers get the parent turn's chat model (`workerSettings`/`workerStreamer`); researchers use the research model.

Key invariants:
- Child tool contexts always nil out `ResearchAgents`/`WorkerAgents` (`toolCtx` setup in `runAgentTurn`), so recursion fails with `*_agents_unavailable`.
- Child transcripts are private; parents receive only bounded reports via `waitResultLocked` (`reportMaxBytes`/`aggregateReportMaxBytes` scale to the parent context window).
- Mode gating: `modeAllowsWorkers`/`modeAllowsResearch` check tool permissions; Plan mode has neither.

**Path-claim registry (Phase 2, `internal/server/worker_path_claims.go`)**: prevents concurrent workers from silently clobbering the same files. `claimWorkerPaths(agentID, toolName, args)` is called in the child dispatch loop before `tools.Execute` for worker-kind agents only. It covers `filesystem_create_text`, `filesystem_edit_text`, `filesystem_delete_file`, `filesystem_move` (path + destination). Conflicts return `tools.ExecutionResult` with code `path_claimed_by_agent` and the owner's display name; claims are idempotent per owner, cover ancestor/descendant paths, and are released at every terminal state (runWorker exits, `finishCanceledAgent`, `cancelAgents`, `Close`). `shell_command` is a documented gap.

When adding a new mutating workspace tool: add it to `workerChildToolNames` (opt-in for workers) AND to `workerMutatingPathTools` with its path-bearing argument fields, plus a test. Design doc: `docs/worker-subagents-design.md`.
