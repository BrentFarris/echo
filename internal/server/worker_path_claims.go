package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Phase 2 of the worker-subagent design (docs/worker-subagents-design.md): a
// per-turn path-claim registry. When several worker subagents run
// concurrently, the first worker to mutate a workspace path claims it; a later
// mutating call against the same path (or a path inside it) by a different
// worker fails with a typed error instead of silently clobbering the file.
// Claims are acquired on first write and released when the worker reaches a
// terminal state. shell_command remains a documented gap: it runs in the
// sandbox and cannot be cheaply intercepted per path.

// workerMutatingPathTools maps the worker-allowlisted tools that create or
// modify workspace files to the argument fields carrying the target paths.
// It must stay in sync with the workerChildToolNames allowlist in
// internal/tools/registry.go.
var workerMutatingPathTools = map[string][]string{
	"filesystem_create_text": {"path"},
	"filesystem_edit_text":   {"path"},
	"filesystem_delete_file": {"path"},
	"filesystem_move":        {"path", "destination"},
}

// workerPathClaimConflictCode is the ExecutionError code returned to a worker
// whose mutating tool call targets a path claimed by another agent.
const workerPathClaimConflictCode = "path_claimed_by_agent"

// extractWorkerClaimPaths returns the normalized workspace paths a mutating
// tool call would touch, or nil for other tools and unparsable arguments.
func extractWorkerClaimPaths(toolName, arguments string) []string {
	fields, ok := workerMutatingPathTools[toolName]
	if !ok || strings.TrimSpace(arguments) == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return nil
	}
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		value, _ := parsed[field].(string)
		if normalized := normalizeWorkerClaimPath(value); normalized != "" {
			paths = append(paths, normalized)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return paths
}

func normalizeWorkerClaimPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "./")
	return strings.TrimRight(path, "/")
}

// workerPathsConflict reports whether claiming newPath would collide with an
// existing claim: identical paths, or one path located inside the other.
func workerPathsConflict(existing, newPath string) bool {
	return existing == newPath ||
		strings.HasPrefix(newPath, existing+"/") ||
		strings.HasPrefix(existing, newPath+"/")
}

// claimWorkerPaths records agentID's claim over every path the mutating tool
// call touches. When another agent already holds a conflicting claim it
// returns (ownerLabel, path, true) and claims nothing; otherwise it claims
// all paths and returns ("", "", false). Claims are idempotent for the
// owning agent, so follow-up jobs re-acquire without friction.
func (r *chatResearchRun) claimWorkerPaths(agentID, toolName, arguments string) (string, string, bool) {
	paths := extractWorkerClaimPaths(toolName, arguments)
	if len(paths) == 0 {
		return "", "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workerPathClaims == nil {
		r.workerPathClaims = make(map[string]string)
	}
	for existing, owner := range r.workerPathClaims {
		if owner == agentID {
			continue
		}
		for _, path := range paths {
			if workerPathsConflict(existing, path) {
				return r.workerClaimOwnerLabelLocked(owner), path, true
			}
		}
	}
	for _, path := range paths {
		r.workerPathClaims[path] = agentID
	}
	return "", "", false
}

// workerClaimOwnerLabelLocked resolves a human-readable owner label for
// conflict messages. Callers must hold r.mu.
func (r *chatResearchRun) workerClaimOwnerLabelLocked(agentID string) string {
	if agent, ok := r.agents[agentID]; ok && agent.name != "" {
		return fmt.Sprintf("%s (%s)", agent.name, agentID)
	}
	return agentID
}

// releaseWorkerPathClaims drops every claim held by agentID. Called when a
// worker reaches a terminal state so later workers can reuse the paths.
func (r *chatResearchRun) releaseWorkerPathClaims(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseWorkerPathClaimsLocked(agentID)
}

func (r *chatResearchRun) releaseWorkerPathClaimsLocked(agentID string) {
	for path, owner := range r.workerPathClaims {
		if owner == agentID {
			delete(r.workerPathClaims, path)
		}
	}
}
