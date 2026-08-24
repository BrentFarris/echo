import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const legacyEndpoint = {
  id: "legacy",
  name: "Legacy local model",
  endpoint: "http://localhost:11434/v1",
  model: "Qwen3.6-35B-A3B",
  thinkingTokenBudget: -1,
  thinkingCorrection: false,
};

const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return {
      settings: {
        endpoints: [{ ...legacyEndpoint }],
        endpointSelection: { chat: "legacy", research: "legacy", vision: "legacy", inlineCode: "legacy" },
      },
    };
    if (path === "/api/workspaces") return { workspaces: [], activeId: "" };
    if (path === "/api/auth/status") return { transportSecure: false };
    if (path === "/api/auth/sessions") return { sessions: [] };
    return {};
  }),
  post: vi.fn(),
  put: vi.fn(async (_path: string, body: { settings?: Record<string, unknown> }) => ({ settings: body.settings || {} })),
  del: vi.fn(),
}));

vi.mock("../js/api.js", () => api);
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./echoUpdate.ts", () => ({
  getEchoUpdateSnapshot: () => ({ status: null, checking: false, error: "" }),
  refreshEchoUpdateStatus: vi.fn(),
  syncEchoUpdateBadges: vi.fn(),
}));
vi.mock("./navigation.ts", () => ({ codeRouteHash: () => "#/code", navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: () => "" }));
vi.mock("../js/workspaces.js", () => ({ openAddWorkspaceModal: vi.fn(), openWorkspaceDropdown: vi.fn() }));

import { mount, unmount } from "../js/views/settings.js";

describe("endpoint reasoning effort settings", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    api.put.mockClear();
    root = document.createElement("div");
    document.body.appendChild(root);
    mount(root);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("keeps legacy profiles on token-budget mode and persists an explicit effort", async () => {
    root.querySelector<HTMLButtonElement>('[data-action="edit-endpoint"]')!.click();

    const effort = root.querySelector<HTMLSelectElement>('[data-endpoint-field="reasoningEffort"]')!;
    const budget = root.querySelector<HTMLInputElement>('[data-endpoint-field="thinkingTokenBudget"]')!;
    const correction = root.querySelector<HTMLInputElement>('[data-endpoint-field="thinkingCorrection"]')!;
    expect(effort.value).toBe("");
    expect(budget.disabled).toBe(false);

    effort.value = "none";
    effort.dispatchEvent(new Event("input", { bubbles: true }));
    expect(budget.disabled).toBe(true);
    expect(correction.disabled).toBe(true);

    effort.value = "max";
    effort.dispatchEvent(new Event("input", { bubbles: true }));
    expect(budget.disabled).toBe(true);
    expect(correction.disabled).toBe(false);

    root.querySelector<HTMLButtonElement>('[data-action="save-endpoint"]')!.click();
    await Promise.resolve();

    const settings = api.put.mock.calls.at(-1)?.[1].settings as { endpoints: Array<{ reasoningEffort?: string }> };
    expect(settings.endpoints[0].reasoningEffort).toBe("max");
  });

  it("defaults newly added endpoint profiles to Max", async () => {
    root.querySelector<HTMLButtonElement>('[data-action="add-endpoint"]')!.click();
    await Promise.resolve();

    const effort = root.querySelector<HTMLSelectElement>('[data-endpoint-field="reasoningEffort"]')!;
    const budget = root.querySelector<HTMLInputElement>('[data-endpoint-field="thinkingTokenBudget"]')!;
    expect(effort.value).toBe("max");
    expect(budget.disabled).toBe(true);

    const settings = api.put.mock.calls.at(-1)?.[1].settings as { endpoints: Array<{ reasoningEffort?: string }> };
    expect(settings.endpoints.at(-1)?.reasoningEffort).toBe("max");
  });
});
