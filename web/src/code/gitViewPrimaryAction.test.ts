import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GitStatus } from "./gitTypes";

const gitAPI = vi.hoisted(() => ({
  listRepositories: vi.fn(),
  loadStatus: vi.fn(),
  runAction: vi.fn(),
}));

vi.mock("./gitApi", () => gitAPI);
vi.mock("../../js/api.js", () => ({ api: vi.fn() }));
vi.mock("../../js/ws.js", () => ({
  on: vi.fn(() => vi.fn()),
  onState: vi.fn(() => vi.fn()),
  send: vi.fn(),
}));

import { GitView } from "./gitView";

function cleanPendingStatus(): GitStatus {
  return {
    workspaceId: "workspace",
    repositoryId: "repository",
    revision: 1,
    branch: "main",
    detached: false,
    upstream: "origin/main",
    ahead: 1,
    behind: 0,
    conflicts: [],
    staged: [],
    unstaged: [],
    totalChangeCount: 0,
    state: {},
  };
}

describe("Git view primary action", () => {
  let host: HTMLElement;
  let controller: AbortController;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    controller = new AbortController();
    gitAPI.listRepositories.mockResolvedValue({
      repositories: [{ id: "repository", label: "Repository", parent: false, scopes: [], revision: 1 }],
      searchParentGitRepositories: false,
    });
    gitAPI.loadStatus.mockResolvedValue(cleanPendingStatus());
    gitAPI.runAction.mockResolvedValue({ requestId: "sync", repositoryId: "repository", revision: 2 });
  });

  afterEach(() => {
    controller.abort();
    document.body.innerHTML = "";
    vi.clearAllMocks();
  });

  it("renders Sync and runs the sync action for a clean branch with pending commits", async () => {
    const view = new GitView(host, "workspace", controller.signal, {
      roots: () => [],
      openFile: vi.fn(),
      openDiff: vi.fn(),
      updateBadge: vi.fn(),
    });
    await view.start();

    const button = host.querySelector<HTMLButtonElement>(".git-commit-button")!;
    expect(button.textContent?.trim()).toBe("Sync");
    expect(button.dataset.gitRepoAction).toBe("sync");
    expect(button.disabled).toBe(false);
    expect(host.querySelector("[title='Sync pending commits']")).not.toBeNull();

    button.click();
    await vi.waitFor(() => expect(gitAPI.runAction).toHaveBeenCalledWith(
      "workspace",
      "repository",
      expect.objectContaining({ action: "sync" }),
    ));
    await vi.waitFor(() => expect(host.querySelector(".git-commit-button")?.textContent?.trim()).toBe("Commit"));
  });
});
