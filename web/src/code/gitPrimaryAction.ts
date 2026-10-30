import type { GitStatus } from "./gitTypes";

export function shouldShowSyncAction(status: GitStatus | undefined): boolean {
  if (!status?.upstream || (status.ahead <= 0 && status.behind <= 0)) return false;
  if (status.conflicts.length || status.staged.length || status.unstaged.length || status.hiddenStagedCount) return false;
  return !status.state.mergeInProgress
    && !status.state.rebaseInProgress
    && !status.state.cherryPickInProgress;
}
