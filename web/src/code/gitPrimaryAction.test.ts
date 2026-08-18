import { describe, expect, it } from "vitest";
import { shouldShowSyncAction } from "./gitPrimaryAction";
import type { GitChange, GitStatus } from "./gitTypes";

const change = (scope: GitChange["scope"]): GitChange => ({
  path: "changed.txt",
  status: "Modified",
  statusCode: "M",
  scope,
});

function status(overrides: Partial<GitStatus> = {}): GitStatus {
  return {
    workspaceId: "workspace",
    repositoryId: "repository",
    revision: 1,
    branch: "main",
    detached: false,
    upstream: "origin/main",
    ahead: 0,
    behind: 0,
    conflicts: [],
    staged: [],
    unstaged: [],
    totalChangeCount: 0,
    state: {},
    ...overrides,
  };
}

describe("Git primary action", () => {
  it.each([
    ["ahead", { ahead: 2 }],
    ["behind", { behind: 3 }],
    ["diverged", { ahead: 2, behind: 3 }],
  ])("offers Sync for a clean branch that is %s", (_label, overrides) => {
    expect(shouldShowSyncAction(status(overrides))).toBe(true);
  });

  it("keeps Commit when no upstream work is pending", () => {
    expect(shouldShowSyncAction(status())).toBe(false);
    expect(shouldShowSyncAction(status({ upstream: undefined, ahead: 1 }))).toBe(false);
  });

  it.each([
    ["conflicts", { conflicts: [change("conflict")] }],
    ["staged changes", { staged: [change("staged")] }],
    ["unstaged changes", { unstaged: [change("unstaged")] }],
    ["hidden staged changes", { hiddenStagedCount: 1 }],
    ["a merge", { state: { mergeInProgress: true } }],
    ["a rebase", { state: { rebaseInProgress: true } }],
    ["a cherry-pick", { state: { cherryPickInProgress: true } }],
  ])("keeps Commit while the repository has %s", (_label, overrides) => {
    expect(shouldShowSyncAction(status({ ahead: 1, ...overrides }))).toBe(false);
  });
});
