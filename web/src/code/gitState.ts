import type { GitActionRequest, GitActionResult, GitStatus } from "./gitTypes";

// predictGitStatus mirrors only membership changes that a completed Git
// mutation guarantees. It never runs before the action response, and an
// authoritative snapshot at the same or a newer revision replaces it.
export function predictGitStatus(current: GitStatus, request: GitActionRequest, result: GitActionResult): GitStatus {
  if (current.revision >= result.revision) return current;
  const status: GitStatus = {
    ...current,
    revision: result.revision,
    conflicts: [...current.conflicts],
    staged: [...current.staged],
    unstaged: [...current.unstaged],
  };
  const paths = new Set((result.affectedPaths || ("paths" in request ? request.paths : []) || []).filter((path) => path !== "."));
  if (request.action === "stage" || request.action === "stage_all") {
    const moving = request.action === "stage_all"
      ? [...status.conflicts, ...status.unstaged]
      : [...status.conflicts, ...status.unstaged].filter((change) => paths.has(change.path));
    status.conflicts = status.conflicts.filter((change) => !moving.includes(change));
    status.unstaged = status.unstaged.filter((change) => !moving.includes(change));
    for (const change of moving) {
      if (!status.staged.some((candidate) => candidate.path === change.path)) {
        status.staged.push({
          ...change,
          scope: "staged",
          indexStatus: change.statusCode === "?" ? "A" : change.statusCode,
          statusCode: change.statusCode === "?" ? "A" : change.statusCode,
        });
      }
    }
  } else if (request.action === "unstage" || request.action === "unstage_all") {
    const moving = request.action === "unstage_all" ? [...status.staged] : status.staged.filter((change) => paths.has(change.path));
    status.staged = status.staged.filter((change) => !moving.includes(change));
    for (const change of moving) {
      if (!status.unstaged.some((candidate) => candidate.path === change.path)) {
        status.unstaged.push({
          ...change,
          scope: "unstaged",
          statusCode: change.indexStatus === "A" ? "?" : "M",
          worktreeStatus: change.indexStatus === "A" ? "?" : "M",
        });
      }
    }
  } else if (request.action === "discard" || request.action === "discard_all") {
    status.unstaged = request.action === "discard_all" ? [] : status.unstaged.filter((change) => !paths.has(change.path));
  } else if (request.action.startsWith("commit_")) {
    status.staged = [];
    if (request.action.includes("all")) status.unstaged = [];
  } else if (request.action === "sync") {
    status.ahead = 0;
    status.behind = 0;
  }
  status.totalChangeCount = new Set([...status.conflicts, ...status.staged, ...status.unstaged].map((change) => change.path)).size;
  return status;
}
