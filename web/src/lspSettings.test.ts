import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => {
  const profile = {
    id: "gopls", name: "gopls", command: "gopls", args: [],
    selectors: [{ languageId: "go", extensions: [".go"] }],
  };
  return ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return { settings: { endpoints: [] } };
    if (path === "/api/workspaces") return { workspaces: [{ id: "workspace", name: "Demo" }], activeId: "workspace" };
    if (path === "/api/agent-modes") return { modes: [], tools: [] };
    if (path === "/api/lsp/profiles") return {
      profiles: [profile], templates: [{ id: "clangd", description: "Clang server", profile: { ...profile, id: "clangd", name: "clangd", command: "clangd" } }],
    };
    if (path === "/api/workspaces/workspace/lsp/config") return {
      config: { enabledProfileIds: [], formatOnSave: false, formatOnSaveTimeoutMs: 3000 }, profiles: [], statuses: [],
    };
    if (path === "/api/auth/status") return { transportSecure: false };
    if (path === "/api/auth/sessions") return { sessions: [] };
    return {};
  }),
  post: vi.fn(async () => ({ profile: { ...profile, id: "clangd", name: "clangd" } })),
  put: vi.fn(async (path: string, body: any) => {
    if (path.endsWith("/lsp/config")) return { config: body.config, profiles: [], statuses: [] };
    return { settings: body.settings || {} };
  }),
  del: vi.fn(async () => ({})),
  });
});

vi.mock("../js/api.js", () => api);
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./echoUpdate.ts", () => ({
  getEchoUpdateSnapshot: () => ({ status: null, checking: false, error: "" }),
  refreshEchoUpdateStatus: vi.fn(), syncEchoUpdateBadges: vi.fn(),
}));
vi.mock("./navigation.ts", () => ({ codeRouteHash: () => "#/code", navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: () => "" }));
vi.mock("../js/workspaces.js", () => ({ openAddWorkspaceModal: vi.fn(), openWorkspaceDropdown: vi.fn() }));
vi.mock("./plugins/catalog.ts", () => ({ refreshPluginCatalog: vi.fn(async () => ({ safeMode: false, plugins: [], stages: [], missing: [], conflicts: [], retained: [] })) }));
vi.mock("./completionNotifications.ts", () => ({
  completionNotificationPermission: () => "unsupported", requestCompletionNotificationPermission: vi.fn(), updateCompletionNotificationSettings: vi.fn(),
}));

import { mount, unmount } from "../js/views/settings.js";

async function flush(): Promise<void> {
  for (let index = 0; index < 10; index++) await Promise.resolve();
}

describe("language server settings", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    root = document.createElement("div");
    document.body.appendChild(root);
    api.get.mockClear();
    api.put.mockClear();
    api.post.mockClear();
    mount(root);
    await flush();
    root.querySelector<HTMLButtonElement>("[data-section=lsp]")!.click();
    await flush();
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("renders presets, global profiles, activation, and formatting controls", () => {
    expect(root.textContent).toContain("Built-in templates");
    expect(root.textContent).toContain("gopls");
    expect(root.querySelector("[data-lsp-enable=gopls]")).not.toBeNull();
    expect(root.querySelector("[data-lsp-config=formatOnSave]")).not.toBeNull();
  });

  it("copies presets and persists workspace activation", async () => {
    root.querySelector<HTMLButtonElement>("[data-lsp-action=add-template]")!.click();
    await flush();
    expect(api.post).toHaveBeenCalledWith("/api/lsp/profiles", { templateId: "clangd" });

    const enable = root.querySelector<HTMLInputElement>("[data-lsp-enable=gopls]")!;
    enable.checked = true;
    enable.dispatchEvent(new Event("change", { bubbles: true }));
    await flush();
    expect(api.put.mock.calls.some(([path, body]) => path === "/api/workspaces/workspace/lsp/config" && body.config.enabledProfileIds.includes("gopls"))).toBe(true);
  });
});
