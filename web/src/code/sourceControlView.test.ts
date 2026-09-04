import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { normalizeStatus, type SourceControlStatus } from "./sourceControlTypes";

const sourceControlAPI = vi.hoisted(() => ({
  listRepositories: vi.fn(),
  loadStatus: vi.fn(),
  runAction: vi.fn(),
}));

vi.mock("./sourceControlApi", () => sourceControlAPI);
vi.mock("../../js/api.js", () => ({ api: vi.fn() }));
vi.mock("../../js/ws.js", () => ({
  on: vi.fn(() => vi.fn()),
  onState: vi.fn(() => vi.fn()),
  send: vi.fn(),
}));

import { SourceControlView } from "./sourceControlView";

const repository = {
  id: "fossil-repository",
  providerId: "fossil",
  providerLabel: "Fossil",
  label: "Project",
  rootRef: { rootId: "root", path: "" },
  parent: false,
  scopes: [{ rootId: "root", rootLabel: "Project", repoPrefix: "" }],
  revision: 1,
  available: true,
  capabilities: ["status", "diff", "history", "track", "commitAll", "commitSelected", "update", "sync", "pull", "push", "branches", "merge", "stashes"],
};

const status: SourceControlStatus = normalizeStatus({
  workspaceId: "workspace",
  repositoryId: repository.id,
  providerId: "fossil",
  revision: 1,
  branch: "trunk",
  detached: false,
  ahead: 0,
  behind: 0,
  groups: [
    { id: "working", label: "Changes", role: "working", actions: ["discard", "commit_selected", "untrack"], changes: [{ path: "edited.txt", ref: { rootId: "root", path: "edited.txt" }, status: "Modified", statusCode: "EDITED", kind: "modified", groupId: "working" }] },
    { id: "untracked", label: "Untracked Files", role: "untracked", actions: ["track", "discard"], changes: [{ path: "new.txt", ref: { rootId: "root", path: "new.txt" }, status: "Untracked", statusCode: "EXTRA", kind: "untracked", groupId: "untracked" }] },
  ],
  totalChangeCount: 2,
  state: {},
});

describe("Source Control Fossil view", () => {
  let host: HTMLElement;
  let controller: AbortController;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    controller = new AbortController();
    sourceControlAPI.listRepositories.mockResolvedValue({
      providers: [{ id: "fossil", label: "Fossil", available: true, capabilities: repository.capabilities }],
      repositories: [repository, { ...repository, id: "unavailable", label: "Unavailable", available: false, diagnostic: "Fossil executable is missing" }],
      searchParentRepositories: false,
    });
    sourceControlAPI.loadStatus.mockResolvedValue(status);
  });

  afterEach(() => {
    controller.abort();
    document.body.innerHTML = "";
    vi.clearAllMocks();
  });

  it("shows one provider-labelled hub without staging controls", async () => {
    const view = new SourceControlView(host, "workspace", controller.signal, {
      roots: () => [],
      openFile: vi.fn(),
      openDiff: vi.fn(),
      updateBadge: vi.fn(),
    });
    await view.start();

    expect(host.textContent).toContain("SOURCE CONTROL");
    expect(host.querySelector(".source-control-provider")?.textContent).toBe("Fossil");
    expect(host.querySelector(".git-commit-button")?.textContent?.trim()).toBe("Commit All");
    expect(host.textContent).toContain("Untracked Files");
    expect(host.querySelector("[data-git-file-action='track']")).not.toBeNull();
    expect(host.querySelector("[data-git-file-action='stage']")).toBeNull();
    expect(host.querySelector("[data-git-file-action='unstage']")).toBeNull();
    expect(host.textContent).not.toContain("Staged Changes");
    expect(host.textContent).toContain("Fossil executable is missing");
  });
});
