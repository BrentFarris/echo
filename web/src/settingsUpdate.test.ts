import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return { settings: { endpoints: [] } };
    if (path === "/api/workspaces") return { workspaces: [], activeId: "" };
    if (path === "/api/auth/status") return { transportSecure: false };
    if (path === "/api/auth/sessions") return { sessions: [] };
    return {};
  }),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}));

const replacement = vi.hoisted(() => ({
  waitForReplacementServer: vi.fn(async () => undefined),
  reloadForReplacementServer: vi.fn(),
}));

const updateMonitor = vi.hoisted(() => ({
  snapshot: {
    status: {
      updateAvailable: true,
      localMasterCommit: "1111111111111111111111111111111111111111",
      remoteMasterCommit: "2222222222222222222222222222222222222222",
      checkedAt: "2026-08-19T12:00:00Z",
    } as null | Record<string, unknown>,
    checking: false,
    error: "",
  },
  refresh: vi.fn<() => Promise<{
    status: null | Record<string, unknown>;
    checking: boolean;
    error: string;
  }>>(),
}));

vi.mock("../js/api.js", () => api);
vi.mock("./rebuildRelaunch.ts", () => replacement);
vi.mock("./echoUpdate.ts", () => ({
  getEchoUpdateSnapshot: () => updateMonitor.snapshot,
  refreshEchoUpdateStatus: updateMonitor.refresh,
  syncEchoUpdateBadges: (root: ParentNode) => {
    const available = updateMonitor.snapshot.status?.updateAvailable === true;
    root.querySelectorAll<HTMLElement>("[data-echo-update-badge]").forEach((badge) => { badge.hidden = !available; });
  },
}));
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./navigation.ts", () => ({ codeRouteHash: vi.fn(() => "#/code"), navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: vi.fn(() => "") }));
vi.mock("../js/workspaces.js", () => ({ openAddWorkspaceModal: vi.fn(), openWorkspaceDropdown: vi.fn() }));

import { mount, unmount } from "../js/views/settings.js";

describe("Development Echo update", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    updateMonitor.snapshot = {
      status: {
        updateAvailable: true,
        localMasterCommit: "1111111111111111111111111111111111111111",
        remoteMasterCommit: "2222222222222222222222222222222222222222",
        checkedAt: "2026-08-19T12:00:00Z",
      },
      checking: false,
      error: "",
    };
    updateMonitor.refresh.mockImplementation(async () => updateMonitor.snapshot);
    root = document.createElement("div");
    document.body.appendChild(root);
    mount(root);
    await Promise.resolve();
    await Promise.resolve();
    root.querySelector<HTMLButtonElement>("[data-section=development]")!.click();
    vi.spyOn(window, "confirm").mockReturnValue(true);
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    api.post.mockReset();
    updateMonitor.refresh.mockClear();
    replacement.waitForReplacementServer.mockClear();
    replacement.reloadForReplacementServer.mockClear();
  });

  it("offers an immediate check, shows the Development arrow, and updates through the replacement server", async () => {
    updateMonitor.snapshot = {
      status: { ...updateMonitor.snapshot.status!, updateAvailable: false },
      checking: false,
      error: "",
    };
    window.dispatchEvent(new CustomEvent("echo:update-status", { detail: updateMonitor.snapshot }));
    expect(root.querySelector("[data-action=update-echo]")).toBeNull();
    expect(root.querySelector("[data-action=check-for-updates]")?.textContent).toBe("Check for Updates");

    root.querySelector<HTMLButtonElement>("[data-action=check-for-updates]")!.click();
    await Promise.resolve();
    await Promise.resolve();

    expect(updateMonitor.refresh).toHaveBeenCalledOnce();
    expect(root.querySelector("[data-update-status]")?.textContent).toContain("up to date");

    updateMonitor.snapshot = {
      status: { ...updateMonitor.snapshot.status!, updateAvailable: true },
      checking: false,
      error: "",
    };
    window.dispatchEvent(new CustomEvent("echo:update-status", { detail: updateMonitor.snapshot }));
    expect(root.querySelector<HTMLElement>("[data-section=development] [data-echo-update-badge]")!.hidden).toBe(false);
    expect(root.querySelector("[data-action=update-echo]")).not.toBeNull();
    expect(root.textContent).not.toContain("Echo is up to date.");

    let finish!: (value: { instanceId: string; logPath: string }) => void;
    api.post.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    root.querySelector<HTMLButtonElement>("[data-action=update-echo]")!.click();
    await Promise.resolve();

    expect(api.post).toHaveBeenCalledWith("/api/development/update", {});
    expect(root.querySelector<HTMLButtonElement>("[data-action=update-echo]")!.disabled).toBe(true);
    expect(root.querySelector<HTMLButtonElement>("[data-action=rebuild-relaunch]")!.disabled).toBe(true);

    finish({ instanceId: "old-instance", logPath: "C:\\Echo\\rebuild-relaunch.log" });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(replacement.waitForReplacementServer).toHaveBeenCalledWith("old-instance");
    expect(replacement.reloadForReplacementServer).toHaveBeenCalledOnce();
  });

  it("keeps Echo running and displays update errors with the log path", async () => {
    api.post.mockRejectedValueOnce(Object.assign(new Error("master must be checked out"), {
      payload: { details: { logPath: "C:\\Echo\\rebuild-relaunch.log" } },
    }));
    root.querySelector<HTMLButtonElement>("[data-action=update-echo]")!.click();
    await Promise.resolve();
    await Promise.resolve();

    expect(root.querySelector("[data-update-status]")?.textContent).toContain("master must be checked out");
    expect(root.textContent).toContain("C:\\Echo\\rebuild-relaunch.log");
    expect(replacement.reloadForReplacementServer).not.toHaveBeenCalled();
  });

  it("offers retry after check errors", () => {
    updateMonitor.snapshot = { status: null, checking: false, error: "GitHub unavailable" };
    window.dispatchEvent(new CustomEvent("echo:update-status", { detail: updateMonitor.snapshot }));

    expect(root.querySelector("[data-update-check-error]")?.textContent).toContain("GitHub unavailable");
    root.querySelector<HTMLButtonElement>("[data-action=retry-update-check]")!.click();
    expect(updateMonitor.refresh).toHaveBeenCalledOnce();
  });
});
