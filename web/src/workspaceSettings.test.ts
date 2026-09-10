import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fixture = vi.hoisted(() => ({
  workspaces: [] as Array<{ id: string; name: string; mainPath: string; folders: string[]; iconExt?: string }>,
  activeId: "",
}));

const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return { settings: { endpoints: [] } };
    if (path === "/api/workspaces") return { workspaces: fixture.workspaces.map((workspace) => ({ ...workspace })), activeId: fixture.activeId };
    if (path === "/api/agent-modes") return { modes: [], tools: [] };
    if (path === "/api/lsp/profiles") return { profiles: [], templates: [] };
    if (path.includes("/lsp/config")) return { config: {}, profiles: [], statuses: [] };
    if (path.includes("/testing/go/config")) return {
      config: { codeLens: true, coverage: true, timeout: "30s", flags: [], tags: "", environment: {} },
    };
    if (path.includes("/testing/c/config")) return { config: { codeLens: true, coverage: true, targets: [] } };
    if (path === "/api/auth/status") return { transportSecure: true };
    if (path === "/api/auth/sessions") return { sessions: [] };
    return {};
  }),
  post: vi.fn(async (path: string, body: Record<string, unknown>) => {
    if (path === "/api/workspaces") {
      const workspace = {
        id: "third", name: String(body.name), mainPath: String(body.mainPath),
        folders: [String(body.mainPath), ...((body.folders as string[]) || [])],
      };
      fixture.workspaces.push(workspace);
      return { workspace };
    }
    return {};
  }),
  put: vi.fn(async (path: string, body: Record<string, unknown>) => {
    if (path === "/api/settings") return { settings: body.settings || {} };
    if (path === "/api/workspaces/active") {
      fixture.activeId = String(body.id || "");
      return { activeId: fixture.activeId };
    }
    if (path.includes("/testing/go/config")) return { config: body.config };
    if (path.includes("/testing/c/config")) return { config: body.config };
    if (path.startsWith("/api/workspaces/")) {
      const id = decodeURIComponent(path.slice("/api/workspaces/".length));
      const index = fixture.workspaces.findIndex((workspace) => workspace.id === id);
      const current = fixture.workspaces[index];
      const updated = {
        ...current, name: String(body.name), folders: [current.mainPath, ...((body.folders as string[]) || [])],
        iconExt: body.removeIcon ? undefined : current.iconExt,
      };
      fixture.workspaces[index] = updated;
      return { workspace: updated };
    }
    return {};
  }),
  del: vi.fn(async (path: string) => {
    const id = decodeURIComponent(path.slice("/api/workspaces/".length));
    fixture.workspaces = fixture.workspaces.filter((workspace) => workspace.id !== id);
    if (fixture.activeId === id) fixture.activeId = fixture.workspaces[0]?.id || "";
    return { deletedId: id, activeId: fixture.activeId, workspaceFilesRetained: true };
  }),
}));

vi.mock("../js/api.js", () => api);
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./chatMap.ts", () => ({ installChatMap: vi.fn(() => vi.fn()) }));
vi.mock("./echoUpdate.ts", () => ({
  getEchoUpdateSnapshot: () => ({ status: null, checking: false, error: "" }),
  refreshEchoUpdateStatus: vi.fn(), syncEchoUpdateBadges: vi.fn(),
}));
vi.mock("./navigation.ts", () => ({
  chatTargetRouteHash: () => "#/home", codeRouteHash: () => "#/code", navigateBackFromSettings: vi.fn(),
}));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: () => "" }));
vi.mock("./plugins/catalog.ts", () => ({
  refreshPluginCatalog: vi.fn(async () => ({ safeMode: false, plugins: [], stages: [], missing: [], conflicts: [], retained: [] })),
}));

import { mount, unmount } from "../js/views/settings.js";

describe("workspace settings", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    fixture.workspaces = [
      { id: "first", name: "First", mainPath: "C:\\first", folders: ["C:\\first", "C:\\shared"], iconExt: "png" },
      { id: "second", name: "Second", mainPath: "C:\\second", folders: ["C:\\second"] },
    ];
    fixture.activeId = "first";
    root = document.createElement("div");
    document.body.append(root);
    mount(root);
    await vi.waitFor(() => expect(root.querySelector('[data-section="workspaces"]')).not.toBeNull());
    root.querySelector<HTMLButtonElement>('[data-section="workspaces"]')!.click();
    await vi.waitFor(() => expect(root.querySelectorAll(".workspace-row")).toHaveLength(2));
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    vi.clearAllMocks();
  });

  it("renders active metadata and opens the add workspace modal", () => {
    expect(root.textContent).toContain("1 additional folder");
    expect(root.textContent).toContain("Active");
    expect(root.querySelector('.workspace-row img')?.getAttribute("src")).toContain("/api/workspaces/first/icon");

    root.querySelector<HTMLButtonElement>('[data-action="add-settings-workspace"]')!.click();
    expect(document.querySelector('[data-add-workspace-backdrop]')).not.toBeNull();
  });

  it("adds a workspace and makes it active", async () => {
    root.querySelector<HTMLButtonElement>('[data-action="add-settings-workspace"]')!.click();
    document.querySelector<HTMLInputElement>('[data-field="name"]')!.value = "Third";
    document.querySelector<HTMLInputElement>('[data-folder-path="0"]')!.value = "C:\\third";
    document.querySelector<HTMLInputElement>('[data-folder-path="0"]')!.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector<HTMLButtonElement>('[data-action="save-workspace"]')!.click();

    await vi.waitFor(() => expect(api.post).toHaveBeenCalledWith("/api/workspaces", {
      name: "Third", folders: [], mainPath: "C:\\third",
    }));
    await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith("/api/workspaces/active", { id: "third" }));
    await vi.waitFor(() => expect(root.querySelectorAll(".workspace-row")).toHaveLength(3));
    expect(root.textContent).toContain("Added Third");
  });

  it("configures a workspace through the prepopulated editor", async () => {
    root.querySelector<HTMLButtonElement>('[data-action="configure-workspace"][data-workspace-id="first"]')!.click();
    const main = document.querySelector<HTMLInputElement>('[data-folder-path="0"]')!;
    expect(main.value).toBe("C:\\first");
    expect(main.readOnly).toBe(true);
    document.querySelector<HTMLInputElement>('[data-field="name"]')!.value = "Renamed";
    document.querySelector<HTMLButtonElement>('[data-action="save-workspace"]')!.click();

    await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith("/api/workspaces/first", {
      name: "Renamed", folders: ["C:\\shared"],
    }));
    await vi.waitFor(() => expect(root.textContent).toContain("Saved Renamed"));
    expect(root.textContent).toContain("Renamed");
  });

  it("confirms deletion, selects the replacement, and reports retained files", async () => {
    const changed = vi.fn();
    window.addEventListener("echo:workspace-changed", changed, { once: true });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    root.querySelector<HTMLButtonElement>('[data-action="delete-workspace"][data-workspace-id="first"]')!.click();

    await vi.waitFor(() => expect(api.del).toHaveBeenCalledWith("/api/workspaces/first"));
    await vi.waitFor(() => expect(root.querySelectorAll(".workspace-row")).toHaveLength(1));
    expect(root.textContent).toContain("Project files and .echo history were kept");
    expect(root.textContent).toContain("Second");
    expect(root.textContent).toContain("Active");
    expect(changed).toHaveBeenCalled();
  });

  it("configures package coverage in workspace Testing settings", async () => {
    root.querySelector<HTMLButtonElement>('[data-section="testing"]')!.click();
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledWith("/api/workspaces/first/testing/go/config"));
    const coverage = root.querySelector<HTMLInputElement>('[data-go-testing-field="coverage"]')!;
    expect(coverage.checked).toBe(true);
    coverage.click();
    root.querySelector<HTMLButtonElement>('[data-go-testing-action="save"]')!.click();
    await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith(
      "/api/workspaces/first/testing/go/config",
      { config: expect.objectContaining({ coverage: false }) },
    ));
  });

  it("adds and saves a structured gcov C target", async () => {
    root.querySelector<HTMLButtonElement>('[data-section="testing"]')!.click();
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledWith("/api/workspaces/first/testing/c/config"));
    root.querySelector<HTMLButtonElement>('[data-c-testing-action="add"]')!.click();
    expect(root.querySelectorAll("[data-c-testing-target]")).toHaveLength(1);
    const name = root.querySelector<HTMLInputElement>('[data-c-target-field="name"]')!;
    name.value = "Ledger tests";
    root.querySelector<HTMLButtonElement>('[data-c-testing-action="save"]')!.click();
    await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith(
      "/api/workspaces/first/testing/c/config",
      { config: expect.objectContaining({
        codeLens: true, coverage: true,
        targets: [expect.objectContaining({
          id: "c-tests-1", name: "Ledger tests", entry: { file: "${workspaceFolder}/tests/test_main.c", function: "main" },
          coverage: { provider: "gcov", objectRoots: ["${workspaceFolder}/build"] },
        })],
      }) },
    ));
  });

  it("switches conditional LLVM fields and removes C targets", async () => {
    root.querySelector<HTMLButtonElement>('[data-section="testing"]')!.click();
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledWith("/api/workspaces/first/testing/c/config"));
    root.querySelector<HTMLButtonElement>('[data-c-testing-action="add"]')!.click();
    const provider = root.querySelector<HTMLSelectElement>('[data-c-target-field="provider"]')!;
    provider.value = "llvm";
    provider.dispatchEvent(new Event("change", { bubbles: true }));
    expect(root.querySelector('[data-c-target-field="objects"]')).not.toBeNull();
    expect(root.querySelector('[data-c-target-field="objectRoots"]')).toBeNull();
    root.querySelector<HTMLButtonElement>('[data-c-testing-action="remove"]')!.click();
    expect(root.querySelectorAll("[data-c-testing-target]")).toHaveLength(0);
  });
});
