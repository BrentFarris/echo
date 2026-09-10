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
  capabilities: ["status", "diff", "history", "track", "protect", "commitAll", "commitSelected", "update", "sync", "pull", "push", "branches", "merge", "stashes", "webUI"],
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
    { id: "working", label: "Changes", role: "working", actions: ["discard", "protect", "commit_selected", "untrack"], changes: [{ path: "edited.txt", ref: { rootId: "root", path: "edited.txt" }, status: "Modified", statusCode: "EDITED", kind: "modified", groupId: "working" }] },
    { id: "untracked", label: "Untracked Files", role: "untracked", actions: ["protect", "track", "discard"], changes: [{ path: "new.txt", ref: { rootId: "root", path: "new.txt" }, status: "Untracked", statusCode: "EXTRA", kind: "untracked", groupId: "untracked" }] },
  ],
  totalChangeCount: 2,
  state: {},
});

const protectedStatus: SourceControlStatus = normalizeStatus({
  workspaceId: "workspace",
  repositoryId: repository.id,
  providerId: "fossil",
  revision: 2,
  branch: "trunk",
  detached: false,
  ahead: 0,
  behind: 0,
  groups: [
    { id: "protected", label: "Protected Changes", role: "included", actions: ["unprotect", "commit_protected"], changes: [{ path: "edited.txt", ref: { rootId: "root", path: "edited.txt" }, status: "Modified", statusCode: "EDITED", kind: "modified", groupId: "protected" }] },
    { id: "working", label: "Changes", role: "working", actions: ["discard", "protect"], changes: [{ path: "edited.txt", ref: { rootId: "root", path: "edited.txt" }, status: "Modified", statusCode: "EDITED", kind: "modified", groupId: "working" }] },
  ],
  totalChangeCount: 1,
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
    sourceControlAPI.runAction.mockResolvedValue({ requestId: "action", repositoryId: repository.id, revision: 3 });
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
    expect(host.querySelector("[data-git-file-action='protect']")).not.toBeNull();
    expect(host.querySelector("[data-git-file-action='stage']")).toBeNull();
    expect(host.querySelector("[data-git-file-action='unstage']")).toBeNull();
    expect(host.textContent).not.toContain("Staged Changes");
    expect(host.textContent).toContain("Fossil executable is missing");
  });

  it("renders protected and later versions as separate semantic layers", async () => {
    sourceControlAPI.loadStatus.mockResolvedValue(protectedStatus);
    const openDiff = vi.fn();
    const view = new SourceControlView(host, "workspace", controller.signal, {
      roots: () => [],
      openFile: vi.fn(),
      openDiff,
      updateBadge: vi.fn(),
    });
    await view.start();

    expect(host.querySelector(".git-commit-button")?.textContent?.trim()).toBe("Commit Protected");
    expect(host.textContent).toContain("Protected Changes");
    expect(host.querySelector("[data-git-file-action='unprotect']")).not.toBeNull();
    expect(host.querySelector("[data-git-file-action='protect']")).not.toBeNull();
    expect(host.querySelector("[data-git-group-action='unprotect_all']")).not.toBeNull();

    const protectedList = host.querySelector<HTMLElement>("[data-git-group-id='protected']");
    expect(protectedList?.querySelector("[data-git-file-action='open']")).toBeNull();
    protectedList?.querySelector<HTMLElement>("[data-git-change-index]")?.click();
    await vi.waitFor(() => expect(openDiff).toHaveBeenCalledWith(repository, expect.objectContaining({ groupId: "protected", scope: "included" }), false));

    host.querySelector<HTMLElement>("[data-git-group-id='working'] [data-git-group-action='protect_all']")?.click();
    await vi.waitFor(() => expect(sourceControlAPI.runAction).toHaveBeenCalledWith(
      "workspace",
      repository.id,
      expect.objectContaining({ action: "protect_all", paths: ["edited.txt"], expectedRevision: 2 }),
    ));
  });

  it("opens Fossil UI from the repository menu", async () => {
    const view = new SourceControlView(host, "workspace", controller.signal, {
      roots: () => [],
      openFile: vi.fn(),
      openDiff: vi.fn(),
      updateBadge: vi.fn(),
    });
    await view.start();

    host.querySelector<HTMLElement>("[data-git-repository='fossil-repository'] [data-git-repo-action='menu']")?.dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: 10, clientY: 10 }));
    const button = [...document.querySelectorAll<HTMLButtonElement>("[role='menuitem']")].find((item) => item.textContent?.includes("Fossil UI"));
    expect(button).toBeDefined();
    expect(button?.querySelector(".codicon-globe")).not.toBeNull();
    button?.click();
    await vi.waitFor(() => expect(sourceControlAPI.runAction).toHaveBeenCalledWith(
      "workspace",
      repository.id,
      expect.objectContaining({ action: "open_ui", expectedRevision: 1 }),
    ));
  });

  it("shows Fossil UI disabled with a sandbox explanation", async () => {
    sourceControlAPI.listRepositories.mockResolvedValue({
      providers: [{ id: "fossil", label: "Fossil", available: true, capabilities: repository.capabilities }],
      repositories: [{
        ...repository,
        actionAvailability: { open_ui: { enabled: false, diagnostic: "Disable the workspace sandbox first" } },
      }],
      searchParentRepositories: false,
    });
    const view = new SourceControlView(host, "workspace", controller.signal, {
      roots: () => [], openFile: vi.fn(), openDiff: vi.fn(), updateBadge: vi.fn(),
    });
    await view.start();

    host.querySelector<HTMLElement>("[data-git-repository='fossil-repository'] [data-git-repo-action='menu']")?.dispatchEvent(new MouseEvent("click", { bubbles: true, clientX: 10, clientY: 10 }));
    const button = [...document.querySelectorAll<HTMLButtonElement>("[role='menuitem']")].find((item) => item.textContent?.includes("Fossil UI"));
    expect(button?.disabled).toBe(true);
    expect(button?.textContent).toContain("Disable the workspace sandbox first");
    button?.click();
    expect(sourceControlAPI.runAction).not.toHaveBeenCalled();
  });
});
