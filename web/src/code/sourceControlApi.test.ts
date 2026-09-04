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
});
