import { beforeEach, describe, expect, it, vi } from "vitest";

const request = vi.hoisted(() => vi.fn());
const legacy = vi.hoisted(() => ({
  listRepositories: vi.fn(),
  loadStatus: vi.fn(),
  loadDiff: vi.fn(),
  loadMetadata: vi.fn(),
  loadHistory: vi.fn(),
  loadCommitDetail: vi.fn(),
  runAction: vi.fn(),
  setParentRepositorySearch: vi.fn(),
}));

vi.mock("../../js/api.js", () => ({ api: request }));
vi.mock("./gitApi", () => legacy);

import * as sourceControlAPI from "./sourceControlApi";

describe("Source Control transport compatibility", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("selects the legacy Git transport only when discovery proves the new endpoint is absent", async () => {
    const missing = Object.assign(new Error("missing"), { status: 404 });
    request.mockRejectedValueOnce(missing);
    legacy.listRepositories.mockResolvedValue({
      repositories: [{ id: "legacy-repository", label: "Legacy", parent: false, scopes: [], revision: 1 }],
      searchParentGitRepositories: true,
    });
    legacy.loadStatus.mockResolvedValue({
      workspaceId: "legacy-workspace", repositoryId: "legacy-repository", revision: 1,
      branch: "main", detached: false, ahead: 0, behind: 0, conflicts: [], staged: [], unstaged: [],
      totalChangeCount: 0, state: {},
    });

    const discovery = await sourceControlAPI.listRepositories("legacy-workspace");
    expect(discovery.repositories[0]).toMatchObject({ providerId: "git", providerLabel: "Git" });
    expect(discovery.searchParentRepositories).toBe(true);
    await sourceControlAPI.loadStatus("legacy-workspace", "legacy-repository");
    expect(legacy.loadStatus).toHaveBeenCalledOnce();
    expect(request).toHaveBeenCalledOnce();
  });

  it("never retries a failed provider-neutral mutation through the Git alias", async () => {
    request.mockResolvedValueOnce({ repositories: [], providers: [], searchParentRepositories: false });
    await sourceControlAPI.listRepositories("current-workspace");
    const failure = Object.assign(new Error("stale"), { status: 409 });
    request.mockRejectedValueOnce(failure);

    await expect(sourceControlAPI.runAction("current-workspace", "repository", {
      requestId: "request", action: "commit_all", expectedRevision: 3, message: "message",
    })).rejects.toBe(failure);
    expect(legacy.runAction).not.toHaveBeenCalled();
  });

  it("normalizes nullable commit collections from provider-neutral history", async () => {
    request.mockResolvedValueOnce({
      commits: [
        { hash: "tagged", parents: ["parent"], author: "Echo", authoredAt: "2026-09-12T12:00:00Z", refs: ["HEAD -> main"], subject: "Tagged" },
        { hash: "untagged", parents: null, author: "Echo", authoredAt: "2026-09-11T12:00:00Z", refs: null, subject: "Untagged" },
        { hash: "missing", author: "Echo", authoredAt: "2026-09-10T12:00:00Z", subject: "Missing arrays" },
      ],
      hasMore: false,
    });

    const history = await sourceControlAPI.loadHistory("history-current-workspace", "repository");

    expect(history.commits.map((commit) => commit.hash)).toEqual(["tagged", "untagged", "missing"]);
    expect(history.commits[1]).toMatchObject({ parents: [], refs: [] });
    expect(history.commits[2]).toMatchObject({ parents: [], refs: [] });
  });

  it("normalizes a nullable legacy commit list", async () => {
    const missing = Object.assign(new Error("missing"), { status: 404 });
    request.mockRejectedValueOnce(missing);
    legacy.listRepositories.mockResolvedValue({ repositories: [], searchParentGitRepositories: false });
    legacy.loadHistory.mockResolvedValue({ commits: null, hasMore: false });
    await sourceControlAPI.listRepositories("history-legacy-workspace");

    const history = await sourceControlAPI.loadHistory("history-legacy-workspace", "repository");

    expect(history).toEqual({ commits: [], hasMore: false });
  });
});
