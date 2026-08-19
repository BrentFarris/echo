import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fixtures = vi.hoisted(() => ({ catalog: {
  safeMode: false,
  stages: [{
    id: "stage-1234567890abcdef", createdAt: new Date().toISOString(), source: { type: "local", path: "C:/plugin" },
    validation: { compatible: true, target: "windows-amd64", digest: "a".repeat(64), manifest: {
      id: "review-plugin", name: "Review Plugin", version: "1.0.0", description: "Pending review", runtime: { protocol: "echo-jsonrpc-1" },
      permissions: [{ name: "network", reason: "Connect to the reviewed service" }],
      contributes: {
        tools: [{ name: "review_plugin_lookup", description: "Look up a record" }],
        settings: [{ key: "api-token", type: "secret", scope: "global", label: "API token" }],
      },
    } },
  }],
  plugins: [{
    id: "installed-plugin", name: "Installed Plugin", version: "1.0.0", digest: "b".repeat(64),
    source: { type: "local", path: "C:/installed" }, globalEnabled: false, workspaceEnabled: true,
    effective: true, compatible: true, approvedTools: [], views: [],
    settings: [{ key: "private-key", type: "secret", scope: "workspace", label: "Private key", configured: true, secretSource: "os" }],
  }],
  missing: [], conflicts: [], retained: [],
} }));

const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return { settings: { endpoints: [] } };
    if (path === "/api/workspaces") return { workspaces: [{ id: "ws-1234567890abcdef", name: "Workspace", mainPath: "C:/workspace" }], activeId: "ws-1234567890abcdef" };
    if (path === "/api/agent-modes") return { modes: [], tools: [] };
    if (path === "/api/auth/status") return { transportSecure: true };
    if (path === "/api/auth/sessions") return { sessions: [] };
    if (path === "/api/plugins") return fixtures.catalog;
    return {};
  }),
  post: vi.fn(async () => ({})),
  put: vi.fn(async () => ({})),
  del: vi.fn(async () => ({})),
}));

vi.mock("../js/api.js", () => api);
vi.mock("./rebuildRelaunch.ts", () => ({ waitForReplacementServer: vi.fn(), reloadForReplacementServer: vi.fn() }));
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./navigation.ts", () => ({ codeRouteHash: vi.fn(() => "#/code"), navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: vi.fn(() => "") }));
vi.mock("../js/workspaces.js", () => ({ openAddWorkspaceModal: vi.fn(), openWorkspaceDropdown: vi.fn() }));

import { mount, unmount } from "../js/views/settings.js";

describe("plugin settings", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    root = document.createElement("div");
    document.body.append(root);
    mount(root);
    await vi.waitFor(() => expect(root.querySelector("[data-section=plugins]")).not.toBeNull());
    root.querySelector<HTMLButtonElement>("[data-section=plugins]")!.click();
    await vi.waitFor(() => expect(root.querySelector("[data-stage-id]")).not.toBeNull());
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    api.post.mockClear();
  });

  it("renders a complete trusted review and never renders secret values", async () => {
    expect(root.textContent).toContain("Native code warning");
    expect(root.textContent).toContain("review_plugin_lookup");
    expect(root.textContent).toContain("api-token");
    expect(root.textContent).toContain("Connect to the reviewed service");
    expect(root.textContent).not.toContain("actual-secret");
    const secret = root.querySelector<HTMLInputElement>('[data-plugin-secret="installed-plugin"] [data-secret-part="value"]')!;
    expect(secret.value).toBe("");
    expect(root.textContent).toContain("configured (os)");

    root.querySelector<HTMLButtonElement>('[data-plugin-action="approve-stage"][data-scope="workspace"]')!.click();
    await vi.waitFor(() => expect(api.post).toHaveBeenCalledWith(
      "/api/plugins/stages/stage-1234567890abcdef/approve",
      { scope: "workspace", workspaceId: "ws-1234567890abcdef", enable: true },
    ));
  });
});
