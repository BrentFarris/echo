import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { normalizeStatus, type SourceControlStatus } from "./sourceControlTypes";

const sourceControlAPI = vi.hoisted(() => ({
  listRepositories: vi.fn(),
  loadStatus: vi.fn(),
  loadHistory: vi.fn(),
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
    sourceControlAPI.loadHistory.mockResolvedValue({ commits: [], hasMore: false });
    sourceControlAPI.runAction.mockResolvedValue({ requestId: "action", repositoryId: repository.id, revision: 3 });
  });

  afterEach(() => {
    controller.abort();
    document.body.innerHTML = "";
    vi.clearAllMocks();
  });

  it("starts P4 groups collapsed and preserves independent expansion choices through refresh", async () => {
    const p4 = { ...repository, id: "p4-repo", providerId: "p4", providerLabel: "Perforce", capabilities: ["status", "diff", "changelists", "openForEdit", "reconcile", "revert"] };
    const p4Status = normalizeStatus({ ...status, repositoryId: p4.id, providerId: "p4", activeGroupId: "12", detectionIncomplete: true, groups: [
      { id: "default", label: "Default", role: "pending", keepEmpty: true, actions: [], changes: [] },
      { id: "12", label: "Change 12", role: "pending", keepEmpty: true, description: "Unshelved work", actions: ["revert"], changes: [{ path: "edited.txt", ref: { rootId: "root", path: "edited.txt" }, status: "modified", statusCode: "M", groupId: "12" }] },
      { id: "local", label: "Local changes awaiting P4", role: "local", keepEmpty: true, actions: ["reconcile_preview"], changes: [] },
    ] });
    sourceControlAPI.listRepositories.mockResolvedValue({ repositories: [p4], providers: [], searchParentRepositories: false });
    sourceControlAPI.loadStatus.mockResolvedValue(p4Status);
    const openDiff = vi.fn();
    const view = new SourceControlView(host, "workspace", controller.signal, { roots: () => [], openFile: vi.fn(), openDiff, updateBadge: vi.fn() });
    await view.start();

    const toggle = (id: string) => host.querySelector<HTMLButtonElement>(`[data-git-group-toggle='${id}']`)!;
    expect(host.querySelectorAll("[data-git-group-toggle][aria-expanded='false']")).toHaveLength(3);
    expect(host.querySelector(".git-change-list")).toBeNull();
    expect(host.querySelector(".p4-description")).toBeNull();
    expect(host.querySelector(".git-commit-button")).toBeNull();

    toggle("12").querySelector<HTMLElement>(".codicon")!.click();
    expect(toggle("12").getAttribute("aria-expanded")).toBe("true");
    expect(toggle("default").getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-git-group-id='12']")?.textContent).toContain("Unshelved work");
    expect(host.querySelector("[data-git-file-action='stage']")).toBeNull();
    host.querySelector<HTMLElement>("[data-git-group-id='12'] .git-change-row")!.click();
    await vi.waitFor(() => expect(openDiff).toHaveBeenCalledWith(p4, expect.objectContaining({ kind: "change", groupId: "12", path: "edited.txt", scope: "working" }), false));

    toggle("12").click();
    expect(toggle("12").getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-git-group-id='12'] .git-change-list")).toBeNull();
    expect(host.querySelector(".p4-description")).toBeNull();
    toggle("default").click();
    expect(toggle("default").getAttribute("aria-expanded")).toBe("true");

    sourceControlAPI.loadStatus.mockResolvedValue(normalizeStatus({ ...p4Status, revision: 2, groups: [
      ...p4Status.groups,
      { id: "13", label: "Change 13", role: "pending", keepEmpty: true, actions: [], changes: [] },
    ] }));
    await view.refreshStatus(p4.id);
    expect(toggle("12").getAttribute("aria-expanded")).toBe("false");
    expect(toggle("default").getAttribute("aria-expanded")).toBe("true");
    expect(toggle("13").getAttribute("aria-expanded")).toBe("false");
    expect(toggle("local").getAttribute("aria-expanded")).toBe("false");

    toggle("12").click();
    expect(host.querySelector("[data-git-group-id='12'] .git-change-row")).not.toBeNull();
    toggle("default").click();
    expect(toggle("default").getAttribute("aria-expanded")).toBe("false");
  });

  it("lets an initially expanded Fossil group stay collapsed after a status refresh", async () => {
    const view = new SourceControlView(host, "workspace", controller.signal, {
      roots: () => [], openFile: vi.fn(), openDiff: vi.fn(), updateBadge: vi.fn(),
    });
    await view.start();
    const toggle = () => host.querySelector<HTMLButtonElement>("[data-git-group-toggle='working']")!;
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    toggle().click();
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-git-group-id='working'] .git-change-row")).toBeNull();
    expect(host.querySelector("[data-git-group-id='untracked'] .git-change-row")).not.toBeNull();

    sourceControlAPI.loadStatus.mockResolvedValue(protectedStatus);
    await view.refreshStatus(repository.id);
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-git-group-id='working'] .git-change-row")).toBeNull();
    expect(host.querySelector("[data-git-group-id='protected'] .git-change-row")).not.toBeNull();
    toggle().click();
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector("[data-git-group-id='working'] .git-change-row")).not.toBeNull();
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

  it.each([".git-commit-button", ".git-repository-actions [data-git-repo-action='sync']"])(
    "offers animated Fossil Sync through %s after committing all protected changes", async (selector) => {
      const cleanStatus = normalizeStatus({
        ...status, revision: 3, totalChangeCount: 0,
        groups: status.groups.map((group) => ({ ...group, changes: [] })),
      });
      sourceControlAPI.loadStatus
        .mockResolvedValueOnce(normalizeStatus({ ...protectedStatus, groups: [protectedStatus.groups[0]] }))
        .mockResolvedValue(cleanStatus);
      const view = new SourceControlView(host, "workspace", controller.signal, {
        roots: () => [], openFile: vi.fn(), openDiff: vi.fn(), updateBadge: vi.fn(),
      });
      await view.start();

      const primaryButton = () => host.querySelector<HTMLButtonElement>(".git-commit-button")!;
      const headerButton = () => host.querySelector<HTMLButtonElement>(".git-repository-actions [data-git-repo-action='sync']")!;
      expect(primaryButton().textContent?.trim()).toBe("Commit Protected");
      const message = host.querySelector<HTMLTextAreaElement>("[data-git-commit-message]")!;
      message.value = "Commit protected changes";
      message.dispatchEvent(new Event("input", { bubbles: true }));
      primaryButton().click();
      await vi.waitFor(() => expect(sourceControlAPI.runAction).toHaveBeenCalledWith(
        "workspace", repository.id,
        expect.objectContaining({ action: "commit_protected", message: "Commit protected changes", expectedRevision: 2 }),
      ));
      await vi.waitFor(() => expect(primaryButton().textContent?.trim()).toBe("Sync"));
      expect(primaryButton().disabled).toBe(false);
      expect(headerButton().disabled).toBe(false);
      expect(host.querySelector<HTMLTextAreaElement>("[data-git-commit-message]")!.value).toBe("");
      expect(host.querySelector(".git-change-row")).toBeNull();

      let finishSync!: () => void;
      sourceControlAPI.runAction.mockImplementationOnce(() => new Promise((resolve) => {
        finishSync = () => resolve({ requestId: "sync", repositoryId: repository.id, revision: 4 });
      }));
      host.querySelector<HTMLButtonElement>(selector)!.click();
      expect(sourceControlAPI.runAction).toHaveBeenLastCalledWith(
        "workspace", repository.id, expect.objectContaining({ action: "sync", expectedRevision: 3 }),
      );
      for (const button of [primaryButton(), headerButton()]) {
        expect(button.disabled).toBe(true);
        expect(button.classList.contains("is-syncing")).toBe(true);
        expect(button.querySelector(".codicon-sync")).not.toBeNull();
      }

      finishSync();
      await vi.waitFor(() => expect(primaryButton().disabled).toBe(false));
      expect(primaryButton().classList.contains("is-syncing")).toBe(false);
      expect(headerButton().classList.contains("is-syncing")).toBe(false);

      sourceControlAPI.loadStatus.mockResolvedValue({ ...status, revision: 5 });
      await view.refreshStatus(repository.id);
      expect(primaryButton().textContent?.trim()).toBe("Commit All");
      expect(headerButton()).toBeNull();
    },
  );

  it("renders tagged and untagged commits when history is expanded", async () => {
    sourceControlAPI.loadHistory.mockResolvedValue({
      commits: [
        { hash: "tagged", parents: ["parent"], author: "Echo", authoredAt: "2026-09-12T12:00:00Z", refs: ["trunk"], subject: "Tagged commit" },
        { hash: "untagged", parents: [], author: "Echo", authoredAt: "2026-09-11T12:00:00Z", refs: [], subject: "Untagged commit" },
      ],
      hasMore: false,
    });
    const view = new SourceControlView(host, "workspace", controller.signal, {
      roots: () => [], openFile: vi.fn(), openDiff: vi.fn(), updateBadge: vi.fn(),
    });
    await view.start();

    host.querySelector<HTMLElement>("[data-git-history-toggle]")?.click();

    await vi.waitFor(() => expect(host.querySelectorAll(".git-history-row")).toHaveLength(2));
    expect([...host.querySelectorAll(".git-history-row strong")].map((element) => element.textContent)).toEqual(["Tagged commit", "Untagged commit"]);
    expect(host.textContent).toContain("trunk");
  });

  it("shows an explicit empty state for a repository without commits", async () => {
    const view = new SourceControlView(host, "workspace", controller.signal, {
      roots: () => [], openFile: vi.fn(), openDiff: vi.fn(), updateBadge: vi.fn(),
    });
    await view.start();

    host.querySelector<HTMLElement>("[data-git-history-toggle]")?.click();

    await vi.waitFor(() => expect(host.textContent).toContain("No commits yet"));
    expect(host.querySelector(".git-history-row")).toBeNull();
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
