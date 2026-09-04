import { describe, expect, it } from "vitest";
import { aggregateSourceControlChangeCount, sourceControlChangeIdentity } from "./sourceControlIdentity";
import { presentationFor } from "./sourceControlPresentation";
import { normalizeStatus, type SourceControlRepository, type SourceControlStatus } from "./sourceControlTypes";

const fossilRepository: SourceControlRepository = {
  id: "fossil-repository",
  providerId: "fossil",
  providerLabel: "Fossil",
  label: "Project",
  rootRef: { rootId: "root", path: "nested" },
  parent: false,
  scopes: [{ rootId: "root", rootLabel: "Project", repoPrefix: "" }],
  revision: 1,
  available: true,
  capabilities: ["status", "diff", "history", "track", "commitAll", "commitSelected", "update", "sync"],
};

function fossilStatus(groups: SourceControlStatus["groups"]): SourceControlStatus {
  return normalizeStatus({
    workspaceId: "workspace",
    repositoryId: fossilRepository.id,
    providerId: "fossil",
    revision: 1,
    branch: "trunk",
    detached: false,
    ahead: 0,
    behind: 0,
    groups,
    totalChangeCount: groups.reduce((sum, group) => sum + group.changes.length, 0),
    state: {},
  });
}

describe("Source Control presentation adapters", () => {
  it("models Fossil as a native no-staging workflow", () => {
    const status = fossilStatus([
      { id: "working", label: "Changes", role: "working", actions: ["discard", "commit_selected", "untrack"], changes: [{ path: "edited.txt", status: "Modified", statusCode: "EDITED", kind: "modified", groupId: "working" }] },
      { id: "untracked", label: "Untracked Files", role: "untracked", actions: ["track", "discard"], changes: [{ path: "new.txt", status: "Untracked", statusCode: "EXTRA", kind: "untracked", groupId: "untracked" }] },
    ]);
    const presentation = presentationFor(fossilRepository);
    expect(presentation.usesStagingArea).toBe(false);
    expect(presentation.commit(fossilRepository, status, [])).toMatchObject({ action: "commit_all", label: "Commit All", enabled: true });
    expect(presentation.commit(fossilRepository, status, [status.unstaged[0]])).toMatchObject({ action: "commit_selected", label: "Commit 1 Selected", enabled: true });
    expect(presentation.changeAction(fossilRepository, status.groups[1], status.unstaged[1])).toMatchObject({ action: "track" });
  });

  it("blocks Fossil commits while merge conflicts remain", () => {
    const status = fossilStatus([
      { id: "conflicts", label: "Merge Changes", role: "conflicts", actions: ["discard"], changes: [{ path: "conflict.txt", status: "Conflict", statusCode: "CONFLICT", kind: "conflict", groupId: "conflicts" }] },
    ]);
    expect(presentationFor(fossilRepository).commit(fossilRepository, status, []).enabled).toBe(false);
  });
});

describe("Source Control activity identity", () => {
  it("deduplicates Git and Fossil changes for the same workspace file", () => {
    const git = { ...fossilRepository, id: "git-repository", providerId: "git", providerLabel: "Git" };
    const change = { path: "same.txt", ref: { rootId: "root", path: "nested/same.txt" }, status: "Modified", statusCode: "M", groupId: "working" };
    const fossil = fossilStatus([{ id: "working", label: "Changes", role: "working", actions: [], changes: [change] }]);
    const gitStatus = { ...fossil, repositoryId: git.id, providerId: "git" };
    expect(sourceControlChangeIdentity(git, change)).toBe(sourceControlChangeIdentity(fossilRepository, change));
    expect(aggregateSourceControlChangeCount([git, fossilRepository], new Map([[git.id, gitStatus], [fossilRepository.id, fossil]]))).toBe(1);
  });

  it("does not collapse equally named files in different nested repositories", () => {
    const other = { ...fossilRepository, id: "other", rootRef: { rootId: "root", path: "elsewhere" } };
    const change = { path: "same.txt", status: "Modified", statusCode: "M", groupId: "working" };
    expect(sourceControlChangeIdentity(fossilRepository, change)).not.toBe(sourceControlChangeIdentity(other, change));
  });
});
