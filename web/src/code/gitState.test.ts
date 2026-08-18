import { describe, expect, it } from "vitest";
import { predictGitStatus } from "./gitState";
import type { GitChange, GitStatus } from "./gitTypes";

function change(path: string, scope: GitChange["scope"], statusCode = "M"): GitChange {
  return { path, status: statusCode === "?" ? "added" : "modified", statusCode, scope };
}

function status(): GitStatus {
  return {
    workspaceId: "workspace", repositoryId: "repo", revision: 4, branch: "main", detached: false,
    ahead: 0, behind: 0, conflicts: [change("conflict.txt", "conflict", "U")],
    staged: [change("both.txt", "staged")],
    unstaged: [change("both.txt", "unstaged"), change("new.txt", "unstaged", "?")],
    totalChangeCount: 3, state: {},
  };
}

describe("predicted Git groups", () => {
  it("moves only requested paths after a successful stage", () => {
    const next = predictGitStatus(status(), { requestId: "1", action: "stage", paths: ["new.txt"] }, {
      requestId: "1", repositoryId: "repo", revision: 5, affectedPaths: ["new.txt"],
    });
    expect(next.unstaged.map((item) => item.path)).toEqual(["both.txt"]);
    expect(next.staged.map((item) => item.path)).toEqual(["both.txt", "new.txt"]);
    expect(next.staged[1].statusCode).toBe("A");
  });

  it("moves the complete visible groups for all-file actions", () => {
    const staged = predictGitStatus(status(), { requestId: "2", action: "stage_all" }, {
      requestId: "2", repositoryId: "repo", revision: 5, affectedPaths: ["."],
    });
    expect(staged.conflicts).toHaveLength(0);
    expect(staged.unstaged).toHaveLength(0);
    expect(staged.staged.map((item) => item.path).sort()).toEqual(["both.txt", "conflict.txt", "new.txt"]);
  });

  it("retains a newer snapshot instead of applying a stale action result", () => {
    const current = status();
    current.revision = 8;
    expect(predictGitStatus(current, { requestId: "3", action: "unstage_all" }, {
      requestId: "3", repositoryId: "repo", revision: 7,
    })).toBe(current);
  });

  it("preserves an existing unstaged side when unstaging a partially staged file", () => {
    const next = predictGitStatus(status(), { requestId: "4", action: "unstage", paths: ["both.txt"] }, {
      requestId: "4", repositoryId: "repo", revision: 5, affectedPaths: ["both.txt"],
    });
    expect(next.staged).toHaveLength(0);
    expect(next.unstaged.filter((item) => item.path === "both.txt")).toHaveLength(1);
  });
});
