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

vi.mock("../js/api.js", () => api);
vi.mock("./rebuildRelaunch.ts", () => replacement);
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./navigation.ts", () => ({ codeRouteHash: vi.fn(() => "#/code"), navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: vi.fn(() => "") }));
vi.mock("../js/workspaces.js", () => ({
  openAddWorkspaceModal: vi.fn(),
  openWorkspaceDropdown: vi.fn(),
}));

import { mount, unmount } from "../js/views/settings.js";

describe("Development rebuild and relaunch", () => {
  let root: HTMLElement;

  beforeEach(async () => {
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
    replacement.waitForReplacementServer.mockClear();
    replacement.reloadForReplacementServer.mockClear();
  });

  it("builds, waits for the new server identity, and reloads", async () => {
    let finish!: (value: { instanceId: string; logPath: string }) => void;
    api.post.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));

    root.querySelector<HTMLButtonElement>("[data-action=rebuild-relaunch]")!.click();
    await Promise.resolve();

    expect(api.post).toHaveBeenCalledWith("/api/development/rebuild-relaunch", {});
    expect(root.querySelector<HTMLButtonElement>("[data-action=rebuild-relaunch]")!.disabled).toBe(true);
    expect(root.querySelector("[data-rebuild-status]")?.textContent).toContain("Building");

    finish({ instanceId: "old-instance", logPath: "C:\\Echo\\rebuild-relaunch.log" });
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(replacement.waitForReplacementServer).toHaveBeenCalledWith("old-instance");
    expect(replacement.reloadForReplacementServer).toHaveBeenCalledOnce();
  });

  it("keeps Echo usable and displays build errors with their log path", async () => {
    const error = Object.assign(new Error("server build failed"), {
      payload: { details: { logPath: "C:\\Echo\\rebuild-relaunch.log" } },
    });
    api.post.mockRejectedValueOnce(error);

    root.querySelector<HTMLButtonElement>("[data-action=rebuild-relaunch]")!.click();
    await Promise.resolve();
    await Promise.resolve();

    expect(root.querySelector<HTMLButtonElement>("[data-action=rebuild-relaunch]")!.disabled).toBe(false);
    expect(root.querySelector("[data-rebuild-status]")?.textContent).toContain("server build failed");
    expect(root.textContent).toContain("C:\\Echo\\rebuild-relaunch.log");
    expect(replacement.reloadForReplacementServer).not.toHaveBeenCalled();
  });

  it("does nothing when confirmation is canceled", () => {
    vi.mocked(window.confirm).mockReturnValue(false);
    root.querySelector<HTMLButtonElement>("[data-action=rebuild-relaunch]")!.click();
    expect(api.post).not.toHaveBeenCalled();
  });

  it("does not terminate Echo when confirmation is canceled", () => {
    vi.mocked(window.confirm).mockReturnValue(false);
    root.querySelector<HTMLButtonElement>("[data-action=terminate-echo]")!.click();
    expect(api.post).not.toHaveBeenCalled();
  });

  it("terminates Echo without waiting for a replacement", async () => {
    api.post.mockResolvedValueOnce({ status: "terminating" });

    root.querySelector<HTMLButtonElement>("[data-action=terminate-echo]")!.click();
    await Promise.resolve();
    await Promise.resolve();

    expect(api.post).toHaveBeenCalledWith("/api/development/terminate", {});
    expect(root.querySelector<HTMLButtonElement>("[data-action=terminate-echo]")!.disabled).toBe(true);
    expect(root.querySelector("[data-terminate-status]")?.textContent).toContain("shutting down");
    expect(replacement.waitForReplacementServer).not.toHaveBeenCalled();
    expect(replacement.reloadForReplacementServer).not.toHaveBeenCalled();
  });
});
